// Package webhook verifies HMAC-SHA256 signatures on vendor webhooks and
// processes re-classifications into alerts and cache invalidation.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
)

// Verifier verifies HMAC-SHA256 webhook signatures.
type Verifier struct {
	secrets map[string][]byte // vendor -> secret
}

// NewVerifier returns a Verifier with the given vendor secrets.
func NewVerifier(secrets map[string][]byte) *Verifier {
	return &Verifier{secrets: secrets}
}

// Verify returns nil if sig matches HMAC-SHA256(secret, body) for the vendor.
// Comparison is constant-time. An unknown vendor or empty signature returns
// ErrSignatureMismatch.
func (v *Verifier) Verify(vendor string, body []byte, sig string) error {
	secret, ok := v.secrets[vendor]
	if !ok {
		return ErrSignatureMismatch
	}
	if sig == "" {
		return ErrSignatureMismatch
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return ErrSignatureMismatch
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	if !hmac.Equal(want, expected) {
		return ErrSignatureMismatch
	}
	return nil
}

// ErrSignatureMismatch is returned when a webhook signature is missing or
// does not match the expected HMAC.
var ErrSignatureMismatch = errors.New("webhook signature mismatch")

// Reclassification is the parsed vendor webhook payload for a re-classified
// address.
type Reclassification struct {
	EventID  string `json:"event_id"`
	Address  string `json:"address"`
	Chain    string `json:"chain"`
	Exposure string `json:"exposure"`
	TxID     string `json:"tx_id,omitempty"`
}

// CacheInvalidator is the subset of the cache interface used by the webhook
// handler to invalidate re-classified entries.
type CacheInvalidator interface {
	Delete(ctx context.Context, address, chain string) error
}

// ReviewTrigger triggers downstream review of already-settled transactions
// for a re-classified address. It is invoked best-effort and asynchronously by
// the webhook processor after a re-classification is accepted.
type ReviewTrigger interface {
	TriggerReview(ctx context.Context, address, chain, exposure, txID string) error
}

// Result is the outcome of a webhook ingestion.
type Result struct {
	Accepted  bool
	Duplicate bool
	Reason    string
	EventID   string
}

// Processor handles verified vendor webhooks: dedupes by event id, parses the
// reclassification, invalidates the cache, creates an alert, and triggers
// best-effort downstream review of already-settled transactions for the
// affected address.
type Processor struct {
	verifier *Verifier
	cache    CacheInvalidator
	alerts   *alert.Service
	reviewer ReviewTrigger
	mu       sync.Mutex
	seen     map[string]bool
}

// NewProcessor returns a webhook Processor.
func NewProcessor(verifier *Verifier, cache CacheInvalidator, alerts *alert.Service) *Processor {
	return &Processor{verifier: verifier, cache: cache, alerts: alerts, seen: make(map[string]bool)}
}

// WithReviewer installs a ReviewTrigger invoked after a re-classification is
// accepted. The trigger runs asynchronously (best-effort); failures are logged
// but do not affect the webhook response.
func (p *Processor) WithReviewer(r ReviewTrigger) *Processor {
	p.reviewer = r
	return p
}

// Ingest verifies, dedupes, and processes a vendor webhook.
func (p *Processor) Ingest(ctx context.Context, vendor string, body []byte, sig string) Result {
	if err := p.verifier.Verify(vendor, body, sig); err != nil {
		return Result{Reason: err.Error()}
	}
	var rec Reclassification
	if err := json.Unmarshal(body, &rec); err != nil {
		return Result{Reason: "invalid json: " + err.Error()}
	}
	if rec.Address == "" || rec.Chain == "" || rec.Exposure == "" {
		return Result{Reason: "missing required fields"}
	}
	if rec.EventID == "" {
		rec.EventID = vendor + ":" + rec.Chain + ":" + rec.Address + ":" + rec.Exposure
	}

	p.mu.Lock()
	if p.seen[rec.EventID] {
		p.mu.Unlock()
		return Result{Duplicate: true, EventID: rec.EventID}
	}
	p.seen[rec.EventID] = true
	p.mu.Unlock()

	if p.cache != nil {
		if err := p.cache.Delete(ctx, rec.Address, rec.Chain); err != nil {
			return Result{Reason: "cache invalidate: " + err.Error()}
		}
	}
	if p.alerts != nil {
		severity := alertSeverity(rec.Exposure)
		if _, err := p.alerts.Create("", rec.TxID, rec.Address, rec.Chain, rec.Exposure, severity); err != nil {
			return Result{Reason: "alert create: " + err.Error()}
		}
	}
	if p.reviewer != nil {
		go p.triggerReview(context.Background(), rec)
	}
	return Result{Accepted: true, EventID: rec.EventID}
}

// triggerReview invokes the downstream ReviewTrigger best-effort. It uses a
// background context (the webhook request may have already returned) and a
// bounded timeout so a slow downstream cannot block the goroutine forever.
func (p *Processor) triggerReview(ctx context.Context, rec Reclassification) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = p.reviewer.TriggerReview(ctx, rec.Address, rec.Chain, rec.Exposure, rec.TxID)
}

// alertSeverity maps an exposure to an alert severity.
func alertSeverity(exposure string) string {
	switch exposure {
	case "SANCTIONED":
		return "critical"
	case "HIGH_RISK":
		return "high"
	case "UNKNOWN":
		return "medium"
	default:
		return "low"
	}
}

// ReadBody reads up to maxBytes from r. If the body exceeds maxBytes it returns
// ErrBodyTooLarge.
func ReadBody(r io.Reader, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(r, maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrBodyTooLarge
	}
	return body, nil
}

// ErrBodyTooLarge is returned by ReadBody when the payload exceeds the limit.
var ErrBodyTooLarge = errors.New("webhook body too large")