// Package vendor wraps Chainalysis and TRM Labs KYT APIs behind a swappable
// ScreenProvider interface with circuit breaker, timeouts, and idempotency keys.
package vendor

import (
	"context"
	"errors"
	"time"
)

// ScreenRequest is the input to a KYT vendor screen call.
type ScreenRequest struct {
	TxID         string
	Address      string
	SourceAddress string
	Chain        string
	Amount       string
	IdempotencyKey string
}

// ScreenResponse is the output of a KYT vendor screen call.
type ScreenResponse struct {
	Vendor     string
	RiskScore  int
	Exposure   string
	RawRequest  []byte
	RawResponse []byte
}

// ScreenProvider is the swappable interface implemented by ChainalysisProvider,
// TRMProvider, and MockProvider.
type ScreenProvider interface {
	Name() string
	Screen(ctx context.Context, req ScreenRequest) (ScreenResponse, error)
}

// ErrVendorUnavailable is returned when the vendor cannot be reached or the
// circuit breaker is open. The decision path degrades to manual_review in this
// case and never returns allow.
var ErrVendorUnavailable = errors.New("vendor unavailable")

// IdempotencyKey computes the deterministic idempotency key for a screen
// request: (tx_id, address, chain). Repeated orchestrator retries with the
// same key must return the cached vendor response instead of re-querying the
// vendor.
func IdempotencyKey(txID, address, chain string) string {
	return txID + ":" + address + ":" + chain
}

// Config holds vendor client configuration read from the environment.
type Config struct {
	VendorTimeout             time.Duration
	CircuitBreakerThreshold   int
	CircuitBreakerOpenFor     time.Duration
	ChainalysisAPIKey         string
	ChainalysisAPIURL         string
	TRMAPIKey                 string
	TRMAPIURL                 string
}

// DefaultConfig returns a Config populated from env vars with README defaults.
func DefaultConfig() Config {
	return Config{
		VendorTimeout:           envDuration("VENDOR_TIMEOUT_MS", 800*time.Millisecond),
		CircuitBreakerThreshold: envInt("VENDOR_CIRCUIT_BREAKER_THRESHOLD", 5),
		CircuitBreakerOpenFor:   60 * time.Second,
		ChainalysisAPIKey:       osGetenv("CHAINALYSIS_API_KEY"),
		ChainalysisAPIURL:       envOr("CHAINALYSIS_API_URL", "https://api.chainalysis.com"),
		TRMAPIKey:               osGetenv("TRM_API_KEY"),
		TRMAPIURL:               envOr("TRM_API_URL", "https://api.trmlabs.com"),
	}
}