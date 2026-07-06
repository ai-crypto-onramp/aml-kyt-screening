package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PGCache is a Postgres-backed implementation of Cache. It uses the
// address_risk_cache table (created by migration 0001) as the store.
type PGCache struct {
	db             *sql.DB
	defaultTTL     time.Duration
	sanctionedTTL  time.Duration
	now            func() time.Time
}

// PGCacheOption configures a PGCache.
type PGCacheOption func(*PGCache)

// WithNow overrides the clock used to compute expiry (for testing).
func WithNow(now func() time.Time) PGCacheOption {
	return func(c *PGCache) { c.now = now }
}

// NewPGCache returns a PG-backed cache. defaultTTL applies to clean/unknown
// verdicts; sanctionedTTL applies to sanctioned verdicts.
func NewPGCache(db *sql.DB, defaultTTL, sanctionedTTL time.Duration, opts ...PGCacheOption) *PGCache {
	c := &PGCache{
		db:            db,
		defaultTTL:    defaultTTL,
		sanctionedTTL: sanctionedTTL,
		now:           time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *PGCache) Get(ctx context.Context, address, chain string) (Verdict, bool, error) {
	row := c.db.QueryRowContext(ctx, `
SELECT address, chain, risk_score, exposure, decision, vendor, cached_at, ttl_seconds, expires_at
  FROM address_risk_cache
 WHERE address = $1 AND chain = $2 AND expires_at > $3`,
		address, chain, c.now())

	var v Verdict
	err := row.Scan(
		&v.Address, &v.Chain, &v.RiskScore, &v.Exposure, &v.Decision,
		&v.Vendor, &v.CachedAt, &v.TTLSeconds, &v.ExpiresAt,
	)
	if err == sql.ErrNoRows {
		return Verdict{}, false, nil
	}
	if err != nil {
		return Verdict{}, false, fmt.Errorf("pgcache get: %w", err)
	}
	return v, true, nil
}

func (c *PGCache) Set(ctx context.Context, v Verdict) error {
	ttl := TTLFor(v.Exposure, c.defaultTTL, c.sanctionedTTL)
	now := c.now()
	if v.CachedAt.IsZero() {
		v.CachedAt = now
	}
	if v.TTLSeconds == 0 {
		v.TTLSeconds = int(ttl.Seconds())
	}
	if v.ExpiresAt.IsZero() {
		v.ExpiresAt = v.CachedAt.Add(ttl)
	}
	_, err := c.db.ExecContext(ctx, `
INSERT INTO address_risk_cache
  (address, chain, risk_score, exposure, decision, vendor, cached_at, ttl_seconds, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (address, chain) DO UPDATE SET
  risk_score  = EXCLUDED.risk_score,
  exposure    = EXCLUDED.exposure,
  decision    = EXCLUDED.decision,
  vendor      = EXCLUDED.vendor,
  cached_at   = EXCLUDED.cached_at,
  ttl_seconds = EXCLUDED.ttl_seconds,
  expires_at  = EXCLUDED.expires_at`,
		v.Address, v.Chain, v.RiskScore, v.Exposure, v.Decision, v.Vendor,
		v.CachedAt, v.TTLSeconds, v.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("pgcache set: %w", err)
	}
	return nil
}

func (c *PGCache) Delete(ctx context.Context, address, chain string) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM address_risk_cache WHERE address = $1 AND chain = $2`, address, chain)
	if err != nil {
		return fmt.Errorf("pgcache delete: %w", err)
	}
	return nil
}

func (c *PGCache) Ping(ctx context.Context) error { return c.db.PingContext(ctx) }

func (c *PGCache) Close() error { return c.db.Close() }