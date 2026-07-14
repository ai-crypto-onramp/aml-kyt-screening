// Package screen is the synchronous screen path. It wires cache lookup ->
// vendor screen -> decision logic -> persist kyt_screens row, and emits an
// audit event + alert for non-allow decisions.
package screen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/audit"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/decision"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/metrics"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/vendor"
)

// Request is the input to the synchronous screen path.
type Request struct {
	TxID          string `json:"tx_id"`
	Address       string `json:"address"`
	SourceAddress string `json:"source_address,omitempty"`
	Chain         string `json:"chain"`
	Amount        string `json:"amount"`
}

// Response is the output of the synchronous screen path.
type Response struct {
	ScreenID  string `json:"screen_id"`
	RiskScore int    `json:"risk_score"`
	Exposure  string `json:"exposure"`
	Decision  string `json:"decision"`
	Vendor    string `json:"vendor,omitempty"`
	CacheHit  bool   `json:"cache_hit"`
}

// ScreenRecord is the persisted row in kyt_screens.
type ScreenRecord struct {
	ScreenID        string
	TxID            string
	Address         string
	SourceAddress   string
	Chain           string
	Amount          string
	RiskScore       int
	Exposure        string
	Decision        string
	Vendor          string
	VendorResponseID string
	CacheHit        bool
	CreatedAt       time.Time
}

// ScreenStore persists kyt_screens rows.
type ScreenStore interface {
	Put(rec ScreenRecord) error
	Get(id string) (ScreenRecord, bool, error)
	ListByAddress(address, chain string) ([]ScreenRecord, error)
}

// Cache is the subset of the store.Cache interface used here.
type Cache interface {
	Get(ctx context.Context, address, chain string) (Verdict, bool, error)
	Set(ctx context.Context, v Verdict) error
	Delete(ctx context.Context, address, chain string) error
}

// Verdict is the cached address-risk verdict.
type Verdict struct {
	Address   string
	Chain     string
	RiskScore int
	Exposure  string
	Decision  string
	Vendor    string
}

// Service is the synchronous screen path.
type Service struct {
	cache     Cache
	provider  vendor.ScreenProvider
	threshold *decision.Thresholds
	screens   ScreenStore
	alerts    *alert.Service
	audit     *audit.Emitter
	metrics   *metrics.Metrics
	now       func() time.Time
	id        func() string
	mu        sync.Mutex
}

// NewService returns a screen Service.
func NewService(cache Cache, provider vendor.ScreenProvider, th *decision.Thresholds, screens ScreenStore, alerts *alert.Service, auditEmitter *audit.Emitter) *Service {
	return &Service{
		cache:     cache,
		provider:  provider,
		threshold: th,
		screens:   screens,
		alerts:    alerts,
		audit:     auditEmitter,
		metrics:   metrics.Global(),
		now:       time.Now,
		id:        newID,
	}
}

// WithNow overrides the clock (for testing).
func (s *Service) WithNow(now func() time.Time) *Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
	return s
}

// WithID overrides the screen id generator (for testing).
func (s *Service) WithID(id func() string) *Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
	return s
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Screen runs the synchronous screen path.
func (s *Service) Screen(ctx context.Context, req Request) (Response, error) {
	if err := validate(req); err != nil {
		return Response{}, err
	}
	start := time.Now()
	defer func() {
		s.metrics.ObserveScreenDuration(time.Since(start).Nanoseconds())
		s.metrics.ScreenTotal.Add(1)
	}()

	// 1. Cache lookup.
	if s.cache != nil {
		if v, ok, err := s.cache.Get(ctx, req.Address, req.Chain); err == nil && ok {
			s.metrics.CacheHitsTotal.Add(1)
			return s.finalize(ctx, req, v.RiskScore, v.Exposure, v.Decision, v.Vendor, true, start)
		}
	}
	s.metrics.CacheMissesTotal.Add(1)

	// 2. Vendor screen.
	vendorReq := vendor.ScreenRequest{
		TxID:          req.TxID,
		Address:       req.Address,
		SourceAddress: req.SourceAddress,
		Chain:         req.Chain,
		Amount:        req.Amount,
		IdempotencyKey: vendor.IdempotencyKey(req.TxID, req.Address, req.Chain),
	}
	vresp, verr := s.provider.Screen(ctx, vendorReq)
	if verr != nil {
		// Vendor-unreachable: fail-safe to manual_review.
		s.metrics.VendorErrorsTotal.Add(1)
		decision_ := decision.DecideVendorUnreachable()
		return s.finalize(ctx, req, 0, decision.ExposureUnknown, decision_, "", false, start)
	}

	// 3. Derive exposure & decision.
	exposure := vresp.Exposure
	if exposure == "" {
		exposure = s.threshold.ExposureFromScore(vresp.RiskScore, req.Chain)
	}
	decisionVal := s.threshold.Decide(exposure)

	// 4. Cache the verdict.
	if s.cache != nil {
		_ = s.cache.Set(ctx, Verdict{
			Address:   req.Address,
			Chain:     req.Chain,
			RiskScore: vresp.RiskScore,
			Exposure:  exposure,
			Decision:  decisionVal,
			Vendor:    vresp.Vendor,
		})
	}

	return s.finalize(ctx, req, vresp.RiskScore, exposure, decisionVal, vresp.Vendor, false, start)
}

