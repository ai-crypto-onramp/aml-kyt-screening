package audit

import (
	"context"
	"database/sql"
	"fmt"
)

// DBSink appends audit events to an audit_events table. It is the DB fallback
// used when AUDIT_EVENT_BUS_URL is unset: events remain durable across process
// restarts because they are persisted in PostgreSQL.
type DBSink struct {
	db *sql.DB
}

// NewDBSink returns a DBSink backed by db. The audit_events table must exist
// (created by migration 0005).
func NewDBSink(db *sql.DB) *DBSink {
	return &DBSink{db: db}
}

// Emit inserts e into the audit_events table.
func (s *DBSink) Emit(ctx context.Context, e Event) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO audit_events
  (screen_id, tx_id, address, chain, amount, decision, exposure, risk_score, vendor, cache_hit, source, operator, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		e.ScreenID, e.TxID, e.Address, e.Chain, e.Amount, e.Decision, e.Exposure,
		e.RiskScore, e.Vendor, e.CacheHit, e.Source, e.Operator, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("db audit emit: %w", err)
	}
	return nil
}

// Close is a no-op; the caller owns the *sql.DB.
func (s *DBSink) Close() error { return nil }