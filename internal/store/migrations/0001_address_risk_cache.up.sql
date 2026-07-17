-- Conventions: UUID PKs (app-generated UUIDv7, no DB default), UPPER_CASE enum
-- TEXT (no CHECK), created_at + updated_at on every table, no DB triggers.
CREATE TABLE IF NOT EXISTS address_risk_cache (
    id           UUID PRIMARY KEY,
    address      TEXT        NOT NULL,
    chain        TEXT        NOT NULL,
    risk_score   INTEGER     NOT NULL,
    exposure     TEXT        NOT NULL,
    decision     TEXT        NOT NULL,
    vendor       TEXT        NOT NULL,
    cached_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    ttl_seconds  INTEGER     NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (address, chain)
);

CREATE INDEX IF NOT EXISTS idx_address_risk_cache_expires_at
    ON address_risk_cache (expires_at);

-- The (address, chain) lookup is served by the UNIQUE constraint; no separate index needed.