package store

import (
	"context"
	"time"
)

// Verdict is the cached address-risk verdict returned to the hot screen path.
type Verdict struct {
	Address    string
	Chain      string
	RiskScore  int
	Exposure   string
	Decision   string
	Vendor     string
	CachedAt   time.Time
	TTLSeconds int
	ExpiresAt  time.Time
}

// Cache is the address-risk cache abstraction used by the hot screen path to
// avoid repeated vendor calls for the same (address, chain) within the TTL.
//
// Implementations must honor the default and sanctioned TTLs: sanctioned
// verdicts are cached for the longer sanctioned TTL; clean/unknown use the
// default TTL.
type Cache interface {
	// Get returns the cached verdict for (address, chain), or
	// (Verdict{}, false) on miss.
	Get(ctx context.Context, address, chain string) (Verdict, bool, error)
	// Set caches verdict with the TTL appropriate to its exposure.
	Set(ctx context.Context, v Verdict) error
	// Delete removes the cached verdict for (address, chain); used by webhook
	// re-classification handlers.
	Delete(ctx context.Context, address, chain string) error
	// Ping reports liveness for health checks.
	Ping(ctx context.Context) error
	// Close releases underlying resources.
	Close() error
}

// TTLFor returns the TTL to apply for an exposure given the default and
// sanctioned TTLs. Sanctioned verdicts use the longer (sanctioned) TTL;
// any other verdict uses the default TTL.
func TTLFor(exposure string, defaultTTL, sanctionedTTL time.Duration) time.Duration {
	if exposure == "SANCTIONED" {
		return sanctionedTTL
	}
	return defaultTTL
}