package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
)

func TestPGScreenStorePutGet(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	s := NewPGScreenStore(db)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	rec := screen.ScreenRecord{
		ScreenID:        "11111111-2222-3333-4444-555555555555",
		TxID:            "tx-screen-1",
		Address:         "0xscreen1",
		SourceAddress:   "0xsrc1",
		Chain:           "ethereum",
		Amount:          "100.5",
		RiskScore:       42,
		Exposure:        "high_risk",
		Decision:        "manual_review",
		Vendor:          "chainalysis",
		VendorResponseID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		CacheHit:        false,
		CreatedAt:       now,
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
	if os.Getenv("DB_URL") == "" {
		t.Skip("DB_URL not set")
	}
	ctx := context.Background()
	db, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	screenStore := NewPGScreenStore(db)
	alertStore := NewPGAlertStore(db)

	screenID := "22222222-3333-4444-5555-666666666666"
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := screenStore.Put(screen.ScreenRecord{
		ScreenID:  screenID,
		TxID:     "tx-alert-1",
		Address:  "0xalert1",
		Chain:    "ethereum",
		Amount:   "50",
		RiskScore: 99,
		Exposure: "sanctioned",
		Decision: "block",
		Vendor:   "chainalysis",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("put screen: %v", err)
	}

	alertID := "33333333-4444-5555-6666-777777777777"
	a := alert.Alert{
		ID:        alertID,
		ScreenID:  screenID,
		TxID:      "tx-alert-1",
		Address:   "0xalert1",
		Chain:     "ethereum",
		Exposure:  "sanctioned",
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

	alertStore := NewPGAlertStore(db)
	alertID := "44444444-5555-6666-7777-888888888888"
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	a := alert.Alert{
		ID:        alertID,
		TxID:      "tx-dup",
		Address:   "0xdup",
		Chain:     "ethereum",
		Exposure:  "high_risk",
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

	alertStore := NewPGAlertStore(db)
	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	for i, st := range []string{alert.StatusOpen, alert.StatusClosed, alert.StatusOpen, alert.StatusInReview} {
		id := fmt.Sprintf("55555555-6666-7777-8888-%012d", i)
		a := alert.Alert{
			ID:        id,
			TxID:      "tx-list",
			Address:   "0xlist",
			Chain:     "ethereum",
			Exposure:  "high_risk",
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
	if len(open) != 2 {
		t.Fatalf("open count: %d", len(open))
	}
	all, err := alertStore.List("")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) < 4 {
		t.Fatalf("all count: %d", len(all))
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

	alertStore := NewPGAlertStore(db)
	alertID := "66666666-7777-8888-9999-aaaaaaaaaaaa"
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	a := alert.Alert{
		ID:        alertID,
		TxID:      "tx-upd",
		Address:   "0xupd",
		Chain:     "ethereum",
		Exposure:  "high_risk",
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

func padInt(i int) string {
	s := ""
	switch {
	case i < 10:
		s = "00000000000"
	case i < 100:
		s = "0000000000"
	}
	return s + intToStr(i)
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	digits := "0123456789"
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	return string(b)
}