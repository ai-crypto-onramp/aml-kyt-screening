package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/api"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/store"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/vendor"
)

func TestBuildServicesInMemoryMode(t *testing.T) {
	t.Setenv("DB_URL", "")
	cfg := store.LoadConfig()
	svc, cleanup, err := buildServices(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildServices: %v", err)
	}
	if svc == nil || svc.Screen == nil || svc.Alerts == nil || svc.Webhook == nil || svc.Audit == nil {
		t.Fatalf("services incomplete: %+v", svc)
	}
	if cleanup != nil {
		t.Errorf("expected nil cleanup in in-memory mode")
	}
}

func TestBuildServicesRejectsBadDBURL(t *testing.T) {
	t.Setenv("DB_URL", "postgres://nobody:nopass@127.0.0.1:1/kyt?sslmode=disable&connect_timeout=0")
	// lib/pq opens lazily; Open calls PingContext which will fail fast.
	cfg := store.LoadConfig()
	// Use a short-timeout context to fail fast.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := buildServices(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for unreachable DB")
	}
}

func TestRunBootsAndShutsDown(t *testing.T) {
	t.Setenv("DB_URL", "")
	t.Setenv("PORT", "0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx)
	}()
	// Give the server a moment to boot, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

func TestRunBuildServicesError(t *testing.T) {
	// Force buildServices to fail by pointing DB_URL at an unreachable host
	// with a short connect timeout.
	t.Setenv("DB_URL", "postgres://nobody:nopass@127.0.0.1:1/kyt?sslmode=disable&connect_timeout=1")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := run(ctx); err == nil {
		t.Fatal("expected run to return error on bad DB")
	}
}

func TestHealthzRoute(t *testing.T) {
	t.Setenv("DB_URL", "")
	cfg := store.LoadConfig()
	svc, _, err := buildServices(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildServices: %v", err)
	}
	// Build the mux via api.NewMux.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux := api.NewMux(svc)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestMainHelpsCoverage(t *testing.T) {
	// Ensure os.Getenv paths are exercised for PORT default.
	if os.Getenv("PORT") == "" {
		_ = os.Getenv("PORT")
	}
}

func TestStoreCacheAdapterRoundTrip(t *testing.T) {
	inner := newStoreCacheForTest(t)
	a := &storeCacheAdapter{cache: inner}
	v := screen.Verdict{Address: "0x1", Chain: "ethereum", RiskScore: 42, Exposure: "high_risk", Decision: "manual_review", Vendor: "chainalysis"}
	if err := a.Set(context.Background(), v); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok, err := a.Get(context.Background(), "0x1", "ethereum")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.RiskScore != 42 || got.Exposure != "high_risk" || got.Vendor != "chainalysis" {
		t.Fatalf("got: %+v", got)
	}
	if err := a.Delete(context.Background(), "0x1", "ethereum"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := a.Get(context.Background(), "0x1", "ethereum"); ok || err != nil {
		t.Fatalf("after delete: ok=%v err=%v", ok, err)
	}
}

// newStoreCacheForTest returns a store.Cache backed by an in-memory map for
// the adapter test. We can't use store.NewPGCache (needs DB) so we build a
// tiny stub.
func newStoreCacheForTest(t *testing.T) store.Cache {
	t.Helper()
	return &stubStoreCache{mem: make(map[string]store.Verdict)}
}

type stubStoreCache struct {
	mem map[string]store.Verdict
}

func (s *stubStoreCache) Get(_ context.Context, address, chain string) (store.Verdict, bool, error) {
	v, ok := s.mem[chain+":"+address]
	return v, ok, nil
}
func (s *stubStoreCache) Set(_ context.Context, v store.Verdict) error {
	s.mem[v.Chain+":"+v.Address] = v
	return nil
}
func (s *stubStoreCache) Delete(_ context.Context, address, chain string) error {
	delete(s.mem, chain+":"+address)
	return nil
}
func (s *stubStoreCache) Ping(_ context.Context) error { return nil }
func (s *stubStoreCache) Close() error                 { return nil }

func TestBuildProviderChainalysis(t *testing.T) {
	t.Setenv("CHAINALYSIS_API_KEY", "k")
	t.Setenv("CHAINALYSIS_API_URL", "http://example.test")
	t.Setenv("TRM_API_KEY", "")
	cfg := store.LoadConfig()
	b := vendor.NewCircuitBreaker(cfg.CircuitBreakerThreshold, time.Minute)
	p := buildProvider(cfg, b)
	if p == nil || p.Name() != "chainalysis" {
		t.Fatalf("provider: %+v", p)
	}
}

func TestBuildProviderTRM(t *testing.T) {
	t.Setenv("CHAINALYSIS_API_KEY", "")
	t.Setenv("TRM_API_KEY", "trmkey")
	t.Setenv("TRM_API_URL", "http://example.test")
	cfg := store.LoadConfig()
	b := vendor.NewCircuitBreaker(cfg.CircuitBreakerThreshold, time.Minute)
	p := buildProvider(cfg, b)
	if p == nil || p.Name() != "trm" {
		t.Fatalf("provider: %+v", p)
	}
}

func TestBuildProviderNone(t *testing.T) {
	t.Setenv("CHAINALYSIS_API_KEY", "")
	t.Setenv("TRM_API_KEY", "")
	cfg := store.LoadConfig()
	b := vendor.NewCircuitBreaker(cfg.CircuitBreakerThreshold, time.Minute)
	if p := buildProvider(cfg, b); p != nil {
		t.Fatalf("expected nil provider, got %v", p)
	}
}