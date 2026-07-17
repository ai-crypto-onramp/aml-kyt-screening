-- Conventions: UUID PKs (app-generated UUIDv7, no DB default), UPPER_CASE enum
-- TEXT (no CHECK), created_at + updated_at on every table, no DB triggers.
CREATE TABLE IF NOT EXISTS kyt_screens (
    screen_id          UUID         PRIMARY KEY,
    tx_id              TEXT         NOT NULL,
    address            TEXT         NOT NULL,
    source_address     TEXT,
    chain              TEXT         NOT NULL,
    amount             NUMERIC(20, 8) NOT NULL,
    risk_score         INTEGER      NOT NULL,
    exposure           TEXT         NOT NULL,
    decision           TEXT         NOT NULL,
    vendor             TEXT         NOT NULL,
    vendor_response_id UUID,
    cache_hit          BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_kyt_screens_tx_id
    ON kyt_screens (tx_id);

CREATE INDEX IF NOT EXISTS idx_kyt_screens_address_chain
    ON kyt_screens (address, chain);

CREATE INDEX IF NOT EXISTS idx_kyt_screens_created_at
    ON kyt_screens (created_at);