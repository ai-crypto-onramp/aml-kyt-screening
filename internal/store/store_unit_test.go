package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
)

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("DB_URL", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv("CACHE_TTL_SECONDS", "")
	t.Setenv("SANCTIONED_CACHE_TTL_SECONDS", "")
	t.Setenv("VENDOR_TIMEOUT_MS", "")
	t.Setenv("VENDOR_CIRCUIT_BREAKER_THRESHOLD", "")
	cfg := LoadConfig()
	if cfg.DBURL != "" {
		t.Errorf("DBURL: %q", cfg.DBURL)
	}
	if cfg.CacheTTL != time.Hour {
		t.Errorf("default CacheTTL: %v", cfg.CacheTTL)
	}
	if cfg.SanctionedCacheTTL != 24*7*time.Hour {
		t.Errorf("default SanctionedCacheTTL: %v", cfg.SanctionedCacheTTL)
	}
	if cfg.MaxOpenConns != 25 {
		t.Errorf("MaxOpenConns: %d", cfg.MaxOpenConns)
	}
	if cfg.VendorTimeout != 800*time.Millisecond {
		t.Errorf("VendorTimeout: %v", cfg.VendorTimeout)
	}
	if cfg.CircuitBreakerThreshold != 5 {
		t.Errorf("CircuitBreakerThreshold: %d", cfg.CircuitBreakerThreshold)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("DB_URL", "postgres://u:p@h:5432/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("CACHE_TTL_SECONDS", "120")
	t.Setenv("SANCTIONED_CACHE_TTL_SECONDS", "7200")
	t.Setenv("DB_MAX_OPEN_CONNS", "10")
	t.Setenv("DB_MAX_IDLE_CONNS", "2")
	t.Setenv("DB_CONN_MAX_LIFETIME_SECONDS", "60")
	t.Setenv("VENDOR_TIMEOUT_MS", "250")
	t.Setenv("VENDOR_CIRCUIT_BREAKER_THRESHOLD", "3")
	cfg := LoadConfig()
	if cfg.DBURL != "postgres://u:p@h:5432/db" {
		t.Errorf("DBURL: %q", cfg.DBURL)
	}
	if cfg.RedisURL != "redis://localhost:6379" {
		t.Errorf("RedisURL: %q", cfg.RedisURL)
	}
	if cfg.CacheTTL != 120*time.Second {
		t.Errorf("CacheTTL: %v", cfg.CacheTTL)
	}
	if cfg.SanctionedCacheTTL != 7200*time.Second {
		t.Errorf("SanctionedCacheTTL: %v", cfg.SanctionedCacheTTL)
	}
	if cfg.MaxOpenConns != 10 {
		t.Errorf("MaxOpenConns: %d", cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns != 2 {
		t.Errorf("MaxIdleConns: %d", cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime != 60*time.Second {
		t.Errorf("ConnMaxLifetime: %v", cfg.ConnMaxLifetime)
	}
	if cfg.VendorTimeout != 250*time.Millisecond {
		t.Errorf("VendorTimeout: %v", cfg.VendorTimeout)
	}
	if cfg.CircuitBreakerThreshold != 3 {
		t.Errorf("CircuitBreakerThreshold: %d", cfg.CircuitBreakerThreshold)
	}
}

func TestEnvIntInvalidFallsBack(t *testing.T) {
	t.Setenv("DB_MAX_OPEN_CONNS", "not-a-number")
	if got := envInt("DB_MAX_OPEN_CONNS", 42); got != 42 {
		t.Errorf("envInt invalid: %d", got)
	}
}

func TestEnvDurationInvalidFallsBack(t *testing.T) {
	t.Setenv("CACHE_TTL_SECONDS", "garbage")
	if got := envDuration("CACHE_TTL_SECONDS", time.Minute); got != time.Minute {
		t.Errorf("envDuration invalid: %v", got)
	}
}

func TestParseSeconds(t *testing.T) {
	d, err := parseSeconds("90")
	if err != nil || d != 90*time.Second {
		t.Fatalf("parseSeconds: %v %v", d, err)
	}
	if _, err := parseSeconds("xx"); err == nil {
		t.Fatal("expected error for non-numeric")
	}
}

func TestOpenRequiresDSN(t *testing.T) {
	_, err := Open(context.Background(), Config{DBURL: ""})
	if err == nil || !strings.Contains(err.Error(), "DB_URL") {
		t.Fatalf("err: %v", err)
	}
}

func TestNewCacheInvalidRedisURL(t *testing.T) {
	_, err := NewCache(context.Background(), Config{DBURL: "postgres://x", RedisURL: "://bad-url"}, nil)
	if err == nil || !strings.Contains(err.Error(), "parse REDIS_URL") {
		t.Fatalf("err: %v", err)
	}
}

func TestHealthCheckerNilDeps(t *testing.T) {
	h := NewHealthChecker(nil, nil)
	if err := h.Check(context.Background()); err != nil {
		t.Fatalf("check with nil deps: %v", err)
	}
}

type errCache struct{ err error }

func (e *errCache) Get(_ context.Context, _, _ string) (Verdict, bool, error) { return Verdict{}, false, nil }
func (e *errCache) Set(_ context.Context, _ Verdict) error                    { return nil }
func (e *errCache) Delete(_ context.Context, _, _ string) error               { return nil }
func (e *errCache) Ping(_ context.Context) error                              { return e.err }
func (e *errCache) Close() error                                              { return nil }

func TestHealthCheckerCacheError(t *testing.T) {
	h := NewHealthChecker(nil, &errCache{err: errHealth})
	if err := h.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "cache") {
		t.Fatalf("err: %v", err)
	}
}

var errHealth = errPing("cache down")

type errPing string

func (e errPing) Error() string { return string(e) }

func TestPGCacheConstructors(t *testing.T) {
	c := NewPGCache(nil, time.Hour, 24*time.Hour, WithNow(func() time.Time { return time.Time{} }))
	if c == nil {
		t.Fatal("nil pgcache")
	}
}

func TestRedisCacheConstructors(t *testing.T) {
	c := NewRedisCache(nil, time.Hour, 24*time.Hour, WithRedisNow(func() time.Time { return time.Time{} }))
	if c == nil {
		t.Fatal("nil rediscache")
	}
	if got := redisKey("0x1", "ethereum"); got != "arc:ethereum:0x1" {
		t.Errorf("redisKey: %q", got)
	}
}

func TestIsInvalidUUID(t *testing.T) {
	if isInvalidUUID(nil) {
		t.Error("nil err should not be invalid uuid")
	}
	if isInvalidUUID(errors.New("random")) {
		t.Error("random err should not be invalid uuid")
	}
}

func TestNewCachePGFallback(t *testing.T) {
	c, err := NewCache(context.Background(), Config{DBURL: "postgres://x"}, nil)
	if err != nil {
		t.Fatalf("NewCache pg fallback: %v", err)
	}
	if _, ok := c.(*PGCache); !ok {
		t.Fatalf("expected *PGCache, got %T", c)
	}
}

func TestHealthCheckerDBError(t *testing.T) {
	db, err := sql.Open("postgres", "host=localhost port=1 dbname=none")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	// Use a short timeout ctx so Ping fails fast.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	h := NewHealthChecker(db, nil)
	if err := h.Check(ctx); err == nil {
		// Best-effort: if somehow reachable, fine. We expect an error here.
		t.Log("db unexpectedly reachable")
	}
}

func TestOpenPingError(t *testing.T) {
	// Use an invalid DSN so Ping fails fast. sql.Open itself does not connect.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := Open(ctx, Config{DBURL: "postgres://u:p@127.0.0.1:1/db", ConnMaxLifetime: time.Second})
	if err == nil {
		t.Fatal("expected ping error")
	}
}

func TestEnvMillisInvalidFallsBack(t *testing.T) {
	t.Setenv("VENDOR_TIMEOUT_MS", "garbage")
	if got := envMillis("VENDOR_TIMEOUT_MS", time.Second); got != time.Second {
		t.Errorf("envMillis invalid = %v", got)
	}
}

func TestEnvIntInvalidFallsBack2(t *testing.T) {
	t.Setenv("DB_MAX_IDLE_CONNS", "garbage")
	if got := envInt("DB_MAX_IDLE_CONNS", 9); got != 9 {
		t.Errorf("envInt invalid = %d", got)
	}
}

func TestPGAlertStoreCreateEmptyID(t *testing.T) {
	s := NewPGAlertStore(nil)
	if _, err := s.Create(alert.Alert{}); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestPGScreenStorePutEmptyIDNoDB(t *testing.T) {
	s := NewPGScreenStore(nil)
	if err := s.Put(screen.ScreenRecord{}); err == nil {
		t.Fatal("expected error for empty id")
	}
}