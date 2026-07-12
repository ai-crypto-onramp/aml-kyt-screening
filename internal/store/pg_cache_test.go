package store

import (
	"context"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// skipIfNoDB skips tests requiring a live Postgres. The acceptance criterion
// "go test ./internal/store/... passes against a ephemeral Postgres/Redis" is
// exercised in CI where DB_URL is set; locally these tests skip.
func skipIfNoDB(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		t.Skip("DB_URL not set; skipping live Postgres test")
	}
	return dsn
}

func TestMigrateAppliesAllMigrations(t *testing.T) {
	dsn := skipIfNoDB(t)
	db, err := Open(context.Background(), Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"address_risk_cache", "kyt_screens", "kyt_alerts", "vendor_responses", "schema_migrations"} {
		var exists bool
		row := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, table)
		if err := row.Scan(&exists); err != nil || !exists {
			t.Errorf("table %q missing after migrate (err=%v, exists=%v)", table, err, exists)
		}
	}

	// Idempotent: running again must not error.
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
}

func TestPGCacheSetGetDeleteSanctionedTTL(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Truncate to avoid conflicts with prior runs.
	if _, err := db.ExecContext(ctx, `DELETE FROM address_risk_cache WHERE address LIKE 'test-%'`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	def := time.Hour
	san := 24 * time.Hour
	c := NewPGCache(db, def, san, WithNow(func() time.Time { return now }))

	// Sanctioned -> sanctioned TTL (24h).
	v := Verdict{
		Address:   "test-sanctioned",
		Chain:     "ethereum",
		RiskScore: 99,
		Exposure:  "sanctioned",
		Decision:  "block",
		Vendor:    "chainalysis",
	}
	if err := c.Set(ctx, v); err != nil {
		t.Fatalf("set sanctioned: %v", err)
	}
	got, ok, err := c.Get(ctx, v.Address, v.Chain)
	if err != nil {
		t.Fatalf("get sanctioned: %v", err)
	}
	if !ok {
		t.Fatalf("sanctioned entry missing after set")
	}
	if got.Exposure != "sanctioned" || got.Decision != "block" {
		t.Fatalf("got = %+v", got)
	}
	wantExpiry := now.Add(san)
	if got.ExpiresAt.Sub(wantExpiry).Abs() > time.Second {
		t.Errorf("sanctioned expiry = %s, want ~%s", got.ExpiresAt, wantExpiry)
	}
	if got.TTLSeconds != int(san.Seconds()) {
		t.Errorf("sanctioned ttl_seconds = %d, want %d", got.TTLSeconds, int(san.Seconds()))
	}

	// Clean -> default TTL (1h).
	now = now.Add(time.Minute)
	cClean := NewPGCache(db, def, san, WithNow(func() time.Time { return now }))
	vClean := Verdict{
		Address:   "test-clean",
		Chain:     "ethereum",
		RiskScore: 5,
		Exposure:  "clean",
		Decision:  "allow",
		Vendor:    "chainalysis",
	}
	if err := cClean.Set(ctx, vClean); err != nil {
		t.Fatalf("set clean: %v", err)
	}
	gotClean, ok, err := cClean.Get(ctx, vClean.Address, vClean.Chain)
	if err != nil {
		t.Fatalf("get clean: %v", err)
	}
	if !ok {
		t.Fatalf("clean entry missing after set")
	}
	if gotClean.TTLSeconds != int(def.Seconds()) {
		t.Errorf("clean ttl_seconds = %d, want %d", gotClean.TTLSeconds, int(def.Seconds()))
	}

	// Delete removes the entry.
	if err := c.Delete(ctx, v.Address, v.Chain); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := c.Get(ctx, v.Address, v.Chain); err != nil || ok {
		t.Fatalf("expected miss after delete (ok=%v, err=%v)", ok, err)
	}
}

func TestPGCacheExpiredEntriesMiss(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `DELETE FROM address_risk_cache WHERE address = 'test-expired'`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	def := time.Hour
	san := 24 * time.Hour
	setter := NewPGCache(db, def, san, WithNow(func() time.Time { return now }))

	v := Verdict{
		Address:   "test-expired",
		Chain:     "ethereum",
		RiskScore: 10,
		Exposure:  "clean",
		Decision:  "allow",
		Vendor:    "chainalysis",
	}
	if err := setter.Set(ctx, v); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Advance past TTL; Get must miss.
	after := now.Add(2 * def)
	reader := NewPGCache(db, def, san, WithNow(func() time.Time { return after }))
	if _, ok, err := reader.Get(ctx, v.Address, v.Chain); err != nil {
		t.Fatalf("get expired: %v", err)
	} else if ok {
		t.Fatalf("expected miss for expired entry")
	}
}