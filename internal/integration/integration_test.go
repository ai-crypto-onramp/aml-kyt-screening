// Package integration contains end-to-end integration tests that boot the
// REST and gRPC servers together against ephemeral Postgres/Redis. Tests skip
// when DB_URL is unset (see skipIfNoDB); CI sets DB_URL to exercise them.
package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/api"
	pb "github.com/ai-crypto-onramp/aml-kyt-screening/internal/pb"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/audit"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/decision"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/grpcserver"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/review"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/store"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/vendor"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/webhook"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func skipIfNoDB(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		t.Skip("DB_URL not set; skipping live integration test")
	}
	return dsn
}

type env struct {
	svc         *api.Services
	screenStore screen.ScreenStore
	restSrv     *http.Server
	grpcClient  pb.KYTServiceClient
	conn        *grpc.ClientConn
}

func bootEnv(t *testing.T, db *sql.DB) (env, func()) {
	t.Helper()
	cache := screen.NewMemoryCache(time.Hour, 24*time.Hour)
	mp := vendor.NewMockProvider("chainalysis")
	th := decision.NewThresholds(90, 50, decision.DecisionManualReview)
	screenStore := store.NewPGScreenStore(db)
	alertStore := store.NewPGAlertStore(db)
	alerts := alert.NewService(alertStore)
	auditSink := audit.NewDBSink(db)
	emitter := audit.NewEmitter(auditSink, 16)
	screenSvc := screen.NewService(cache, mp, th, screenStore, alerts, emitter)
	verifier := webhook.NewVerifier(map[string][]byte{"chainalysis": []byte("secret")})
	proc := webhook.NewProcessor(verifier, cache, alerts).WithReviewer(review.NewTrigger(screenStore, alerts))
	svc := &api.Services{Screen: screenSvc, Alerts: alerts, Webhook: proc, Audit: emitter}

	restSrv := api.NewServer(svc, "127.0.0.1:0")
	grpcSrv := grpcserver.NewServer(svc)

	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("grpc listen: %v", err)
	}
	go func() { _ = grpcSrv.GRPCServer.Serve(grpcLn) }()

	conn, err := grpc.NewClient(grpcLn.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	client := pb.NewKYTServiceClient(conn)

	go func() { _ = restSrv.ListenAndServe() }()

	cleanup := func() {
		_ = conn.Close()
		grpcSrv.Stop()
		_ = grpcLn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = restSrv.Shutdown(ctx)
		emitter.Close()
	}
	return env{svc: svc, screenStore: screenStore, restSrv: restSrv, grpcClient: client, conn: conn}, cleanup
}

func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db
}

