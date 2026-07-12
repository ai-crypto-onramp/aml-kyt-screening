// Package metrics exposes Prometheus-compatible metrics for the screen path,
// vendor calls, cache, webhooks, and audit emission.
package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// Metrics is the metrics container. Counters are atomic int64s; the gauges
// track the latest observed value.
type Metrics struct {
	ScreenTotal          atomic.Int64
	ScreenDurationSum    atomic.Int64 // nanoseconds
	ScreenDurationCount  atomic.Int64
	CacheHitsTotal       atomic.Int64
	CacheMissesTotal     atomic.Int64
	VendorErrorsTotal    atomic.Int64
	CircuitBreakerState  atomic.Int64 // 0=closed,1=open,2=half-open
	AlertsOpen           atomic.Int64
	AuditDropsTotal      atomic.Int64
	WebhookAcceptTotal   atomic.Int64
	WebhookRejectTotal   atomic.Int64
	WebhookDuplicateTotal atomic.Int64
}

// Global singleton metrics instance.
var (
	globalOnce sync.Once
	global     *Metrics
)

// Global returns the process-wide Metrics instance.
func Global() *Metrics {
	globalOnce.Do(func() { global = &Metrics{} })
	return global
}

// ObserveScreenDuration records a screen latency in nanoseconds.
func (m *Metrics) ObserveScreenDuration(nanos int64) {
	m.ScreenDurationSum.Add(nanos)
	m.ScreenDurationCount.Add(1)
}

// Handler serves Prometheus-formatted metrics on /metrics.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(render(Global())))
	}
}

func render(m *Metrics) string {
	var b strings.Builder
	writeCounter(&b, "kyt_screen_total", m.ScreenTotal.Load())
	writeHistogram(&b, "kyt_screen_duration_seconds", m.ScreenDurationSum.Load(), m.ScreenDurationCount.Load())
	writeCounter(&b, "kyt_cache_hits_total", m.CacheHitsTotal.Load())
	writeCounter(&b, "kyt_cache_misses_total", m.CacheMissesTotal.Load())
	writeCounter(&b, "kyt_vendor_errors_total", m.VendorErrorsTotal.Load())
	writeGauge(&b, "kyt_circuit_breaker_state", m.CircuitBreakerState.Load())
	writeGauge(&b, "kyt_alerts_open", m.AlertsOpen.Load())
	writeCounter(&b, "kyt_audit_drops_total", m.AuditDropsTotal.Load())
	writeCounter(&b, "kyt_webhook_accept_total", m.WebhookAcceptTotal.Load())
	writeCounter(&b, "kyt_webhook_reject_total", m.WebhookRejectTotal.Load())
	writeCounter(&b, "kyt_webhook_duplicate_total", m.WebhookDuplicateTotal.Load())
	return b.String()
}

func writeCounter(b *strings.Builder, name string, v int64) {
	fmt.Fprintf(b, "# TYPE %s counter\n%s %d\n", name, name, v)
}

func writeGauge(b *strings.Builder, name string, v int64) {
	fmt.Fprintf(b, "# TYPE %s gauge\n%s %d\n", name, name, v)
}

func writeHistogram(b *strings.Builder, name string, sum, count int64) {
	fmt.Fprintf(b, "# TYPE %s histogram\n%s_sum %d\n%s_count %d\n", name, name, sum, name, count)
}