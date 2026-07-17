package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
)

// skipIfNoDB skips tests requiring a live Postgres. Set DB_URL in CI/local to
// run them; otherwise they skip.
func skipIfNoDB(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		t.Skip("DB_URL not set; skipping live Postgres test")
	}
	return dsn
}

// pgTestPrefix marks rows created by these tests so they can be cleaned up
// before each run, keeping tests isolated on a shared DB.
const pgTestPrefix = "pgtest-"

// deleteAlerts deletes all given alert ids so tests are idempotent on a shared DB.
func deleteAlerts(t *testing.T, db *sql.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := db.Exec(`DELETE FROM kyt_alerts WHERE id = $1`, id); err != nil {
			t.Fatalf("clean alert %s: %v", id, err)
		}
	}
}

// deleteScreens deletes all given screen ids so tests are idempotent on a shared DB.
func deleteScreens(t *testing.T, db *sql.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := db.Exec(`DELETE FROM kyt_screens WHERE screen_id = $1`, id); err != nil {
			t.Fatalf("clean screen %s: %v", id, err)
		}
	}
}

func TestPGScreenStorePutGet(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	screenID := "11111111-2222-3333-4444-555555555555"
	deleteScreens(t, db, screenID)

	s := NewPGScreenStore(db)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	rec := screen.ScreenRecord{
		ScreenID:         screenID,
		TxID:             "tx-screen-1",
		Address:          pgTestPrefix + "screen1",
		SourceAddress:    "0xsrc1",
		Chain:            "ethereum",
		Amount:           "100.50000000",
		RiskScore:        42,
		Exposure:         "HIGH_RISK",
		Decision:         "MANUAL_REVIEW",
		Vendor:           "chainalysis",
		VendorResponseID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		CacheHit:         false,
		CreatedAt:        now,
	}
	if err := s.Put(rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.Get(rec.ScreenID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatalf("screen not found")
	}
	if got.ScreenID != rec.ScreenID || got.TxID != rec.TxID || got.Address != rec.Address ||
		got.SourceAddress != rec.SourceAddress || got.Chain != rec.Chain || got.Amount != rec.Amount ||
		got.RiskScore != rec.RiskScore || got.Exposure != rec.Exposure || got.Decision != rec.Decision ||
		got.Vendor != rec.Vendor || got.VendorResponseID != rec.VendorResponseID || got.CacheHit != rec.CacheHit {
		t.Fatalf("round-trip mismatch:\n got=%+v\n want=%+v", got, rec)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("created_at: got %v want %v", got.CreatedAt, now)
	}
}

func TestPGScreenStoreGetMiss(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	s := NewPGScreenStore(db)
	_, ok, err := s.Get("99999999-9999-9999-9999-999999999999")
	if err != nil {
		t.Fatalf("get miss err: %v", err)
	}
	if ok {
		t.Fatal("expected miss")
	}
}

func TestPGScreenStorePutEmptyID(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	s := NewPGScreenStore(db)
	if err := s.Put(screen.ScreenRecord{}); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestPGAlertStoreCreateGet(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	screenID := "22222222-3333-4444-5555-666666666666"
	alertID := "33333333-4444-5555-6666-777777777777"
	deleteAlerts(t, db, alertID)
	deleteScreens(t, db, screenID)

	screenStore := NewPGScreenStore(db)
	alertStore := NewPGAlertStore(db)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := screenStore.Put(screen.ScreenRecord{
		ScreenID:  screenID,
		TxID:      "tx-alert-1",
		Address:   pgTestPrefix + "alert1",
		Chain:     "ethereum",
		Amount:    "50.00000000",
		RiskScore: 99,
		Exposure:  "SANCTIONED",
		Decision:  "BLOCK",
		Vendor:    "chainalysis",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("put screen: %v", err)
	}

	a := alert.Alert{
		ID:        alertID,
		ScreenID:  screenID,
		TxID:      "tx-alert-1",
		Address:   pgTestPrefix + "alert1",
		Chain:     "ethereum",
		Exposure:  "SANCTIONED",
		Severity:  "critical",
		Status:    alert.StatusOpen,
		CreatedAt: now,
	}
	if _, err := alertStore.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, ok, err := alertStore.Get(alertID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("alert not found")
	}
	if got.ID != a.ID || got.ScreenID != a.ScreenID || got.TxID != a.TxID ||
		got.Address != a.Address || got.Chain != a.Chain || got.Exposure != a.Exposure ||
		got.Severity != a.Severity || got.Status != a.Status {
		t.Fatalf("round-trip mismatch:\n got=%+v\n want=%+v", got, a)
	}
	if got.ClosedAt != nil {
		t.Errorf("closed_at should be nil, got %v", got.ClosedAt)
	}
}

func TestPGAlertStoreDuplicate(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	alertID := "44444444-5555-6666-7777-888888888888"
	deleteAlerts(t, db, alertID)

	alertStore := NewPGAlertStore(db)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	a := alert.Alert{
		ID:        alertID,
		TxID:      "tx-dup",
		Address:   pgTestPrefix + "dup",
		Chain:     "ethereum",
		Exposure:  "HIGH_RISK",
		Severity:  "high",
		Status:    alert.StatusOpen,
		CreatedAt: now,
	}
	if _, err := alertStore.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := alertStore.Create(a); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestPGAlertStoreListByStatus(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var ids []string
	for i := 0; i < 4; i++ {
		ids = append(ids, fmt.Sprintf("55555555-6666-7777-8888-%012d", i))
	}
	deleteAlerts(t, db, ids...)

	alertStore := NewPGAlertStore(db)
	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	statuses := []string{alert.StatusOpen, alert.StatusClosed, alert.StatusOpen, alert.StatusInReview}
	for i, st := range statuses {
		a := alert.Alert{
			ID:        ids[i],
			TxID:      "tx-list",
			Address:   pgTestPrefix + "list",
			Chain:     "ethereum",
			Exposure:  "HIGH_RISK",
			Severity:  "high",
			Status:    st,
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if st == alert.StatusClosed {
			ct := base.Add(time.Duration(i) * time.Minute).Add(time.Hour)
			a.ClosedAt = &ct
		}
		if _, err := alertStore.Create(a); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	open, err := alertStore.List(alert.StatusOpen)
	if err != nil {
		t.Fatalf("list open: %v", err)
	}
	openCount := 0
	for _, a := range open {
		if a.Address == pgTestPrefix+"list" {
			openCount++
		}
	}
	if openCount != 2 {
		t.Fatalf("open count: %d", openCount)
	}
	all, err := alertStore.List("")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	allCount := 0
	for _, a := range all {
		if a.Address == pgTestPrefix+"list" {
			allCount++
		}
	}
	if allCount != 4 {
		t.Fatalf("all count: %d", allCount)
	}
}

func TestPGAlertStoreUpdate(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	alertID := "66666666-7777-8888-9999-aaaaaaaaaaaa"
	deleteAlerts(t, db, alertID)

	alertStore := NewPGAlertStore(db)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	a := alert.Alert{
		ID:        alertID,
		TxID:      "tx-upd",
		Address:   pgTestPrefix + "upd",
		Chain:     "ethereum",
		Exposure:  "HIGH_RISK",
		Severity:  "high",
		Status:    alert.StatusOpen,
		CreatedAt: now,
	}
	if _, err := alertStore.Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	a.Status = alert.StatusInReview
	a.Assignee = "analyst1"
	if err := alertStore.Update(a); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, ok, err := alertStore.Get(alertID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("not found after update")
	}
	if got.Status != alert.StatusInReview || got.Assignee != "analyst1" {
		t.Fatalf("update not applied: %+v", got)
	}

	ct := now.Add(time.Hour)
	a.Status = alert.StatusClosed
	a.ClosedAt = &ct
	if err := alertStore.Update(a); err != nil {
		t.Fatalf("update close: %v", err)
	}
	got, _, err = alertStore.Get(alertID)
	if err != nil {
		t.Fatalf("get after close: %v", err)
	}
	if got.Status != alert.StatusClosed || got.ClosedAt == nil || !got.ClosedAt.Equal(ct) {
		t.Fatalf("close not applied: %+v", got)
	}
}

func TestPGAlertStoreUpdateNotFound(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	alertStore := NewPGAlertStore(db)
	if err := alertStore.Update(alert.Alert{ID: "99999999-9999-9999-9999-999999999999"}); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestPGAlertStoreGetMiss(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	alertStore := NewPGAlertStore(db)
	_, ok, err := alertStore.Get("88888888-9999-aaaa-bbbb-cccccccccccc")
	if err != nil {
		t.Fatalf("get miss err: %v", err)
	}
	if ok {
		t.Fatal("expected miss")
	}
}