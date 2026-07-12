// Package decision centralizes the exposure -> decision mapping and per-chain
// threshold evaluation, including the fail-safe behavior for unknown and
// vendor-unreachable.
package decision

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// Exposure classifications returned by KYT vendors.
const (
	ExposureSanctioned = "sanctioned"
	ExposureHighRisk   = "high_risk"
	ExposureUnknown    = "unknown"
	ExposureClean      = "clean"
)

// Decision values returned by the screen endpoint.
const (
	DecisionBlock        = "block"
	DecisionManualReview = "manual_review"
	DecisionAllow        = "allow"
)

// Thresholds configures the per-chain and global thresholds used to derive an
// exposure from a numeric risk score and then a decision from an exposure.
type Thresholds struct {
	BlockThreshold       int // >= -> sanctioned -> block
	HighRiskThreshold    int // >= -> high_risk   -> manual_review
	UnknownDecision      string // decision for unknown exposure
	perChain             map[string]chainThreshold
	mu                   sync.RWMutex
}

type chainThreshold struct {
	Block    int
	HighRisk int
}

// DefaultThresholds returns a Thresholds populated from env with README defaults.
// Per-chain overrides are read from BLOCK_THRESHOLD_<CHAIN> and
// HIGH_RISK_THRESHOLD_<CHAIN>.
func DefaultThresholds() *Thresholds {
	t := &Thresholds{
		BlockThreshold:    envInt("BLOCK_THRESHOLD", 90),
		HighRiskThreshold: envInt("HIGH_RISK_THRESHOLD", 50),
		UnknownDecision:   envOr("UNKNOWN_DECISION", DecisionManualReview),
		perChain:          make(map[string]chainThreshold),
	}
	t.ReloadFromEnv()
	return t
}

// NewThresholds returns a Thresholds with the given global defaults and no
// per-chain overrides. Used by tests that want to bypass env parsing.
func NewThresholds(block, highRisk int, unknownDecision string) *Thresholds {
	if unknownDecision != DecisionBlock && unknownDecision != DecisionManualReview {
		unknownDecision = DecisionManualReview
	}
	return &Thresholds{
		BlockThreshold:    block,
		HighRiskThreshold: highRisk,
		UnknownDecision:   unknownDecision,
		perChain:          make(map[string]chainThreshold),
	}
}

// ReloadFromenv re-reads env vars. Hot-reload entry point.
func (t *Thresholds) ReloadFromEnv() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.BlockThreshold = envInt("BLOCK_THRESHOLD", t.BlockThreshold)
	t.HighRiskThreshold = envInt("HIGH_RISK_THRESHOLD", t.HighRiskThreshold)
	t.UnknownDecision = envOr("UNKNOWN_DECISION", t.UnknownDecision)
	if t.UnknownDecision != DecisionManualReview && t.UnknownDecision != DecisionBlock {
		t.UnknownDecision = DecisionManualReview
	}
	t.perChain = make(map[string]chainThreshold)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BLOCK_THRESHOLD_") && !strings.HasPrefix(kv, "HIGH_RISK_THRESHOLD_") {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		n, err := strconv.Atoi(val)
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(key, "BLOCK_THRESHOLD_"):
			chain := strings.ToLower(strings.TrimPrefix(key, "BLOCK_THRESHOLD_"))
			c := t.perChain[chain]
			c.Block = n
			t.perChain[chain] = c
		case strings.HasPrefix(key, "HIGH_RISK_THRESHOLD_"):
			chain := strings.ToLower(strings.TrimPrefix(key, "HIGH_RISK_THRESHOLD_"))
			c := t.perChain[chain]
			c.HighRisk = n
			t.perChain[chain] = c
		}
	}
}

// SetChainOverride sets a per-chain threshold override (for testing).
func (t *Thresholds) SetChainOverride(chain string, block, highRisk int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.perChain[strings.ToLower(chain)] = chainThreshold{Block: block, HighRisk: highRisk}
}

// thresholdsFor returns the effective thresholds for chain (per-chain override
// takes precedence; global default otherwise).
func (t *Thresholds) thresholdsFor(chain string) (block, highRisk int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	block, highRisk = t.BlockThreshold, t.HighRiskThreshold
	if c, ok := t.perChain[strings.ToLower(chain)]; ok {
		if c.Block > 0 {
			block = c.Block
		}
		if c.HighRisk > 0 {
			highRisk = c.HighRisk
		}
	}
	return
}

// ExposureFromScore derives the exposure classification from a numeric risk
// score using the configured thresholds for chain.
func (t *Thresholds) ExposureFromScore(score int, chain string) string {
	block, high := t.thresholdsFor(chain)
	switch {
	case score >= block:
		return ExposureSanctioned
	case score >= high:
		return ExposureHighRisk
	case score < 0:
		return ExposureUnknown
	default:
		return ExposureClean
	}
}

// Decide maps an exposure classification to a decision. Unknown follows the
// configured UnknownDecision (fail-safe: never allow).
func (t *Thresholds) Decide(exposure string) string {
	switch exposure {
	case ExposureSanctioned:
		return DecisionBlock
	case ExposureHighRisk:
		return DecisionManualReview
	case ExposureClean:
		return DecisionAllow
	case ExposureUnknown:
		t.mu.RLock()
		ud := t.UnknownDecision
		t.mu.RUnlock()
		if ud != DecisionBlock && ud != DecisionManualReview {
			ud = DecisionManualReview
		}
		return ud
	}
	// Unknown exposure classification: fail-safe to manual_review, never allow.
	return DecisionManualReview
}

// SeverityFor returns the alert severity for an exposure. Sanctioned -> critical,
// high_risk -> high, unknown -> medium, clean -> low.
func SeverityFor(exposure string) string {
	switch exposure {
	case ExposureSanctioned:
		return "critical"
	case ExposureHighRisk:
		return "high"
	case ExposureUnknown:
		return "medium"
	default:
		return "low"
	}
}

// DecideVendorUnreachable is the fail-safe decision when the vendor cannot be
// reached. It never returns allow.
func DecideVendorUnreachable() string { return DecisionManualReview }

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}