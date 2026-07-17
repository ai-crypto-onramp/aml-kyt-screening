package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/lib/pq"
)

// isInvalidUUID reports whether err is a Postgres invalid_text_representation
// error (SQLSTATE 22P02), which is raised when a non-UUID string is sent to a
// UUID-typed column. Callers treat this as "no matching row" (ErrNotFound)
// rather than surfacing it as an internal 500.
func isInvalidUUID(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "22P02"
	}
	return false
}

// PGAlertStore is a Postgres-backed implementation of alert.Store. It persists
// kyt_alerts rows (created by migration 0003).
type PGAlertStore struct {
	db *sql.DB
}

// NewPGAlertStore returns a PGAlertStore backed by db.
func NewPGAlertStore(db *sql.DB) *PGAlertStore {
	return &PGAlertStore{db: db}
}

// Create inserts a new alert row. Returns ErrDuplicate on a primary-key conflict.
func (s *PGAlertStore) Create(a alert.Alert) (alert.Alert, error) {
	if a.ID == "" {
		return a, errors.New("alert id required")
	}
	var screenID any
	if a.ScreenID != "" {
		screenID = a.ScreenID
	}
	var assignee any
	if a.Assignee != "" {
		assignee = a.Assignee
	}
	var closedAt any
	if a.ClosedAt != nil {
		closedAt = *a.ClosedAt
	}
	res, err := s.db.Exec(`
INSERT INTO kyt_alerts
  (id, screen_id, tx_id, address, chain, exposure, severity, status, assignee, created_at, closed_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (id) DO NOTHING`,
		a.ID, screenID, a.TxID, a.Address, a.Chain, a.Exposure, a.Severity, a.Status,
		assignee, a.CreatedAt, closedAt,
	)
	if err != nil {
		return a, fmt.Errorf("pgalert create: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return a, fmt.Errorf("pgalert create rows: %w", err)
	}
	if n == 0 {
		return a, alert.ErrDuplicate
	}
	return a, nil
}

// Get returns the alert by id.
func (s *PGAlertStore) Get(id string) (alert.Alert, bool, error) {
	row := s.db.QueryRow(`
SELECT id, COALESCE(screen_id::text,''), tx_id, address, chain, exposure, severity, status, COALESCE(assignee,''), created_at, closed_at
  FROM kyt_alerts
 WHERE id = $1`, id)
	a, err := scanAlert(row)
	if err == sql.ErrNoRows {
		return alert.Alert{}, false, nil
	}
	if err != nil {
		if isInvalidUUID(err) {
			return alert.Alert{}, false, nil
		}
		return alert.Alert{}, false, fmt.Errorf("pgalert get: %w", err)
	}
	return a, true, nil
}

// List returns alerts filtered by status (empty = all), ordered by created_at.
func (s *PGAlertStore) List(status string) ([]alert.Alert, error) {
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = s.db.Query(`
SELECT id, COALESCE(screen_id::text,''), tx_id, address, chain, exposure, severity, status, COALESCE(assignee,''), created_at, closed_at
  FROM kyt_alerts
 ORDER BY created_at ASC`)
	} else {
		rows, err = s.db.Query(`
SELECT id, COALESCE(screen_id::text,''), tx_id, address, chain, exposure, severity, status, COALESCE(assignee,''), created_at, closed_at
  FROM kyt_alerts
 WHERE status = $1
 ORDER BY created_at ASC`, status)
	}
	if err != nil {
		return nil, fmt.Errorf("pgalert list: %w", err)
	}
	defer rows.Close()
	var out []alert.Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, fmt.Errorf("pgalert list scan: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgalert list rows: %w", err)
	}
	return out, nil
}

// Update overwrites the stored alert.
func (s *PGAlertStore) Update(a alert.Alert) error {
	var screenID any
	if a.ScreenID != "" {
		screenID = a.ScreenID
	}
	var assignee any
	if a.Assignee != "" {
		assignee = a.Assignee
	}
	var closedAt any
	if a.ClosedAt != nil {
		closedAt = *a.ClosedAt
	}
	res, err := s.db.Exec(`
UPDATE kyt_alerts
   SET screen_id = $2, tx_id = $3, address = $4, chain = $5, exposure = $6, severity = $7, status = $8, assignee = $9, closed_at = $10, updated_at = now()
 WHERE id = $1`,
		a.ID, screenID, a.TxID, a.Address, a.Chain, a.Exposure, a.Severity, a.Status, assignee, closedAt,
	)
	if err != nil {
		if isInvalidUUID(err) {
			return alert.ErrNotFound
		}
		return fmt.Errorf("pgalert update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("pgalert update rows: %w", err)
	}
	if n == 0 {
		return alert.ErrNotFound
	}
	return nil
}

// scanner is the common interface of sql.Row and sql.Rows Scan methods.
type scanner interface {
	Scan(dest ...any) error
}

func scanAlert(sc scanner) (alert.Alert, error) {
	var a alert.Alert
	var screenID, assignee string
	var closedAt sql.NullTime
	err := sc.Scan(
		&a.ID, &screenID, &a.TxID, &a.Address, &a.Chain, &a.Exposure, &a.Severity,
		&a.Status, &assignee, &a.CreatedAt, &closedAt,
	)
	if err != nil {
		return alert.Alert{}, err
	}
	a.ScreenID = screenID
	a.Assignee = assignee
	if closedAt.Valid {
		t := closedAt.Time
		a.ClosedAt = &t
	}
	return a, nil
}