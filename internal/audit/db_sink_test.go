package audit

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func skipIfNoDB(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		t.Skip("DB_URL not set; skipping live Postgres audit DBSink test")
	}
	return dsn
}

func TestDBSinkEmitsAndPersists(t *testing.T) {
	dsn := skipIfNoDB(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	// Ensure migrations are applied (the audit_events table is created by
	// migration 0005). We run a quick CREATE TABLE IF NOT EXISTS to be safe
	// in case the test DB has not run the latest migration.
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS audit_events (
    id           BIGSERIAL    PRIMARY KEY,
    screen_id    TEXT,
    tx_id        TEXT,
    address      TEXT,
    chain        TEXT,
    amount       TEXT,
    decision     TEXT         NOT NULL,
    exposure     TEXT,
    risk_score   INTEGER,
    vendor       TEXT,
    cache_hit    BOOLEAN      NOT NULL DEFAULT FALSE,
    source       TEXT         NOT NULL,
    operator     TEXT,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatalf("ensure audit_events: %v", err)
	}

	sink := NewDBSink(db)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	ev := Event{
		ScreenID:  "scr-dbsink-1",
		TxID:      "tx-dbsink-1",
		Address:   "0xdbsink",
		Chain:     "ethereum",
		Amount:    "100",
		Decision:  "block",
		Exposure:  "sanctioned",
		RiskScore: 99,
		Vendor:    "chainalysis",
		CacheHit:  false,
		Source:    "vendor",
		CreatedAt: now,
	}
	// Clean any prior row so the test is idempotent on a shared DB.
	if _, err := db.Exec(`DELETE FROM audit_events WHERE screen_id = $1`, ev.ScreenID); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if err := sink.Emit(context.Background(), ev); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var gotScreenID, gotDecision, gotSource string
	var gotRiskScore int
	err = db.QueryRow(`
SELECT screen_id, decision, source, risk_score
  FROM audit_events
 WHERE screen_id = $1
 ORDER BY id DESC LIMIT 1`, ev.ScreenID).Scan(&gotScreenID, &gotDecision, &gotSource, &gotRiskScore)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if gotScreenID != ev.ScreenID || gotDecision != ev.Decision || gotSource != ev.Source || gotRiskScore != ev.RiskScore {
		t.Errorf("persisted row mismatch: screen=%s decision=%s source=%s score=%d", gotScreenID, gotDecision, gotSource, gotRiskScore)
	}
	if _, err := db.Exec(`DELETE FROM audit_events WHERE screen_id = $1`, ev.ScreenID); err != nil {
		t.Fatalf("clean: %v", err)
	}
}