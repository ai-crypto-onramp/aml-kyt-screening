package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
)

// PGScreenStore is a Postgres-backed implementation of screen.ScreenStore. It
// persists kyt_screens rows (created by migration 0002). Screen ids are UUIDs
// stored as TEXT and normalized to dashed form on read.
type PGScreenStore struct {
	db *sql.DB
}

// NewPGScreenStore returns a PGScreenStore backed by db.
func NewPGScreenStore(db *sql.DB) *PGScreenStore {
	return &PGScreenStore{db: db}
}

// Put inserts or replaces a kyt_screens row.
func (s *PGScreenStore) Put(rec screen.ScreenRecord) error {
	if rec.ScreenID == "" {
		return errors.New("screen id required")
	}
	var vendorResponseID any
	if rec.VendorResponseID != "" {
		vendorResponseID = rec.VendorResponseID
	}
	var sourceAddress any
	if rec.SourceAddress != "" {
		sourceAddress = rec.SourceAddress
	}
	_, err := s.db.Exec(`
INSERT INTO kyt_screens
  (screen_id, tx_id, address, source_address, chain, amount, risk_score, exposure, decision, vendor, vendor_response_id, cache_hit, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (screen_id) DO UPDATE SET
  tx_id             = EXCLUDED.tx_id,
  address           = EXCLUDED.address,
  source_address    = EXCLUDED.source_address,
  chain             = EXCLUDED.chain,
  amount            = EXCLUDED.amount,
  risk_score        = EXCLUDED.risk_score,
  exposure          = EXCLUDED.exposure,
  decision          = EXCLUDED.decision,
  vendor            = EXCLUDED.vendor,
  vendor_response_id = EXCLUDED.vendor_response_id,
  cache_hit         = EXCLUDED.cache_hit,
  created_at        = EXCLUDED.created_at`,
		rec.ScreenID, rec.TxID, rec.Address, sourceAddress, rec.Chain, rec.Amount,
		rec.RiskScore, rec.Exposure, rec.Decision, rec.Vendor, vendorResponseID,
		rec.CacheHit, rec.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("pgscreen put: %w", err)
	}
	return nil
}

// Get returns the screen record by id.
func (s *PGScreenStore) Get(id string) (screen.ScreenRecord, bool, error) {
	row := s.db.QueryRow(`
SELECT screen_id, tx_id, address, COALESCE(source_address,''), chain, amount::text, risk_score, exposure, decision, vendor, COALESCE(vendor_response_id::text,''), cache_hit, created_at
  FROM kyt_screens
 WHERE screen_id = $1`, id)
	var rec screen.ScreenRecord
	var vendorResponseID string
	err := row.Scan(
		&rec.ScreenID, &rec.TxID, &rec.Address, &rec.SourceAddress, &rec.Chain, &rec.Amount,
		&rec.RiskScore, &rec.Exposure, &rec.Decision, &rec.Vendor, &vendorResponseID,
		&rec.CacheHit, &rec.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return screen.ScreenRecord{}, false, nil
	}
	if err != nil {
		return screen.ScreenRecord{}, false, fmt.Errorf("pgscreen get: %w", err)
	}
	rec.VendorResponseID = vendorResponseID
	return rec, true, nil
}