package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderIncludesAllMetricNames(t *testing.T) {
	m := Global()
	m.ScreenTotal.Add(3)
	m.CacheHitsTotal.Add(2)
	m.CacheMissesTotal.Add(1)
	m.VendorErrorsTotal.Add(1)
	m.CircuitBreakerState.Store(1)
	m.AlertsOpen.Store(5)
	m.AuditDropsTotal.Add(7)
	m.ObserveScreenDuration(1_000_000)
	m.WebhookAcceptTotal.Add(4)
	m.WebhookRejectTotal.Add(1)
	m.WebhookDuplicateTotal.Add(2)

	out := render(m)
	wantSubstrings := []string{
		"kyt_screen_total",
		"kyt_screen_duration_seconds_sum",
		"kyt_screen_duration_seconds_count",
		"kyt_cache_hits_total",
		"kyt_cache_misses_total",
		"kyt_vendor_errors_total",
		"kyt_circuit_breaker_state",
		"kyt_alerts_open",
		"kyt_audit_drops_total",
		"kyt_webhook_accept_total",
		"kyt_webhook_reject_total",
		"kyt_webhook_duplicate_total",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(out, s) {
			t.Errorf("render output missing %q\noutput:\n%s", s, out)
		}
	}
}

func TestHandlerServesMetrics(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	Handler()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type: %s", ct)
	}
	if !strings.Contains(rec.Body.String(), "kyt_screen_total") {
		t.Errorf("body missing metrics: %s", rec.Body.String())
	}
}