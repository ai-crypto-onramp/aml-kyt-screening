CREATE TABLE IF NOT EXISTS vendor_responses (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    vendor            TEXT         NOT NULL,
    request_payload   JSONB        NOT NULL,
    response_payload  JSONB        NOT NULL,
    idempotency_key   TEXT         NOT NULL,
    address           TEXT         NOT NULL,
    chain             TEXT         NOT NULL,
    tx_id             TEXT         NOT NULL,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_vendor_responses_idempotency_key
    ON vendor_responses (idempotency_key);

CREATE INDEX IF NOT EXISTS idx_vendor_responses_tx_id
    ON vendor_responses (tx_id);

CREATE INDEX IF NOT EXISTS idx_vendor_responses_address_chain
    ON vendor_responses (address, chain);