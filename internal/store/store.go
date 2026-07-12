package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"

	"github.com/redis/go-redis/v9"
)

// Config holds the store/cache configuration read from the environment.
type Config struct {
	DBURL                 string
	RedisURL              string
	CacheTTL              time.Duration
	SanctionedCacheTTL    time.Duration
	MaxOpenConns          int
	MaxIdleConns          int
	ConnMaxLifetime       time.Duration
}

// LoadConfig reads store configuration from environment variables using the
// defaults documented in README.md.
func LoadConfig() Config {
	cfg := Config{
		DBURL:              os.Getenv("DB_URL"),
		RedisURL:           os.Getenv("REDIS_URL"),
		CacheTTL:           envDuration("CACHE_TTL_SECONDS", 3600*time.Second),
		SanctionedCacheTTL: envDuration("SANCTIONED_CACHE_TTL_SECONDS", 604800*time.Second),
		MaxOpenConns:       envInt("DB_MAX_OPEN_CONNS", 25),
		MaxIdleConns:       envInt("DB_MAX_IDLE_CONNS", 5),
		ConnMaxLifetime:    envDuration("DB_CONN_MAX_LIFETIME_SECONDS", 300*time.Second),
	}
	return cfg
}

// Open opens a pooled Postgres connection and applies all migrations.
func Open(ctx context.Context, cfg Config) (*sql.DB, error) {
	if cfg.DBURL == "" {
		return nil, fmt.Errorf("DB_URL is required")
	}
	db, err := sql.Open("postgres", cfg.DBURL)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// NewCache selects a Cache implementation based on cfg. When REDIS_URL is set a
// Redis-backed cache is returned; otherwise a PG-backed cache using db is returned.
func NewCache(ctx context.Context, cfg Config, db *sql.DB) (Cache, error) {
	if cfg.RedisURL != "" {
		opts, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			return nil, fmt.Errorf("parse REDIS_URL: %w", err)
		}
		client := redis.NewClient(opts)
		client.PoolStats()
		if err := client.Ping(ctx).Err(); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("ping redis: %w", err)
		}
		return NewRedisCache(client, cfg.CacheTTL, cfg.SanctionedCacheTTL), nil
	}
	return NewPGCache(db, cfg.CacheTTL, cfg.SanctionedCacheTTL), nil
}

// HealthChecker runs a liveness probe against db and (if set) redis for the
// /healthz handler.
type HealthChecker struct {
	db    *sql.DB
	cache Cache
}

// NewHealthChecker returns a HealthChecker for the given db and cache (cache may be nil).
func NewHealthChecker(db *sql.DB, cache Cache) *HealthChecker {
	return &HealthChecker{db: db, cache: cache}
}

// Check returns nil if all dependencies are reachable, otherwise an error
// describing the first failure.
func (h *HealthChecker) Check(ctx context.Context) error {
	if h.db != nil {
		if err := h.db.PingContext(ctx); err != nil {
			return fmt.Errorf("db: %w", err)
		}
	}
	if h.cache != nil {
		if err := h.cache.Ping(ctx); err != nil {
			return fmt.Errorf("cache: %w", err)
		}
	}
	return nil
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if secs, err := parseSeconds(v); err == nil {
			return secs
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func parseSeconds(s string) (time.Duration, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return time.Duration(n) * time.Second, nil
}