func TestIntegrationRESTScreenAndAlert(t *testing.T) {
	dsn := skipIfNoDB(t)
	db := openDB(t, dsn)
	defer db.Close()
	e, cleanup := bootEnv(t, db)
	defer cleanup()

	body := `{"tx_id":"int-tx-1","address":"0xint-clean","chain":"ethereum","amount":"100"}`
	resp, err := http.Post(fmt.Sprintf("http://%s/v1/kyt/screen", e.restSrv.Addr), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var sr screen.Response
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sr.Decision != decision.DecisionAllow {
		t.Errorf("decision: %s", sr.Decision)
	}
	if sr.ScreenID == "" {
		t.Error("missing screen id")
	}
}

func TestIntegrationGRPCScreenMirrorsREST(t *testing.T) {
	dsn := skipIfNoDB(t)
	db := openDB(t, dsn)
	defer db.Close()
	e, cleanup := bootEnv(t, db)
	defer cleanup()

	grpcResp, err := e.grpcClient.Screen(context.Background(), &pb.ScreenRequest{
		TxId: "int-tx-2", Address: "0xint-2", Chain: "ethereum", Amount: "100",
	})
	if err != nil {
		t.Fatalf("grpc screen: %v", err)
	}
	restBody := `{"tx_id":"int-tx-3","address":"0xint-3","chain":"ethereum","amount":"100"}`
	resp, err := http.Post(fmt.Sprintf("http://%s/v1/kyt/screen", e.restSrv.Addr), "application/json", strings.NewReader(restBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var restResp screen.Response
	if err := json.NewDecoder(resp.Body).Decode(&restResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if grpcResp.GetDecision() != restResp.Decision {
		t.Errorf("verdict mismatch: grpc=%s rest=%s", grpcResp.GetDecision(), restResp.Decision)
	}
	if grpcResp.GetExposure() != restResp.Exposure {
		t.Errorf("exposure mismatch: grpc=%s rest=%s", grpcResp.GetExposure(), restResp.Exposure)
	}
}

func TestIntegrationGetAlertViaGRPC(t *testing.T) {
	dsn := skipIfNoDB(t)
	db := openDB(t, dsn)
	defer db.Close()
	e, cleanup := bootEnv(t, db)
	defer cleanup()

	a, err := e.svc.Alerts.Create("scr-int-1", "tx-int-1", "0xint-alert", "ethereum", "sanctioned", "critical")
	if err != nil {
		t.Fatalf("create alert: %v", err)
	}
	resp, err := e.grpcClient.GetAlert(context.Background(), &pb.GetAlertRequest{Id: a.ID})
	if err != nil {
		t.Fatalf("grpc get alert: %v", err)
	}
	if resp.GetId() != a.ID || resp.GetExposure() != "sanctioned" {
		t.Errorf("alert: %+v", resp)
	}
}

func TestIntegrationRESTScreenValidation(t *testing.T) {
	dsn := skipIfNoDB(t)
	db := openDB(t, dsn)
	defer db.Close()
	e, cleanup := bootEnv(t, db)
	defer cleanup()

	body := `{"tx_id":"","address":"0x1","chain":"ethereum","amount":"100"}`
	resp, err := http.Post(fmt.Sprintf("http://%s/v1/kyt/screen", e.restSrv.Addr), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("expected error status, got %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(raw, []byte("request_id")) {
		t.Errorf("error envelope missing request_id: %s", raw)
	}
}

func TestIntegrationWebhookCreatesAlertAndTriggersReview(t *testing.T) {
	dsn := skipIfNoDB(t)
	db := openDB(t, dsn)
	defer db.Close()
	e, cleanup := bootEnv(t, db)
	defer cleanup()

	// Seed a past allow screen for the address so the review trigger opens a
	// new alert.
	now := time.Now().UTC()
	if err := e.screenStore.Put(screen.ScreenRecord{
		ScreenID: "11111111-2222-3333-4444-555555555555", TxID: "tx-past", Address: "0xint-recl", Chain: "ethereum",
		Amount: "1", Decision: decision.DecisionAllow, Exposure: "clean", CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed screen: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM kyt_screens WHERE screen_id = '11111111-2222-3333-4444-555555555555'`)
	})

	body := []byte(`{"event_id":"int-e1","address":"0xint-recl","chain":"ethereum","exposure":"sanctioned","tx_id":"tx-past"}`)
	sig := signHMAC([]byte("secret"), body)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/v1/webhooks/chainalysis", e.restSrv.Addr), bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	// The re-classification alert should exist.
	all, _ := e.svc.Alerts.List("")
	var foundRecl bool
	for _, a := range all {
		if a.TxID == "tx-past" && a.Exposure == "sanctioned" {
			foundRecl = true
		}
	}
	if !foundRecl {
		t.Error("expected a re-classification alert for tx-past")
	}

	// The review trigger runs async; poll for the alert referencing the past
	// allow screen.
	deadline := time.After(2 * time.Second)
	for {
		got, _ := e.svc.Alerts.List("")
		if hasReviewAlert(got, "11111111-2222-3333-4444-555555555555") {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("review alert not created; alerts: %+v", got)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func hasReviewAlert(alerts []alert.Alert, screenID string) bool {
	for _, a := range alerts {
		if a.ScreenID == screenID {
			return true
		}
	}
	return false
}

func signHMAC(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}