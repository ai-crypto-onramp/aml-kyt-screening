// Package alert implements the AlertService that surfaces block and
// manual_review decisions (and async webhook re-classifications) as kyt_alerts
// rows consumable by the compliance dashboard.
package alert

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Status values for an Alert.
const (
	StatusOpen     = "open"
	StatusInReview = "in_review"
	StatusClosed   = "closed"
)

// Alert is the compliance dashboard payload for a flagged flow.
type Alert struct {
	ID        string    `json:"id"`
	ScreenID  string    `json:"screen_id,omitempty"`
	TxID      string    `json:"tx_id"`
	Address   string    `json:"address"`
	Chain     string    `json:"chain"`
	Exposure  string    `json:"exposure"`
	Severity  string    `json:"severity"`
	Status    string    `json:"status"`
	Assignee  string    `json:"assignee,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

// Store is the persistence abstraction for alerts. Implementations may back
// alerts with Postgres (production) or an in-memory map (tests / DB-less mode).
type Store interface {
	Create(a Alert) (Alert, error)
	Get(id string) (Alert, bool, error)
	List(status string) ([]Alert, error)
	Update(a Alert) error
}

// Service is the alerting service.
type Service struct {
	store Store
	now   func() time.Time
	id    func() string
	mu    sync.Mutex
}

// NewService returns an AlertService backed by store.
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, id: newID}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// WithNow overrides the clock (for testing).
func (s *Service) WithNow(now func() time.Time) *Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
	return s
}

// WithID overrides the ID generator (for testing).
func (s *Service) WithID(id func() string) *Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
	return s
}

// Create writes a new open alert for the given inputs. It is called by the
// screen path whenever the decision is block or manual_review, and by the
// webhook path when a re-classification produces a new exposure.
func (s *Service) Create(screenID, txID, address, chain, exposure, severity string) (Alert, error) {
	s.mu.Lock()
	id := s.id()
	now := s.now()
	s.mu.Unlock()
	if severity == "" {
		severity = "medium"
	}
	a := Alert{
		ID:        id,
		ScreenID:  screenID,
		TxID:      txID,
		Address:   address,
		Chain:     chain,
		Exposure:  exposure,
		Severity:  severity,
		Status:    StatusOpen,
		CreatedAt: now,
	}
	return s.store.Create(a)
}

// Get returns the alert with id.
func (s *Service) Get(id string) (Alert, error) {
	a, ok, err := s.store.Get(id)
	if err != nil {
		return Alert{}, err
	}
	if !ok {
		return Alert{}, ErrNotFound
	}
	return a, nil
}

// List returns alerts filtered by status (empty status = all).
func (s *Service) List(status string) ([]Alert, error) {
	return s.store.List(status)
}

// Assign sets the assignee and transitions an open alert to in_review.
func (s *Service) Assign(id, assignee string) (Alert, error) {
	a, err := s.Get(id)
	if err != nil {
		return Alert{}, err
	}
	if a.Status == StatusClosed {
		return Alert{}, ErrAlreadyClosed
	}
	a.Status = StatusInReview
	a.Assignee = assignee
	if err := s.store.Update(a); err != nil {
		return Alert{}, err
	}
	return a, nil
}

// Close transitions an alert to closed, recording closed_at and assignee.
func (s *Service) Close(id, assignee string) (Alert, error) {
	s.mu.Lock()
	now := s.now()
	s.mu.Unlock()
	a, err := s.Get(id)
	if err != nil {
		return Alert{}, err
	}
	if a.Status == StatusClosed {
		return Alert{}, ErrAlreadyClosed
	}
	a.Status = StatusClosed
	if assignee != "" {
		a.Assignee = assignee
	}
	a.ClosedAt = &now
	if err := s.store.Update(a); err != nil {
		return Alert{}, err
	}
	return a, nil
}

// ErrNotFound is returned when an alert id does not exist.
var ErrNotFound = errors.New("alert not found")

// ErrAlreadyClosed is returned when an already-closed alert is modified.
var ErrAlreadyClosed = errors.New("alert already closed")

// MemoryStore is an in-memory implementation of Store.
type MemoryStore struct {
	mu     sync.Mutex
	mem    map[string]Alert
	order  []string
}

// NewMemoryStore returns a fresh in-memory alert store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{mem: make(map[string]Alert)}
}

// Create stores a.
func (s *MemoryStore) Create(a Alert) (Alert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == "" {
		return a, errors.New("alert id required")
	}
	if _, exists := s.mem[a.ID]; exists {
		return a, ErrDuplicate
	}
	s.mem[a.ID] = a
	s.order = append(s.order, a.ID)
	return a, nil
}

// Get returns the alert by id.
func (s *MemoryStore) Get(id string) (Alert, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.mem[id]
	return a, ok, nil
}

// List returns alerts filtered by status (empty = all).
func (s *MemoryStore) List(status string) ([]Alert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Alert, 0, len(s.order))
	for _, id := range s.order {
		a := s.mem[id]
		if status == "" || a.Status == status {
			out = append(out, a)
		}
	}
	return out, nil
}

// Update overwrites the stored alert.
func (s *MemoryStore) Update(a Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.mem[a.ID]; !ok {
		return ErrNotFound
	}
	s.mem[a.ID] = a
	return nil
}

// ErrDuplicate is returned when an alert with the same id already exists.
var ErrDuplicate = errors.New("alert already exists")