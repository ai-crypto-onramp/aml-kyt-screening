package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func skipIfNoRedis(t *testing.T) string {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set; skipping live Redis test")
	}
	return url
}

func TestRedisCacheSetGetDeleteSanctionedTTL(t *testing.T) {
	url := skipIfNoRedis(t)
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	client := redis.NewClient(opts)
	defer client.Close()

	ctx := context.Background()
	// Flush test keys.
	addrSan := "test-redis-sanctioned"
	addrClean := "test-redis-clean"
	chain := "ethereum"
	_ = client.Del(ctx, redisKey(addrSan, chain), redisKey(addrClean, chain)).Err()

	def := time.Hour
	san := 24 * time.Hour
	c := NewRedisCache(client, def, san)

	// Sanctioned -> sanctioned TTL.
	v := Verdict{
		Address:   addrSan,
		Chain:     chain,
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
		t.Fatalf("sanctioned entry missing")
	}
	if got.Exposure != "sanctioned" || got.Decision != "block" {
		t.Fatalf("got = %+v", got)
	}
	if got.TTLSeconds != int(san.Seconds()) {
		t.Errorf("sanctioned ttl = %d, want %d", got.TTLSeconds, int(san.Seconds()))
	}
	// Redis TTL should be ~sanctioned TTL (allow a few seconds slack).
	ttl, err := client.TTL(ctx, redisKey(addrSan, chain)).Result()
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl > san || ttl < san-5*time.Second {
		t.Errorf("sanctioned redis ttl = %s, want ~%s", ttl, san)
	}

	// Clean -> default TTL.
	vClean := Verdict{
		Address:   addrClean,
		Chain:     chain,
		RiskScore: 5,
		Exposure:  "clean",
		Decision:  "allow",
		Vendor:    "chainalysis",
	}
	if err := c.Set(ctx, vClean); err != nil {
		t.Fatalf("set clean: %v", err)
	}
	ttlClean, err := client.TTL(ctx, redisKey(addrClean, chain)).Result()
	if err != nil {
		t.Fatalf("ttl clean: %v", err)
	}
	if ttlClean > def || ttlClean < def-5*time.Second {
		t.Errorf("clean redis ttl = %s, want ~%s", ttlClean, def)
	}

	// Delete.
	if err := c.Delete(ctx, v.Address, v.Chain); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := c.Get(ctx, v.Address, v.Chain); err != nil || ok {
		t.Fatalf("expected miss after delete (ok=%v, err=%v)", ok, err)
	}

	// Ping.
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}