package alert

import (
	"errors"
	"testing"
	"time"
)

func TestServiceCreate(t *testing.T) {
	s := NewService(NewMemoryStore()).WithID(func() string { return "a1" })
	a, err := s.Create("screen1", "tx1", "0xbad", "ethereum", "SANCTIONED", "critical")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.ID != "a1" || a.Status != StatusOpen || a.Severity != "critical" {
		t.Fatalf("alert: %+v", a)
	}
	if a.ScreenID != "screen1" || a.TxID != "tx1" {
		t.Fatalf("alert refs: %+v", a)
	}
}

func TestServiceCreateDefaultSeverity(t *testing.T) {
	s := NewService(NewMemoryStore()).WithID(func() string { return "a1" })
	a, err := s.Create("", "tx1", "0xbad", "ethereum", "UNKNOWN", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.Severity != "medium" {
		t.Fatalf("default severity: %s", a.Severity)
	}
}

func TestServiceGetNotFound(t *testing.T) {
	s := NewService(NewMemoryStore())
	_, err := s.Get("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err: %v", err)
	}
}

func TestServiceAssignAndClose(t *testing.T) {
	s := NewService(NewMemoryStore()).WithID(func() string { return "a1" })
	if _, err := s.Create("", "tx1", "0xbad", "ethereum", "HIGH_RISK", "high"); err != nil {
		t.Fatalf("create: %v", err)
	}
	a, err := s.Assign("a1", "analyst1")
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if a.Status != StatusInReview || a.Assignee != "analyst1" {
		t.Fatalf("after assign: %+v", a)
	}
	closed, err := s.Close("a1", "analyst1")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.Status != StatusClosed || closed.ClosedAt == nil {
		t.Fatalf("after close: %+v", closed)
	}
}

func TestServiceCloseAlreadyClosed(t *testing.T) {
	s := NewService(NewMemoryStore()).WithID(func() string { return "a1" })
	_, _ = s.Create("", "tx1", "0xbad", "ethereum", "HIGH_RISK", "high")
	_, _ = s.Close("a1", "")
	_, err := s.Close("a1", "")
	if !errors.Is(err, ErrAlreadyClosed) {
		t.Fatalf("err: %v", err)
	}
	_, err = s.Assign("a1", "x")
	if !errors.Is(err, ErrAlreadyClosed) {
		t.Fatalf("assign closed: %v", err)
	}
}

func TestServiceListByStatus(t *testing.T) {
	store := NewMemoryStore()
	s := NewService(store).WithID(func() string { return "a1" })
	_, _ = s.Create("", "tx1", "0xbad", "ethereum", "HIGH_RISK", "high")
	s2 := NewService(store).WithID(func() string { return "a2" })
	_, _ = s2.Create("", "tx2", "0xbad2", "ethereum", "SANCTIONED", "critical")
	s3 := NewService(store).WithID(func() string { return "a3" })
	_, _ = s3.Create("", "tx3", "0xbad3", "ethereum", "HIGH_RISK", "high")
	_, _ = s3.Close("a3", "")

	open, err := s.List(StatusOpen)
	if err != nil {
		t.Fatalf("list open: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("open alerts: %d", len(open))
	}
	closed, err := s.List(StatusClosed)
	if err != nil {
		t.Fatalf("list closed: %v", err)
	}
	if len(closed) != 1 {
		t.Fatalf("closed alerts: %d", len(closed))
	}
	all, err := s.List("")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all alerts: %d", len(all))
	}
}

func TestMemoryStoreDuplicate(t *testing.T) {
	store := NewMemoryStore()
	a := Alert{ID: "x", Status: StatusOpen, CreatedAt: time.Now()}
	_, err := store.Create(a)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = store.Create(a)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("dup: %v", err)
	}
}

func TestMemoryStoreUpdateNotFound(t *testing.T) {
	store := NewMemoryStore()
	if err := store.Update(Alert{ID: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err: %v", err)
	}
}

func TestMemoryStoreCreateEmptyID(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.Create(Alert{}); err == nil {
		t.Fatal("expected error for empty id")
	}
}