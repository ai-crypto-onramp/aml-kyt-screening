package vendor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// VendorResponseRecord is the durable record of a vendor exchange, persisted to
// the vendor_responses table for audit and replay.
type VendorResponseRecord struct {
	ID              string
	Vendor          string
	RequestPayload  []byte
	ResponsePayload []byte
	IdempotencyKey  string
	Address         string
	Chain           string
	TxID            string
	CreatedAt       time.Time
}

// ResponseStore persists vendor responses keyed on idempotency key.
type ResponseStore interface {
	// Get returns the previously-stored response for idempotencyKey, or
	// (nil, false) if absent.
	Get(ctx context.Context, idempotencyKey string) (*VendorResponseRecord, bool, error)
	// Put persists rec. Implementations must enforce uniqueness on
	// IdempotencyKey so a duplicate put is a no-op.
	Put(ctx context.Context, rec VendorResponseRecord) error
}

// MemoryResponseStore is an in-memory ResponseStore for tests and the DB-less
// fallback path.
type MemoryResponseStore struct {
	mu  sync.Mutex
	mem map[string]VendorResponseRecord
}

// NewMemoryResponseStore returns a fresh in-memory ResponseStore.
func NewMemoryResponseStore() *MemoryResponseStore {
	return &MemoryResponseStore{mem: make(map[string]VendorResponseRecord)}
}

// Get returns the stored record for key, or (nil, false) on miss.
func (s *MemoryResponseStore) Get(_ context.Context, key string) (*VendorResponseRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.mem[key]
	if !ok {
		return nil, false, nil
	}
	rec := r
	return &rec, true, nil
}

// Put persists rec; duplicates on IdempotencyKey are silently ignored.
func (s *MemoryResponseStore) Put(_ context.Context, rec VendorResponseRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.IdempotencyKey == "" {
		return errors.New("idempotency key required")
	}
	if _, exists := s.mem[rec.IdempotencyKey]; exists {
		return nil
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	s.mem[rec.IdempotencyKey] = rec
	return nil
}

// Len returns the number of stored records.
func (s *MemoryResponseStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.mem)
}

// IdempotentProvider wraps a ScreenProvider with idempotent response caching.
// Repeated calls with the same idempotency key return the cached response
// (no double vendor billing).
type IdempotentProvider struct {
	inner ScreenProvider
	store ResponseStore
}

// NewIdempotentProvider returns an idempotent-caching wrapper around inner.
func NewIdempotentProvider(inner ScreenProvider, store ResponseStore) *IdempotentProvider {
	return &IdempotentProvider{inner: inner, store: store}
}

// Name returns the underlying provider name.
func (p *IdempotentProvider) Name() string { return p.inner.Name() }

// Screen returns the cached response for req.IdempotencyKey if present;
// otherwise calls inner, persists the raw I/O, and returns the result.
func (p *IdempotentProvider) Screen(ctx context.Context, req ScreenRequest) (ScreenResponse, error) {
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = IdempotencyKey(req.TxID, req.Address, req.Chain)
	}
	if cached, ok, err := p.store.Get(ctx, req.IdempotencyKey); err == nil && ok {
		_ = cached
		// Re-hydrate the cached response payload.
		var resp struct {
			RiskScore int    `json:"risk_score"`
			Exposure  string `json:"exposure"`
		}
		if err := json.Unmarshal(cached.ResponsePayload, &resp); err == nil {
			return ScreenResponse{
				Vendor:     cached.Vendor,
				RiskScore:  resp.RiskScore,
				Exposure:   resp.Exposure,
				RawRequest: cached.RequestPayload,
				RawResponse: cached.ResponsePayload,
			}, nil
		}
	}
	resp, err := p.inner.Screen(ctx, req)
	if err != nil {
		return resp, err
	}
	// Ensure raw payloads are populated so the cached record can be
	// re-hydrated on a cache hit (mock providers leave them empty).
	if len(resp.RawRequest) == 0 {
		if b, mErr := json.Marshal(req); mErr == nil {
			resp.RawRequest = b
		}
	}
	if len(resp.RawResponse) == 0 {
		if b, mErr := json.Marshal(map[string]any{
			"risk_score": resp.RiskScore,
			"exposure":   resp.Exposure,
		}); mErr == nil {
			resp.RawResponse = b
		}
	}
	rec := VendorResponseRecord{
		Vendor:          resp.Vendor,
		RequestPayload:  resp.RawRequest,
		ResponsePayload: resp.RawResponse,
		IdempotencyKey:  req.IdempotencyKey,
		Address:         req.Address,
		Chain:           req.Chain,
		TxID:            req.TxID,
		CreatedAt:       time.Now(),
	}
	if err := p.store.Put(ctx, rec); err != nil {
		return resp, fmt.Errorf("persist vendor response: %w", err)
	}
	return resp, nil
}