package store

import (
	"context"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// BenchmarkPGCacheHitGet measures the cache-hit read latency against a live
// Postgres. The acceptance criterion is p99 < 20ms for cache hits.
func BenchmarkPGCacheHitGet(b *testing.B) {
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		b.Skip("DB_URL not set; skipping live benchmark")
	}
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `DELETE FROM address_risk_cache WHERE address = 'bench-hit'`); err != nil {
		b.Fatalf("truncate: %v", err)
	}

	c := NewPGCache(db, time.Hour, 24*time.Hour)
	if err := c.Set(ctx, Verdict{
		Address: "bench-hit", Chain: "ethereum", RiskScore: 7,
		Exposure: "clean", Decision: "allow", Vendor: "chainalysis",
	}); err != nil {
		b.Fatalf("set: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, ok, err := c.Get(ctx, "bench-hit", "ethereum"); err != nil || !ok {
			b.Fatalf("get: ok=%v err=%v", ok, err)
		}
	}
}