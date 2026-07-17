package screen

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/audit"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/decision"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/vendor"
)

func newTestService(t *testing.T, mp vendor.ScreenProvider) (*Service, *MemoryCache, *alert.MemoryStore, *audit.MemorySink) {
	t.Helper()
	cache := NewMemoryCache(time.Hour, 24*time.Hour)
	th := decision.NewThresholds(90, 50, decision.DecisionManualReview)
	screenStore := NewMemoryScreenStore()
	alertStore := alert.NewMemoryStore()
	alerts := alert.NewService(alertStore)
	auditSink := audit.NewMemorySink()
	emitter := audit.NewEmitter(auditSink, 16)
	t.Cleanup(emitter.Close)
	s := NewService(cache, mp, th, screenStore, alerts, emitter)
	return s, cache, alertStore, auditSink
}

// decision.Thresholds.perChain is unexported; tests use NewThresholds.

func TestScreenCleanAllows(t *testing.T) {
	mp := vendor.NewMockProvider("chainalysis")
	s, _, alertStore, auditSink := newTestService(t, mp)
	resp, err := s.Screen(context.Background(), Request{TxID: "tx1", Address: "0x1", Chain: "ethereum", Amount: "100"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.Decision != decision.DecisionAllow {
		t.Fatalf("decision: %s", resp.Decision)
	}
	if resp.Exposure != decision.ExposureClean {
		t.Fatalf("exposure: %s", resp.Exposure)
	}
	if resp.ScreenID == "" {
		t.Error("missing screen id")
	}
	// No alert for allow.
	open, _ := alert.NewService(alertStore).List(alert.StatusOpen)
	if len(open) != 0 {
		t.Fatalf("expected no alert, got %d", len(open))
	}
	// Audit event still emitted.
	waitFor(auditSink, 1)
	if len(auditSink.Events()) != 1 {
		t.Fatalf("audit events: %d", len(auditSink.Events()))
	}
}

func TestScreenSanctionedBlocksAndAlerts(t *testing.T) {
	mp := vendor.NewMockProvider("chainalysis")
	mp.SetResponse("0xbad", "ethereum", vendor.MockResponse{RiskScore: 99, Exposure: "SANCTIONED"})
	s, _, alertStore, auditSink := newTestService(t, mp)
	resp, err := s.Screen(context.Background(), Request{TxID: "tx1", Address: "0xbad", Chain: "ethereum", Amount: "100"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.Decision != decision.DecisionBlock {
		t.Fatalf("decision: %s", resp.Decision)
	}
	open, _ := alert.NewService(alertStore).List(alert.StatusOpen)
	if len(open) != 1 || open[0].Severity != "critical" {
		t.Fatalf("alerts: %+v", open)
	}
	waitFor(auditSink, 1)
	if evs := auditSink.Events(); len(evs) != 1 || evs[0].Decision != decision.DecisionBlock {
		t.Fatalf("audit: %+v", evs)
	}
}

func TestScreenHighRiskManualReview(t *testing.T) {
	mp := vendor.NewMockProvider("chainalysis")
	mp.SetResponse("0xmid", "ethereum", vendor.MockResponse{RiskScore: 60, Exposure: "HIGH_RISK"})
	s, _, alertStore, _ := newTestService(t, mp)
	resp, err := s.Screen(context.Background(), Request{TxID: "tx1", Address: "0xmid", Chain: "ethereum", Amount: "100"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.Decision != decision.DecisionManualReview {
		t.Fatalf("decision: %s", resp.Decision)
	}
	open, _ := alert.NewService(alertStore).List(alert.StatusOpen)
	if len(open) != 1 || open[0].Severity != "high" {
		t.Fatalf("alerts: %+v", open)
	}
}

func TestScreenCacheHitSkipsVendor(t *testing.T) {
	mp := vendor.NewMockProvider("chainalysis")
	s, cache, _, _ := newTestService(t, mp)
	// Pre-seed cache with a clean verdict.
	_ = cache.Set(context.Background(), Verdict{
		Address: "0x1", Chain: "ethereum", RiskScore: 5, Exposure: "CLEAN", Decision: "ALLOW", Vendor: "chainalysis",
	})
	resp, err := s.Screen(context.Background(), Request{TxID: "tx1", Address: "0x1", Chain: "ethereum", Amount: "100"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if !resp.CacheHit {
		t.Error("expected cache hit")
	}
	if mp.Calls() != 0 {
		t.Fatalf("vendor was called: %d", mp.Calls())
	}
}

func TestScreenVendorUnreachableReturnsManualReview(t *testing.T) {
	mp := vendor.NewMockProvider("chainalysis")
	mp.FailNextN(1)
	s, _, alertStore, _ := newTestService(t, mp)
	resp, err := s.Screen(context.Background(), Request{TxID: "tx1", Address: "0x1", Chain: "ethereum", Amount: "100"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.Decision == decision.DecisionAllow {
		t.Fatal("vendor-unreachable must never return allow")
	}
	if resp.Exposure != decision.ExposureUnknown {
		t.Fatalf("exposure: %s", resp.Exposure)
	}
	open, _ := alert.NewService(alertStore).List(alert.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("expected alert for vendor-unreachable, got %d", len(open))
	}
}

func TestScreenValidatesRequest(t *testing.T) {
	mp := vendor.NewMockProvider("chainalysis")
	s, _, _, _ := newTestService(t, mp)
	cases := []Request{
		{Address: "0x1", Chain: "ethereum", Amount: "100"},               // missing tx_id
		{TxID: "tx", Chain: "ethereum", Amount: "100"},                   // missing address
		{TxID: "tx", Address: "0x1", Amount: "100"},                      // missing chain
		{TxID: "tx", Address: "0x1", Chain: "ethereum"},                  // missing amount
	}
	for i, req := range cases {
		if _, err := s.Screen(context.Background(), req); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}

func TestScreenExposureFromScoreWhenVendorOmitsExposure(t *testing.T) {
	mp := vendor.NewMockProvider("chainalysis")
	mp.SetResponse("0x1", "ethereum", vendor.MockResponse{RiskScore: 95, Exposure: ""})
	s, _, alertStore, _ := newTestService(t, mp)
	resp, err := s.Screen(context.Background(), Request{TxID: "tx", Address: "0x1", Chain: "ethereum", Amount: "100"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.Exposure != decision.ExposureSanctioned || resp.Decision != decision.DecisionBlock {
		t.Fatalf("resp: %+v", resp)
	}
	open, _ := alert.NewService(alertStore).List(alert.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("alerts: %d", len(open))
	}
}

func TestMemoryCacheTTLAndDelete(t *testing.T) {
	c := NewMemoryCache(time.Hour, 24*time.Hour)
	_ = c.Set(context.Background(), Verdict{Address: "0x1", Chain: "ethereum", Exposure: "CLEAN", Decision: "ALLOW"})
	if _, ok, _ := c.Get(context.Background(), "0x1", "ethereum"); !ok {
		t.Fatal("expected hit")
	}
	_ = c.Delete(context.Background(), "0x1", "ethereum")
	if _, ok, _ := c.Get(context.Background(), "0x1", "ethereum"); ok {
		t.Fatal("expected miss after delete")
	}
}

func TestMemoryCacheLen(t *testing.T) {
	c := NewMemoryCache(time.Hour, 24*time.Hour)
	if c.Len() != 0 {
		t.Fatalf("initial len: %d", c.Len())
	}
	_ = c.Set(context.Background(), Verdict{Address: "0x1", Chain: "ethereum", Exposure: "CLEAN", Decision: "ALLOW"})
	_ = c.Set(context.Background(), Verdict{Address: "0x2", Chain: "ethereum", Exposure: "CLEAN", Decision: "ALLOW"})
	if c.Len() != 2 {
		t.Fatalf("len after set: %d", c.Len())
	}
	_ = c.Delete(context.Background(), "0x1", "ethereum")
	if c.Len() != 1 {
		t.Fatalf("len after delete: %d", c.Len())
	}
}

func TestMemoryCacheExpired(t *testing.T) {
	now := time.Now()
	c := NewMemoryCache(time.Hour, 24*time.Hour).WithNow(func() time.Time { return now })
	_ = c.Set(context.Background(), Verdict{Address: "0x1", Chain: "ethereum", Exposure: "CLEAN", Decision: "ALLOW"})
	// Advance past TTL.
	c.WithNow(func() time.Time { return now.Add(2 * time.Hour) })
	if _, ok, _ := c.Get(context.Background(), "0x1", "ethereum"); ok {
		t.Fatal("expected miss after expiry")
	}
}

func TestMemoryCacheSanctionedTTL(t *testing.T) {
	now := time.Now()
	c := NewMemoryCache(time.Hour, 24*time.Hour).WithNow(func() time.Time { return now })
	_ = c.Set(context.Background(), Verdict{Address: "0x1", Chain: "ethereum", Exposure: "SANCTIONED", Decision: "BLOCK"})
	// 2h later: default TTL would have expired, sanctioned TTL still valid.
	c.WithNow(func() time.Time { return now.Add(2 * time.Hour) })
	if _, ok, _ := c.Get(context.Background(), "0x1", "ethereum"); !ok {
		t.Fatal("sanctioned entry should still be present after default TTL")
	}
	// 25h later: sanctioned TTL expired.
	c.WithNow(func() time.Time { return now.Add(25 * time.Hour) })
	if _, ok, _ := c.Get(context.Background(), "0x1", "ethereum"); ok {
		t.Fatal("sanctioned entry should be expired after sanctioned TTL")
	}
}

func TestMemoryScreenStoreEmptyID(t *testing.T) {
	s := NewMemoryScreenStore()
	if err := s.Put(ScreenRecord{}); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestMemoryScreenStoreGetAndLen(t *testing.T) {
	s := NewMemoryScreenStore()
	if s.Len() != 0 {
		t.Errorf("initial len: %d", s.Len())
	}
	rec := ScreenRecord{ScreenID: "s1", TxID: "tx1"}
	_ = s.Put(rec)
	if s.Len() != 1 {
		t.Errorf("len after put: %d", s.Len())
	}
	got, ok, err := s.Get("s1")
	if !ok || err != nil || got.TxID != "tx1" {
		t.Fatalf("get: ok=%v err=%v got=%+v", ok, err, got)
	}
	if _, ok, err := s.Get("nope"); ok || err != nil {
		t.Fatalf("miss: ok=%v err=%v", ok, err)
	}
}

func TestServiceWithNowWithID(t *testing.T) {
	mp := vendor.NewMockProvider("chainalysis")
	s, _, _, _ := newTestService(t, mp)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	s.WithNow(func() time.Time { return now })
	s.WithID(func() string { return "fixed-id" })
	resp, err := s.Screen(context.Background(), Request{TxID: "tx", Address: "0x1", Chain: "ethereum", Amount: "1"})
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	if resp.ScreenID != "fixed-id" {
		t.Errorf("screen id: %s", resp.ScreenID)
	}
}

func TestScreenPersistErrorPropagates(t *testing.T) {
	mp := vendor.NewMockProvider("chainalysis")
	s, _, _, _ := newTestService(t, mp)
	s.screens = errScreenStore{}
	_, err := s.Screen(context.Background(), Request{TxID: "tx", Address: "0x1", Chain: "ethereum", Amount: "100"})
	if err == nil || !errors.Is(err, errScreenStoreErr) {
		t.Fatalf("err: %v", err)
	}
}

type errScreenStore struct{}

var errScreenStoreErr = errors.New("store down")

func (errScreenStore) Put(rec ScreenRecord) error                            { return errScreenStoreErr }
func (errScreenStore) Get(id string) (ScreenRecord, bool, error)              { return ScreenRecord{}, false, nil }
func (errScreenStore) ListByAddress(address, chain string) ([]ScreenRecord, error) {
	return nil, nil
}

// waitFor polls the audit sink until n events are present or times out.
func waitFor(sink *audit.MemorySink, n int) {
	deadline := time.After(2 * time.Second)
	for {
		if len(sink.Events()) >= n {
			return
		}
		select {
		case <-deadline:
			return
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}