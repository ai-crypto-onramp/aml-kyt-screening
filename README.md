# AML / KYT Screening

![CI](https://github.com/ai-crypto-onramp/aml-kyt-screening/actions/workflows/ci.yml/badge.svg)

Pre-settlement Know-Your-Transaction checks against destination addresses (Chainalysis/TRM); blocks tainted flows before broadcast.

## Overview / Responsibilities

The AML / KYT (Know-Your-Transaction) Screening service is the on-chain compliance gate on the
transaction path. Sitting inside the Transaction Orchestrator saga between payment capture and
MPC signing, it scores the risk of the destination (and, where applicable, source) address
against sanctioned/tainted address databases before any transaction is broadcast to the chain.

Core responsibilities:

- **Pre-settlement address risk scoring** for every on-ramp transaction before broadcast.
- **Destination + source address screening** against vendor exposure data
  (Chainalysis, TRM Labs).
- **Exposure classification** of each screened address into one of
  `sanctioned` / `high_risk` / `unknown` / `clean`.
- **Block / allow decisioning** returned synchronously to the Transaction Orchestrator,
  with `manual_review` as the intermediate path for high-risk flows.
- **Alert generation** for compliance analysts when a screen flags sanctioned or
  high-risk exposure, including async alerts raised from vendor webhooks after
  settlement (e.g. newly identified tainted address).
- **Historical address exposure caching** so repeated screens for the same address
  hit a local cache within its TTL instead of re-paying vendor calls.

The service emits its verdict to the **Policy / Risk Engine**, which aggregates it with
KYC and Fraud signals before the orchestrator proceeds to signing. Every screen is
recorded in the **Audit / Event Log** for compliance forensics.

## Language & Tech Stack

- **Language:** Go (transactional backbone; concurrency, latency, ops maturity).
- **HTTP framework:** standard `net/http` or a thin router (e.g. `chi`) for REST;
  `grpc-go` for the synchronous transaction-path RPC.
- **Vendor SDKs:** official / vendor-supplied Go clients for
  [Chainalysis](https://www.chainalysis.com/) and
  [TRM Labs](https://trmlabs.com/), wrapped behind an internal `ScreenProvider`
  interface so providers are swappable.
- **Storage:** PostgreSQL for durable screen / alert / vendor-response records;
  Redis (or a PG table) for the address-risk cache with TTL.
- **Observability:** OpenTelemetry traces + Prometheus metrics; structured logs (zerolog/slog).

## System Requirements

1. **Pre-settlement address risk scoring**
   Every transaction on the orchestrator saga must be screened against the configured
   vendor(s) before the saga is allowed to proceed to MPC signing. The screen returns
   a numeric `risk_score` (0-100) and an `exposure` classification.

2. **Destination + source address screening**
   Both the withdrawal destination and (for inbound/swap flows) the source address are
   screened. At minimum the destination address is mandatory; source screening is
   enabled per chain / flow type via configuration.

3. **Exposure classification**
   Each screened address is classified as:
   - `sanctioned` — OFAC / SDN match or vendor-flagged sanctioned entity.
   - `high_risk` — vendor risk score above the configured `HIGH_RISK_THRESHOLD`.
   - `unknown` — vendor has no data, or address is below confidence floor.
   - `clean` — vendor-confirmed no exposure.

4. **Block / allow decisions**
   The screen returns a `decision` field consumed by the orchestrator:
   - `block` — sanctioned exposure; the saga must abort with compensation.
   - `manual_review` — high-risk exposure; routed to the compliance queue.
   - `allow` — clean / below thresholds; saga proceeds.
   Thresholds (`BLOCK_THRESHOLD`, `HIGH_RISK_THRESHOLD`) are configurable per chain.

5. **Alert generation**
   On `block` and `manual_review` decisions, an alert record is written to
   `kyt_alerts` and surfaced to the compliance dashboard. Async alerts from
   vendor webhooks (newly identified tainted address) also create alerts and,
   where applicable, trigger downstream review of already-settled transactions.

6. **Historical address exposure caching**
   Successful vendor responses are cached per `(address, chain)` with a TTL
   (`CACHE_TTL_SECONDS`). Subsequent screens within the TTL return the cached
   verdict without a vendor round-trip. Sanctioned verdicts are cached with a
   longer (or non-expiring) TTL and are invalidated only by an explicit webhook
   re-classification.

## Non-Functional Requirements

- **Latency:** synchronous screen on the transaction path must return at
  **p99 < 500ms** (cache hit < 20ms; vendor miss budgeted within the remainder).
- **Idempotency:** vendor calls are idempotent per `(tx_id, address, chain)` —
  repeated orchestrator retries return the same verdict instead of re-querying
  the vendor. Idempotency keys are persisted in `vendor_responses`.
- **Auditability:** every screen — allow, block, or manual_review, cache hit or
  miss — produces an audit event to the Audit / Event Log with the request,
  vendor response (or cache source), decision, and operator/action timestamps.
- **Availability:** vendor outages degrade to `manual_review` with a circuit
  breaker; the service never silently `allow`s when the vendor is unreachable.
- **Data retention:** `kyt_screens` and `vendor_responses` retained per
  regulatory minimum (default 7 years); `kyt_alerts` retained until explicitly
  closed + retention window.

## Technical Specifications

### API Surface

- **REST** — JSON over HTTP/1.1; used by internal dashboards, webhook receivers,
  and the Policy / Risk Engine for verdict aggregation.
- **gRPC** — used by the Transaction Orchestrator on the synchronous transaction
  path for the lowest-latency screen call. The gRPC service mirrors the REST
  `POST /v1/kyt/screen` contract.

### Endpoints

| Method | Path | Body / Params | Response |
|---|---|---|---|
| `POST` | `/v1/kyt/screen` | `{ "address": "...", "chain": "ethereum", "amount": "100.00", "tx_id": "..." }` | `{ "risk_score": 42, "exposure": "high_risk", "decision": "manual_review", "screen_id": "..." }` |
| `GET` | `/v1/kyt/alerts/:id` | `:id` alert ID | `{ "id": "...", "tx_id": "...", "address": "...", "exposure": "...", "status": "open", ... }` |
| `POST` | `/v1/webhooks/chainalysis` | vendor-signed payload | `{ "accepted": true }` |
| `POST` | `/v1/webhooks/trm` | vendor-signed payload | `{ "accepted": true }` |

gRPC service methods (mirroring REST):

- `Screen(ScreenRequest) returns (ScreenResponse)`
- `GetAlert(GetAlertRequest) returns (Alert)`

### Data Model

PostgreSQL tables (simplified):

- **`address_risk_cache`** — `(address, chain) -> risk_score, exposure, decision,
  cached_at, ttl_seconds, source(vendor|cache)`. Hot path lookup; may be backed
  by Redis instead.
- **`kyt_screens`** — `screen_id, tx_id, address, source_address, chain, amount,
  risk_score, exposure, decision, vendor, vendor_response_id, cache_hit,
  created_at`. One row per screen call.
- **`kyt_alerts`** — `id, screen_id, tx_id, address, chain, exposure, severity,
  status(open|in_review|closed), assignee, created_at, closed_at`. One row per
  flagged flow; referenced by the compliance dashboard.
- **`vendor_responses`** — `id, vendor, request_payload, response_payload,
  idempotency_key, address, chain, created_at`. Raw vendor I/O for audit and
  replay.

### Decision Logic

```
sanctioned  -> decision = block
high_risk   -> decision = manual_review   (risk_score >= HIGH_RISK_THRESHOLD)
clean       -> decision = allow
unknown     -> decision = manual_review   (fail-safe; configurable to block)
```

- `BLOCK_THRESHOLD` — exposure at or above this score is treated as `sanctioned`
  and forces `block`.
- `HIGH_RISK_THRESHOLD` — exposure at or above this score (but below block) is
  routed to `manual_review`.
- Vendor-unreachable (circuit breaker open) degrades to `manual_review`; it never
  returns `allow`.
- All thresholds are configurable per chain via environment variables and hot
  reloaded (no restart required).

### Integrations

- **Chainalysis API** — primary KYT vendor; REST over HTTPS with API key auth.
- **TRM Labs API** — secondary / corroborating KYT vendor; REST over HTTPS
  with API key auth.
- **Transaction Orchestrator** — calls `/v1/kyt/screen` (gRPC) inline in the
  saga between Payment capture and MPC signing.
- **Policy / Risk Engine** — aggregates the KYT verdict with KYC and Fraud
  signals as the final gatekeeper before signing.
- **Audit / Event Log** — every screen emits an audit event (async) to the
  platform event bus consumed by the Audit service.

### Webhook Security

Vendor webhooks (`/v1/webhooks/chainalysis`, `/v1/webhooks/trm`) are authenticated
via **HMAC-SHA256** signatures:

- Each vendor has a dedicated webhook secret
  (`CHAINALYSIS_WEBHOOK_SECRET`, `TRM_WEBHOOK_SECRET`).
- The service recomputes `HMAC-SHA256(secret, raw_body)` and compares against
  the vendor-supplied signature header (constant-time compare).
- Mismatched signatures return `401 Unauthorized` and are logged for security
  monitoring. No business logic runs on unsigned requests.

## Dependencies

- **PostgreSQL** — durable store for `kyt_screens`, `kyt_alerts`,
  `vendor_responses`. May also back `address_risk_cache` if Redis is not deployed.
- **Redis** (optional but recommended) — backing store for
  `address_risk_cache` for sub-millisecond cache hits on the hot path.
- **Chainalysis API** — external; outbound HTTPS.
- **TRM Labs API** — external; outbound HTTPS.
- **Audit / Event Log** — async event bus producer (e.g. NATS / Kafka).

## Configuration

All configuration is via environment variables (12-factor). Secrets must be
injected via the platform secret store (Vault / SOPS), not committed.

| Variable | Required | Default | Description |
|---|---|---|---|
| `PORT` | no | `8080` | HTTP REST listen port. |
| `GRPC_PORT` | no | `9090` | gRPC listen port. |
| `DB_URL` | yes | — | PostgreSQL DSN, e.g. `postgres://user:pass@host:5432/kyt?sslmode=require`. |
| `REDIS_URL` | no | — | Redis DSN. If unset, cache falls back to a PG table. |
| `CHAINALYSIS_API_KEY` | yes | — | API key for the Chainalysis KYT API. |
| `CHAINALYSIS_API_URL` | no | `https://api.chainalysis.com` | Chainalysis API base URL. |
| `CHAINALYSIS_WEBHOOK_SECRET` | yes | — | HMAC secret validating Chainalysis webhook signatures. |
| `TRM_API_KEY` | yes | — | API key for the TRM Labs API. |
| `TRM_API_URL` | no | `https://api.trmlabs.com` | TRM Labs API base URL. |
| `TRM_WEBHOOK_SECRET` | yes | — | HMAC secret validating TRM webhook signatures. |
| `CACHE_TTL_SECONDS` | no | `3600` | TTL for cached clean/unknown verdicts. Sanctioned verdicts use `SANCTIONED_CACHE_TTL_SECONDS`. |
| `SANCTIONED_CACHE_TTL_SECONDS` | no | `604800` | TTL for cached sanctioned verdicts (long-lived; 7d default). |
| `HIGH_RISK_THRESHOLD` | no | `50` | Risk score at/above which a screen is routed to `manual_review`. |
| `BLOCK_THRESHOLD` | no | `90` | Risk score at/above which a screen is forced to `block`. |
| `UNKNOWN_DECISION` | no | `manual_review` | Decision returned for `unknown` exposure (`manual_review` or `block`). |
| `VENDOR_TIMEOUT_MS` | no | `800` | Per-vendor HTTP call timeout. |
| `VENDOR_CIRCUIT_BREAKER_THRESHOLD` | no | `5` | Consecutive vendor failures before opening the circuit breaker. |
| `AUDIT_EVENT_BUS_URL` | no | — | NATS/Kafka URL for async audit events. If unset, audit events fall back to the DB. |
| `LOG_LEVEL` | no | `info` | `debug` / `info` / `warn` / `error`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | no | — | OpenTelemetry OTLP collector endpoint. |

## Local Development

```bash
# Build
go build ./...

# Run (requires DB_URL and vendor keys via env / .env)
go run ./cmd/kyt

# Run tests
go test ./...

# Run linter
golangci-lint run

# Generate gRPC stubs
buf generate
```

Service-specific dev scripts (docker-compose, seed data, mock vendor) — TODO.