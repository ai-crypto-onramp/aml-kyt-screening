package screen

import (
	"context"
	"sync"
	"time"
)

// MemoryCache is an in-memory Cache implementation used for tests and the
// DB-less fallback path. It honors the default and sanctioned TTLs.
type MemoryCache struct {
	mu             sync.Mutex
	mem            map[string]cacheEntry
	defaultTTL     time.Duration
	sanctionedTTL  time.Duration
	now            func() time.Time
}

type cacheEntry struct {
	verdict   Verdict
	expiresAt time.Time
}

func cacheKey(address, chain string) string { return chain + ":" + address }

// NewMemoryCache returns a fresh in-memory cache.
func NewMemoryCache(defaultTTL, sanctionedTTL time.Duration) *MemoryCache {
	return &MemoryCache{
		mem:           make(map[string]cacheEntry),
		defaultTTL:    defaultTTL,
		sanctionedTTL: sanctionedTTL,
		now:           time.Now,
	}
}

// WithNow overrides the clock (for testing).
func (c *MemoryCache) WithNow(now func() time.Time) *MemoryCache {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
	return c
}

// Get returns the cached verdict for (address, chain), or (Verdict{}, false) on miss.
func (c *MemoryCache) Get(_ context.Context, address, chain string) (Verdict, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.mem[cacheKey(address, chain)]
	if !ok || c.now().After(e.expiresAt) {
		return Verdict{}, false, nil
	}
	return e.verdict, true, nil
}

// Set caches v with the TTL appropriate to its exposure.
func (c *MemoryCache) Set(_ context.Context, v Verdict) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := c.defaultTTL
	if v.Exposure == "sanctioned" {
		ttl = c.sanctionedTTL
	}
	c.mem[cacheKey(v.Address, v.Chain)] = cacheEntry{verdict: v, expiresAt: c.now().Add(ttl)}
	return nil
}

// Delete removes the cached verdict for (address, chain).
func (c *MemoryCache) Delete(_ context.Context, address, chain string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.mem, cacheKey(address, chain))
	return nil
}

// Len returns the number of cached entries (including expired until pruned).
func (c *MemoryCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.mem)
}