// finalize persists the kyt_screens row, emits an audit event, creates an alert
// for non-allow decisions, and returns the response.
func (s *Service) finalize(ctx context.Context, req Request, score int, exposure, dec, vend string, cacheHit bool, start time.Time) (Response, error) {
	s.mu.Lock()
	id := s.id()
	now := s.now()
	s.mu.Unlock()

	rec := ScreenRecord{
		ScreenID:      id,
		TxID:          req.TxID,
		Address:       req.Address,
		SourceAddress: req.SourceAddress,
		Chain:         req.Chain,
		Amount:        req.Amount,
		RiskScore:     score,
		Exposure:      exposure,
		Decision:      dec,
		Vendor:        vend,
		CacheHit:      cacheHit,
		CreatedAt:     now,
	}
	if s.screens != nil {
		if err := s.screens.Put(rec); err != nil {
			return Response{}, fmt.Errorf("persist screen: %w", err)
		}
	}

	if dec != decision.DecisionAllow {
		if s.alerts != nil {
			_, _ = s.alerts.Create(id, req.TxID, req.Address, req.Chain, exposure, decision.SeverityFor(exposure))
			s.metrics.AlertsOpen.Add(1)
		}
	}

	if s.audit != nil {
		source := "vendor"
		if cacheHit {
			source = "cache"
		}
		_ = s.audit.Emit(ctx, audit.Event{
			ScreenID:  id,
			TxID:      req.TxID,
			Address:   req.Address,
			Chain:     req.Chain,
			Amount:    req.Amount,
			Decision:  dec,
			Exposure:  exposure,
			RiskScore: score,
			Vendor:    vend,
			CacheHit:  cacheHit,
			Source:    source,
			CreatedAt: now,
		})
	}

	return Response{
		ScreenID:  id,
		RiskScore: score,
		Exposure:  exposure,
		Decision:  dec,
		Vendor:    vend,
		CacheHit:  cacheHit,
	}, nil
}

// validate enforces required fields and basic shape.
func validate(req Request) error {
	if strings.TrimSpace(req.TxID) == "" {
		return errors.New("tx_id is required")
	}
	if strings.TrimSpace(req.Address) == "" {
		return errors.New("address is required")
	}
	if strings.TrimSpace(req.Chain) == "" {
		return errors.New("chain is required")
	}
	if strings.TrimSpace(req.Amount) == "" {
		return errors.New("amount is required")
	}
	return nil
}

// MemoryScreenStore is an in-memory ScreenStore.
type MemoryScreenStore struct {
	mu  sync.Mutex
	mem map[string]ScreenRecord
}

// NewMemoryScreenStore returns a fresh in-memory ScreenStore.
func NewMemoryScreenStore() *MemoryScreenStore {
	return &MemoryScreenStore{mem: make(map[string]ScreenRecord)}
}

// Put stores rec.
func (s *MemoryScreenStore) Put(rec ScreenRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.ScreenID == "" {
		return errors.New("screen id required")
	}
	s.mem[rec.ScreenID] = rec
	return nil
}

// Get returns the screen record by id.
func (s *MemoryScreenStore) Get(id string) (ScreenRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.mem[id]
	return r, ok, nil
}

// ListByAddress returns all stored records for (address, chain), ordered by
// created_at ascending. Used by the webhook re-classification path to trigger
// downstream review of already-settled transactions for the affected address.
func (s *MemoryScreenStore) ListByAddress(address, chain string) ([]ScreenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ScreenRecord
	for _, r := range s.mem {
		if r.Address == address && r.Chain == chain {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Len returns the number of stored records.
func (s *MemoryScreenStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.mem)
}