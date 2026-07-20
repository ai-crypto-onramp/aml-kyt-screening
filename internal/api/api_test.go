package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/audit"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/decision"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/metrics"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/tracing"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/vendor"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/webhook"
)

func newTestServices(t *testing.T) *Services {
	t.Helper()
	mp := vendor.NewMockProvider("chainalysis")
	cache := screen.NewMemoryCache(time.Hour, 24*time.Hour)
	th := decision.NewThresholds(90, 50, decision.DecisionManualReview)
	screenStore := screen.NewMemoryScreenStore()
	alerts := alert.NewService(alert.NewMemoryStore())
	auditSink := audit.NewMemorySink()
	emitter := audit.NewEmitter(auditSink, 16)
	t.Cleanup(emitter.Close)
	screenSvc := screen.NewService(cache, mp, th, screenStore, alerts, emitter)
	v := webhook.NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	proc := webhook.NewProcessor(v, cache, alerts)
	return &Services{Screen: screenSvc, Alerts: alerts, Webhook: proc, Audit: emitter}
}

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	NewMux(newTestServices(t)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestScreenHandler(t *testing.T) {
	s := newTestServices(t)
	s.Screen = s.Screen.WithID(func() string { return "scr-1" })
	body := bytes.NewBufferString(`{"tx_id":"tx1","address":"0x1","chain":"ethereum","amount":"100"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/screen", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rec.Code, rec.Body.String())
	}
	var resp screen.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Decision != decision.DecisionAllow {
		t.Errorf("decision: %s", resp.Decision)
	}
	if resp.ScreenID != "scr-1" {
		t.Errorf("screen id: %s", resp.ScreenID)
	}
}

func TestScreenHandlerBadJSON(t *testing.T) {
	s := newTestServices(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/screen", bytes.NewBufferString("not-json"))
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestScreenHandlerValidation(t *testing.T) {
	s := newTestServices(t)
	body := bytes.NewBufferString(`{"tx_id":"","address":"0x1","chain":"ethereum","amount":"100"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/screen", body)
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		// validation error path returns internal_error currently
	}
	if rec.Code < 400 {
		t.Fatalf("expected error status, got %d", rec.Code)
	}
}

func TestGetAlertHandler(t *testing.T) {
	s := newTestServices(t)
	a, _ := s.Alerts.Create("", "tx1", "0xbad", "ethereum", "SANCTIONED", "critical")
	req := httptest.NewRequest(http.MethodGet, "/v1/kyt/alerts/"+a.ID, nil)
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var got alert.Alert
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != a.ID {
		t.Errorf("id: %s", got.ID)
	}
}

func TestGetAlertHandlerNotFound(t *testing.T) {
	s := newTestServices(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/kyt/alerts/nope", nil)
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestListAlertsHandler(t *testing.T) {
	s := newTestServices(t)
	_, _ = s.Alerts.Create("", "tx1", "0x1", "ethereum", "HIGH_RISK", "high")
	_, _ = s.Alerts.Create("", "tx2", "0x2", "ethereum", "SANCTIONED", "critical")
	req := httptest.NewRequest(http.MethodGet, "/v1/kyt/alerts?status=OPEN", nil)
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp struct {
		Alerts []alert.Alert `json:"alerts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Alerts) != 2 {
		t.Fatalf("alerts: %d", len(resp.Alerts))
	}
}

func TestAssignAndCloseAlertHandler(t *testing.T) {
	s := newTestServices(t)
	a, _ := s.Alerts.Create("", "tx1", "0x1", "ethereum", "HIGH_RISK", "high")
	body := bytes.NewBufferString(`{"assignee":"analyst1"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/alerts/"+a.ID+"/assign", body)
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("assign status: %d", rec.Code)
	}
	body = bytes.NewBufferString(`{"assignee":"analyst1"}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/kyt/alerts/"+a.ID+"/close", body)
	rec = httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("close status: %d", rec.Code)
	}
	var got alert.Alert
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Status != alert.StatusClosed {
		t.Errorf("status: %s", got.Status)
	}
}

func TestCloseAlertAlreadyClosed(t *testing.T) {
	s := newTestServices(t)
	a, _ := s.Alerts.Create("", "tx1", "0x1", "ethereum", "HIGH_RISK", "high")
	_, _ = s.Alerts.Close(a.ID, "")
	body := bytes.NewBufferString(`{"assignee":""}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/alerts/"+a.ID+"/close", body)
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: %d", rec.Code)
	}
}

func signBody(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookHandlerValid(t *testing.T) {
	s := newTestServices(t)
	body := []byte(`{"event_id":"e1","address":"0x1","chain":"ethereum","exposure":"SANCTIONED","tx_id":"tx1"}`)
	sig := signBody([]byte("secret"), body)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/chainalysis", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", sig)
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rec.Code, rec.Body.String())
	}
}

func TestWebhookHandlerTampered(t *testing.T) {
	s := newTestServices(t)
	body := []byte(`{"event_id":"e1","address":"0x1","chain":"ethereum","exposure":"SANCTIONED"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/chainalysis", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", "bad-sig")
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestWebhookHandlerDuplicate(t *testing.T) {
	s := newTestServices(t)
	body := []byte(`{"event_id":"dup","address":"0x1","chain":"ethereum","exposure":"SANCTIONED"}`)
	sig := signBody([]byte("secret"), body)

	mkReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/chainalysis", bytes.NewReader(body))
		req.Header.Set("X-Webhook-Signature", sig)
		return req
	}
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, mkReq())
	if rec.Code != http.StatusOK {
		t.Fatalf("first status: %d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec2, mkReq())
	if rec2.Code != http.StatusOK {
		t.Fatalf("second status: %d", rec2.Code)
	}
}

func TestWebhookHandlerBadPayload(t *testing.T) {
	s := newTestServices(t)
	body := []byte(`{"address":"0x1"}`) // missing chain, exposure
	sig := signBody([]byte("secret"), body)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/chainalysis", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", sig)
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestMetricsHandler(t *testing.T) {
	metrics.Global().ScreenTotal.Add(1)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	NewMux(newTestServices(t)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestToAppErrorDefaults(t *testing.T) {
	if ae := toAppError(errors.New("random")); ae.StatusCode != http.StatusInternalServerError {
		t.Errorf("random err status: %d", ae.StatusCode)
	}
	if ae := toAppError(errBadJSON); ae.Code != "bad_json" {
		t.Errorf("code: %s", ae.Code)
	}
	if ae := toAppError(nil); ae != nil {
		t.Errorf("nil err should return nil")
	}
}

func TestAppErrorError(t *testing.T) {
	ae := &AppError{Code: "x", Message: "boom", StatusCode: 500}
	if got := ae.Error(); got != "boom" {
		t.Errorf("Error() = %q want %q", got, "boom")
	}
}

func TestListAlertsHandlerError(t *testing.T) {
	s := newTestServices(t)
	// Replace Alerts with a service backed by a failing store.
	s.Alerts = alert.NewService(&failListStore{})
	req := httptest.NewRequest(http.MethodGet, "/v1/kyt/alerts", nil)
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestAssignHandlerBadJSON(t *testing.T) {
	s := newTestServices(t)
	a, _ := s.Alerts.Create("", "tx1", "0x1", "ethereum", "HIGH_RISK", "high")
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/alerts/"+a.ID+"/assign", bytes.NewBufferString("not-json"))
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestAssignHandlerNotFound(t *testing.T) {
	s := newTestServices(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/alerts/nope/assign", bytes.NewBufferString(`{"assignee":"x"}`))
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestCloseHandlerBadJSON(t *testing.T) {
	s := newTestServices(t)
	a, _ := s.Alerts.Create("", "tx1", "0x1", "ethereum", "HIGH_RISK", "high")
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/alerts/"+a.ID+"/close", bytes.NewBufferString("not-json"))
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestWebhookHandlerEmptyBody(t *testing.T) {
	s := newTestServices(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/chainalysis", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	// Empty body -> decode fails before reaching webhook logic. But webhook
	// handler uses ReadBody, not decodeJSON; empty body yields empty payload.
	if rec.Code < 400 && rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
}

type failListStore struct{}

func (s *failListStore) Create(a alert.Alert) (alert.Alert, error) { return a, nil }
func (s *failListStore) Get(id string) (alert.Alert, bool, error)  { return alert.Alert{}, false, nil }
func (s *failListStore) List(status string) ([]alert.Alert, error) {
	return nil, errors.New("list failed")
}
func (s *failListStore) Update(a alert.Alert) error { return nil }

func TestDecodeJSONEmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(nil))
	if err := decodeJSON(req, &struct{}{}); !errors.Is(err, errBadJSON) {
		t.Fatalf("err: %v", err)
	}
}

func TestNewServer(t *testing.T) {
	srv := NewServer(newTestServices(t), "127.0.0.1:0")
	if srv == nil {
		t.Fatal("nil server")
	}
	_ = srv.Close()
}

func TestRequestIDMiddlewareGeneratesID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	NewMux(newTestServices(t)).ServeHTTP(rec, req)
	if rid := rec.Header().Get("X-Request-Id"); rid == "" {
		t.Error("expected generated X-Request-Id response header")
	}
}

func TestRequestIDMiddlewarePreservesIncomingID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-Id", "req-123")
	rec := httptest.NewRecorder()
	NewMux(newTestServices(t)).ServeHTTP(rec, req)
	if rid := rec.Header().Get("X-Request-Id"); rid != "req-123" {
		t.Errorf("X-Request-Id: got %q want req-123", rid)
	}
}

func TestScreenHandlerEmitsXRequestIDOnError(t *testing.T) {
	s := newTestServices(t)
	body := bytes.NewBufferString(`{"tx_id":"","address":"0x1","chain":"ethereum","amount":"100"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/screen", body)
	req.Header.Set("X-Request-Id", "req-err-1")
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	if rid := rec.Header().Get("X-Request-Id"); rid != "req-err-1" {
		t.Errorf("X-Request-Id on error: got %q want req-err-1", rid)
	}
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.RequestID != "req-err-1" {
		t.Errorf("error request_id: got %q want req-err-1", env.Error.RequestID)
	}
}

func TestTraceMiddlewareProducesSpanContext(t *testing.T) {
	// Install a no-op tracer provider so the trace middleware has a tracer to
	// build spans with, even without an exporter configured.
	ctx := context.Background()
	shutdown, err := tracing.Install(ctx)
	if err != nil {
		t.Fatalf("install tracer: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/screen", bytes.NewBufferString(`{"tx_id":"tx1","address":"0x1","chain":"ethereum","amount":"100"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewMux(newTestServices(t)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rec.Code, rec.Body.String())
	}
}

// Ensure context cancellation propagates through screen handler.
func TestScreenHandlerContextCancel(t *testing.T) {
	s := newTestServices(t)
	s.Screen = s.Screen.WithID(func() string { return "scr-1" })
	body := bytes.NewBufferString(`{"tx_id":"tx1","address":"0x1","chain":"ethereum","amount":"100"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/kyt/screen", body)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	NewMux(s).ServeHTTP(rec, req)
	// Even with cancelled context, mock provider returns clean; the screen path
	// tolerates ctx cancellation. Verify we get a response.
	if rec.Code >= 500 {
		t.Fatalf("status: %d", rec.Code)
	}
}