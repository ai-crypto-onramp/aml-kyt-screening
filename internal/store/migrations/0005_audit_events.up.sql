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
);

CREATE INDEX IF NOT EXISTS idx_audit_events_screen_id
    ON audit_events (screen_id);

CREATE INDEX IF NOT EXISTS idx_audit_events_tx_id
    ON audit_events (tx_id);

CREATE INDEX IF NOT EXISTS idx_audit_events_created_at
    ON audit_events (created_at);