package review

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/decision"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
)

func TestTriggerOpensReviewForPastAllows(t *testing.T) {
	screenStore := screen.NewMemoryScreenStore()
	alertStore := alert.NewMemoryStore()
	alerts := alert.NewService(alertStore)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = screenStore.Put(screen.ScreenRecord{ScreenID: "s1", TxID: "tx1", Address: "0x1", Chain: "ethereum", Amount: "1", Decision: decision.DecisionAllow, Exposure: "clean", CreatedAt: now})
	_ = screenStore.Put(screen.ScreenRecord{ScreenID: "s2", TxID: "tx2", Address: "0x1", Chain: "ethereum", Amount: "2", Decision: decision.DecisionBlock, Exposure: "sanctioned", CreatedAt: now.Add(time.Minute)})

	tr := NewTrigger(screenStore, alerts)
	if err := tr.TriggerReview(context.Background(), "0x1", "ethereum", "sanctioned", ""); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	open, _ := alerts.List(alert.StatusOpen)
	if len(open) != 1 {
		t.Fatalf("expected 1 review alert for past allow, got %d", len(open))
	}
	if open[0].ScreenID != "s1" || open[0].Exposure != "sanctioned" || open[0].Severity != "critical" {
		t.Errorf("review alert: %+v", open[0])
	}
}

func TestTriggerNoOpWhenDepsNil(t *testing.T) {
	tr := NewTrigger(nil, nil)
	if err := tr.TriggerReview(context.Background(), "0x1", "ethereum", "sanctioned", ""); err != nil {
		t.Fatalf("trigger: %v", err)
	}
}

func TestTriggerHandlesListError(t *testing.T) {
	tr := NewTrigger(&errScreenStore{}, alert.NewService(alert.NewMemoryStore()))
	tr.logf = func(format string, args ...any) {}
	if err := tr.TriggerReview(context.Background(), "0x1", "ethereum", "sanctioned", ""); err != nil {
		t.Fatalf("trigger should swallow list errors: %v", err)
	}
}

func TestTriggerHandlesAlertCreateError(t *testing.T) {
	screenStore := screen.NewMemoryScreenStore()
	_ = screenStore.Put(screen.ScreenRecord{ScreenID: "s1", TxID: "tx1", Address: "0x1", Chain: "ethereum", Decision: decision.DecisionAllow, Exposure: "clean", CreatedAt: time.Now()})
	tr := NewTrigger(screenStore, alert.NewService(alert.NewMemoryStore()))
	tr.logf = func(format string, args ...any) {}
	// Seed an alert with id "s1" to force a duplicate error on Create.
	_, _ = tr.alerts.Create("s1", "tx1", "0x1", "ethereum", "sanctioned", "critical")
	if err := tr.TriggerReview(context.Background(), "0x1", "ethereum", "sanctioned", ""); err != nil {
		t.Fatalf("trigger should swallow alert create errors: %v", err)
	}
}

type errScreenStore struct{}

func (errScreenStore) Put(rec screen.ScreenRecord) error                          { return nil }
func (errScreenStore) Get(id string) (screen.ScreenRecord, bool, error)           { return screen.ScreenRecord{}, false, nil }
func (errScreenStore) ListByAddress(address, chain string) ([]screen.ScreenRecord, error) {
	return nil, errors.New("store down")
}