// Package review implements the best-effort downstream review of
// already-settled transactions for an address that was re-classified by a
// vendor webhook. It looks up past screens for the address in the screen
// store and opens a review alert for each previously-allowed flow so the
// compliance team can re-evaluate the settled transaction in light of the new
// exposure.
package review

import (
	"context"
	"log"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/decision"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
)

// Trigger is a webhook.ReviewTrigger that opens review alerts for past allow
// decisions on a re-classified address. It is best-effort: lookup or alert
// creation failures are logged and do not propagate to the caller.
type Trigger struct {
	screens screen.ScreenStore
	alerts  *alert.Service
	logf    func(format string, args ...any)
}

// NewTrigger returns a review Trigger backed by screens and alerts.
func NewTrigger(screens screen.ScreenStore, alerts *alert.Service) *Trigger {
	return &Trigger{
		screens: screens,
		alerts:  alerts,
		logf:    log.Printf,
	}
}

// TriggerReview looks up past screens for (address, chain) and opens a
// manual_review alert for each previously-allowed flow so the compliance team
// can re-evaluate the settled transaction. Returns nil on best-effort failure
// (the error is logged).
func (t *Trigger) TriggerReview(ctx context.Context, address, chain, exposure, txID string) error {
	if t.screens == nil || t.alerts == nil {
		return nil
	}
	records, err := t.screens.ListByAddress(address, chain)
	if err != nil {
		t.logf("review: list screens for %s/%s: %v", address, chain, err)
		return nil
	}
	sev := decision.SeverityFor(exposure)
	for _, r := range records {
		if r.Decision != decision.DecisionAllow {
			continue
		}
		if _, err := t.alerts.Create(r.ScreenID, r.TxID, r.Address, r.Chain, exposure, sev); err != nil {
			t.logf("review: create alert for screen %s: %v", r.ScreenID, err)
			continue
		}
	}
	return nil
}