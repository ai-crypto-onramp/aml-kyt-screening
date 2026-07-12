package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache is a Redis-backed implementation of Cache.
type RedisCache struct {
	client        *redis.Client
	defaultTTL    time.Duration
	sanctionedTTL time.Duration
	now           func() time.Time
}

// RedisCacheOption configures a RedisCache.
type RedisCacheOption func(*RedisCache)

// WithRedisNow overrides the clock used to compute expiry (for testing).
func WithRedisNow(now func() time.Time) RedisCacheOption {
	return func(c *RedisCache) { c.now = now }
}

// NewRedisCache returns a Redis-backed cache. defaultTTL applies to clean/unknown
// verdicts; sanctionedTTL applies to sanctioned verdicts.
func NewRedisCache(client *redis.Client, defaultTTL, sanctionedTTL time.Duration, opts ...RedisCacheOption) *RedisCache {
	c := &RedisCache{
		client:        client,
		defaultTTL:    defaultTTL,
		sanctionedTTL: sanctionedTTL,
		now:           time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func redisKey(address, chain string) string { return "arc:" + chain + ":" + address }

func (c *RedisCache) Get(ctx context.Context, address, chain string) (Verdict, bool, error) {
	raw, err := c.client.Get(ctx, redisKey(address, chain)).Bytes()
	if err == redis.Nil {
		return Verdict{}, false, nil
	}
	if err != nil {
		return Verdict{}, false, fmt.Errorf("rediscache get: %w", err)
	}
	var v Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		return Verdict{}, false, fmt.Errorf("rediscache decode: %w", err)
	}
	return v, true, nil
}

func (c *RedisCache) Set(ctx context.Context, v Verdict) error {
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
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("rediscache encode: %w", err)
	}
	if err := c.client.Set(ctx, redisKey(v.Address, v.Chain), body, ttl).Err(); err != nil {
		return fmt.Errorf("rediscache set: %w", err)
	}
	return nil
}

func (c *RedisCache) Delete(ctx context.Context, address, chain string) error {
	if err := c.client.Del(ctx, redisKey(address, chain)).Err(); err != nil {
		return fmt.Errorf("rediscache delete: %w", err)
	}
	return nil
}

func (c *RedisCache) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }

func (c *RedisCache) Close() error { return c.client.Close() }