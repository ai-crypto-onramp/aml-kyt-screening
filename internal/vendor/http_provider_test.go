package vendor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestBreaker() *CircuitBreaker { return NewCircuitBreaker(5, time.Minute) }

func TestHTTPProviderSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Idempotency-Key") == "" {
			t.Errorf("missing idempotency header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"risk_score": 42, "exposure": "high_risk"})
	}))
	defer srv.Close()

	p := NewHTTPProvider("chainalysis", "test-key", srv.URL, 5*time.Second, ChainalysisEncoder, ChainalysisDecoder, newTestBreaker())
	resp, err := p.Screen(context.Background(), ScreenRequest{TxID: "tx1", Address: "0x1", Chain: "ethereum"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.RiskScore != 42 || resp.Exposure != "high_risk" {
		t.Fatalf("resp: %+v", resp)
	}
	if resp.Vendor != "chainalysis" {
		t.Errorf("vendor: %s", resp.Vendor)
	}
	if len(resp.RawResponse) == 0 {
		t.Errorf("expected raw response captured")
	}
}

func TestHTTPProviderMissingAPIKey(t *testing.T) {
	p := NewHTTPProvider("chainalysis", "", "http://example.test", 5*time.Second, ChainalysisEncoder, ChainalysisDecoder, newTestBreaker())
	_, err := p.Screen(context.Background(), ScreenRequest{TxID: "tx1", Address: "0x1", Chain: "ethereum"})
	if err == nil {
		t.Fatal("expected error for missing api key")
	}
}

func TestHTTPProvider5xxTriggersBreaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	b := NewCircuitBreaker(2, time.Minute)
	p := NewHTTPProvider("trm", "k", srv.URL, 5*time.Second, TRMEncoder, TRMDecoder, b)
	for i := 0; i < 2; i++ {
		_, err := p.Screen(context.Background(), ScreenRequest{TxID: "tx1", Address: "0x1", Chain: "ethereum"})
		if err == nil {
			t.Fatalf("expected error on 502")
		}
	}
	if b.State() != CircuitOpen {
		t.Fatalf("expected breaker open, got %d", b.State())
	}
	// Next call must short-circuit with ErrVendorUnavailable.
	_, err := p.Screen(context.Background(), ScreenRequest{TxID: "tx1", Address: "0x1", Chain: "ethereum"})
	if !errors.Is(err, ErrVendorUnavailable) {
		t.Fatalf("expected ErrVendorUnavailable, got %v", err)
	}
}

func TestHTTPProvider4xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	}))
	defer srv.Close()

	p := NewHTTPProvider("chainalysis", "k", srv.URL, 5*time.Second, ChainalysisEncoder, ChainalysisDecoder, newTestBreaker())
	_, err := p.Screen(context.Background(), ScreenRequest{TxID: "tx1", Address: "0x1", Chain: "ethereum"})
	if err == nil {
		t.Fatal("expected error on 400")
	}
}

func TestTRMEncoderDecoder(t *testing.T) {
	body, err := TRMEncoder(ScreenRequest{TxID: "tx1", Address: "0x1", Chain: "ethereum", Amount: "100"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if string(body) == "" {
		t.Fatal("empty body")
	}
	resp, err := TRMDecoder("trm", ScreenRequest{}, []byte(`{"riskScore":55,"exposure":"high_risk"}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RiskScore != 55 || resp.Exposure != "high_risk" || resp.Vendor != "trm" {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestChainalysisDecoderInvalidJSON(t *testing.T) {
	_, err := ChainalysisDecoder("chainalysis", ScreenRequest{}, []byte(`not-json`))
	if err == nil {
		t.Fatal("expected error for invalid json")
	}
}