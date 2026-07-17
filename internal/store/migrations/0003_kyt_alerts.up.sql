-- Conventions: UUID PKs (app-generated UUIDv7, no DB default), UPPER_CASE enum
-- TEXT (no CHECK), created_at + updated_at on every table, no DB triggers.
CREATE TABLE IF NOT EXISTS kyt_alerts (
    id          UUID         PRIMARY KEY,
    screen_id   UUID,
    tx_id       TEXT         NOT NULL,
    address     TEXT         NOT NULL,
    chain       TEXT         NOT NULL,
    exposure    TEXT         NOT NULL,
    severity    TEXT         NOT NULL,
    status      TEXT         NOT NULL DEFAULT 'OPEN',
    assignee    TEXT,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    closed_at   TIMESTAMPTZ,
    CONSTRAINT kyt_alerts_screen_fk
        FOREIGN KEY (screen_id) REFERENCES kyt_screens (screen_id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_kyt_alerts_status
    ON kyt_alerts (status);

CREATE INDEX IF NOT EXISTS idx_kyt_alerts_tx_id
    ON kyt_alerts (tx_id);

CREATE INDEX IF NOT EXISTS idx_kyt_alerts_address_chain
    ON kyt_alerts (address, chain);