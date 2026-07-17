package vendor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIdempotencyKey(t *testing.T) {
	got := IdempotencyKey("tx1", "0xabc", "ethereum")
	want := "tx1:0xabc:ethereum"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMockProviderDefault(t *testing.T) {
	m := NewMockProvider("chainalysis")
	resp, err := m.Screen(context.Background(), ScreenRequest{TxID: "tx", Address: "0x1", Chain: "ethereum"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.Exposure != "CLEAN" || resp.RiskScore != 0 {
		t.Fatalf("default response: %+v", resp)
	}
	if m.Calls() != 1 {
		t.Errorf("calls: %d", m.Calls())
	}
}

func TestMockProviderPresetResponse(t *testing.T) {
	m := NewMockProvider("trm")
	m.SetResponse("0xbad", "ethereum", MockResponse{RiskScore: 99, Exposure: "SANCTIONED"})
	resp, err := m.Screen(context.Background(), ScreenRequest{TxID: "tx", Address: "0xbad", Chain: "ethereum"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.Exposure != "SANCTIONED" || resp.RiskScore != 99 {
		t.Fatalf("preset response: %+v", resp)
	}
}

func TestMockProviderFailNextN(t *testing.T) {
	m := NewMockProvider("chainalysis")
	m.FailNextN(2)
	_, err := m.Screen(context.Background(), ScreenRequest{TxID: "tx", Address: "0x1", Chain: "ethereum"})
	if !errors.Is(err, ErrVendorUnavailable) {
		t.Fatalf("err: %v", err)
	}
	_, err = m.Screen(context.Background(), ScreenRequest{TxID: "tx", Address: "0x1", Chain: "ethereum"})
	if !errors.Is(err, ErrVendorUnavailable) {
		t.Fatalf("err: %v", err)
	}
	resp, err := m.Screen(context.Background(), ScreenRequest{TxID: "tx", Address: "0x1", Chain: "ethereum"})
	if err != nil || resp.Exposure != "CLEAN" {
		t.Fatalf("third call: %v %+v", err, resp)
	}
}

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	b := NewCircuitBreaker(3, 50*time.Millisecond)
	for i := 0; i < 3; i++ {
		if err := b.Execute(context.Background(), func(ctx context.Context) error { return errors.New("boom") }); err == nil {
			t.Fatalf("expected error")
		}
	}
	if b.State() != CircuitOpen {
		t.Fatalf("expected open, got %d", b.State())
	}
	// While open: Execute must short-circuit.
	if err := b.Execute(context.Background(), func(ctx context.Context) error { t.Fatal("must not call"); return nil }); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	// After OpenFor elapses, half-open allows a single probe call.
	time.Sleep(60 * time.Millisecond)
	if err := b.Execute(context.Background(), func(ctx context.Context) error { return nil }); err != nil {
		t.Fatalf("probe call: %v", err)
	}
	if b.State() != CircuitClosed {
		t.Fatalf("expected closed after success, got %d", b.State())
	}
}

func TestCircuitBreakerResetsOnSuccess(t *testing.T) {
	b := NewCircuitBreaker(3, time.Minute)
	for i := 0; i < 2; i++ {
		_ = b.Execute(context.Background(), func(ctx context.Context) error { return errors.New("boom") })
	}
	if b.State() != CircuitClosed {
		t.Fatalf("expected still closed after 2 failures, got %d", b.State())
	}
	// A success resets the counter.
	if err := b.Execute(context.Background(), func(ctx context.Context) error { return nil }); err != nil {
		t.Fatalf("success: %v", err)
	}
	for i := 0; i < 2; i++ {
		_ = b.Execute(context.Background(), func(ctx context.Context) error { return errors.New("boom") })
	}
	if b.State() != CircuitClosed {
		t.Fatalf("expected still closed after reset+2 failures, got %d", b.State())
	}
}

func TestIdempotentProviderCachesResponse(t *testing.T) {
	m := NewMockProvider("chainalysis")
	m.SetResponse("0xbad", "ethereum", MockResponse{RiskScore: 75, Exposure: "HIGH_RISK"})
	store := NewMemoryResponseStore()
	p := NewIdempotentProvider(m, store)

	resp, err := p.Screen(context.Background(), ScreenRequest{TxID: "tx1", Address: "0xbad", Chain: "ethereum"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.RiskScore != 75 || resp.Exposure != "HIGH_RISK" {
		t.Fatalf("resp: %+v", resp)
	}
	if store.Len() != 1 {
		t.Fatalf("expected 1 stored record, got %d", store.Len())
	}
	if m.Calls() != 1 {
		t.Fatalf("expected 1 inner call, got %d", m.Calls())
	}

	// Second call with the same idempotency key: must hit the cache.
	resp2, err := p.Screen(context.Background(), ScreenRequest{TxID: "tx1", Address: "0xbad", Chain: "ethereum"})
	if err != nil {
		t.Fatalf("screen2: %v", err)
	}
	if resp2.RiskScore != 75 || resp2.Exposure != "HIGH_RISK" {
		t.Fatalf("resp2: %+v", resp2)
	}
	if m.Calls() != 1 {
		t.Fatalf("expected inner still 1 call (cache hit), got %d", m.Calls())
	}
}

func TestIdempotentProviderPropagatesErrors(t *testing.T) {
	m := NewMockProvider("trm")
	m.FailNextN(1)
	store := NewMemoryResponseStore()
	p := NewIdempotentProvider(m, store)
	_, err := p.Screen(context.Background(), ScreenRequest{TxID: "tx", Address: "0x1", Chain: "ethereum"})
	if !errors.Is(err, ErrVendorUnavailable) {
		t.Fatalf("err: %v", err)
	}
	if store.Len() != 0 {
		t.Fatalf("expected no stored record on failure, got %d", store.Len())
	}
}

func TestMemoryResponseStoreEmptyKeyRejected(t *testing.T) {
	s := NewMemoryResponseStore()
	if err := s.Put(context.Background(), VendorResponseRecord{}); err == nil {
		t.Fatal("expected error for empty idempotency key")
	}
}

func TestDefaultConfigFromEnv(t *testing.T) {
	t.Setenv("VENDOR_TIMEOUT_MS", "1500")
	t.Setenv("VENDOR_CIRCUIT_BREAKER_THRESHOLD", "10")
	t.Setenv("CHAINALYSIS_API_KEY", "ck")
	t.Setenv("CHAINALYSIS_API_URL", "http://chainalysis.test")
	t.Setenv("TRM_API_KEY", "tk")
	t.Setenv("TRM_API_URL", "http://trm.test")
	cfg := DefaultConfig()
	if cfg.VendorTimeout != 1500*time.Millisecond {
		t.Errorf("VendorTimeout: %v", cfg.VendorTimeout)
	}
	if cfg.CircuitBreakerThreshold != 10 {
		t.Errorf("CircuitBreakerThreshold: %d", cfg.CircuitBreakerThreshold)
	}
	if cfg.ChainalysisAPIKey != "ck" || cfg.ChainalysisAPIURL != "http://chainalysis.test" {
		t.Errorf("chainalysis: %+v", cfg)
	}
	if cfg.TRMAPIKey != "tk" || cfg.TRMAPIURL != "http://trm.test" {
		t.Errorf("trm: %+v", cfg)
	}
}

func TestDefaultConfigDefaults(t *testing.T) {
	t.Setenv("VENDOR_TIMEOUT_MS", "")
	t.Setenv("VENDOR_CIRCUIT_BREAKER_THRESHOLD", "")
	cfg := DefaultConfig()
	if cfg.VendorTimeout != 800*time.Millisecond {
		t.Errorf("default VendorTimeout: %v", cfg.VendorTimeout)
	}
	if cfg.CircuitBreakerThreshold != 5 {
		t.Errorf("default threshold: %d", cfg.CircuitBreakerThreshold)
	}
}

func TestProviderNames(t *testing.T) {
	if NewMockProvider("chainalysis").Name() != "chainalysis" {
		t.Error("mock name")
	}
	b := NewCircuitBreaker(2, time.Minute)
	p := NewHTTPProvider("trm", "k", "http://x", time.Second, TRMEncoder, TRMDecoder, b)
	if p.Name() != "trm" {
		t.Error("http name")
	}
}

func TestIdempotentProviderName(t *testing.T) {
	p := NewIdempotentProvider(NewMockProvider("chainalysis"), NewMemoryResponseStore())
	if p.Name() != "chainalysis" {
		t.Error("idempotent name")
	}
}

func TestMockProviderString(t *testing.T) {
	m := NewMockProvider("chainalysis")
	_, _ = m.Screen(context.Background(), ScreenRequest{Address: "0x1", Chain: "ethereum"})
	if s := m.String(); s == "" {
		t.Error("empty string repr")
	}
}

func TestTRMDecoderInvalidJSON(t *testing.T) {
	if _, err := TRMDecoder("trm", ScreenRequest{}, []byte(`nope`)); err == nil {
		t.Fatal("expected error")
	}
}