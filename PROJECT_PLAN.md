# Project Plan — AML / KYT Screening

This plan breaks the AML / KYT Screening service into logically ordered implementation
stages, derived from the requirements in `README.md`. Each stage is independently
mergeable and maps to a tracking GitHub issue. Stages are ordered so that foundational
pieces (schema, cache) land first, then vendor integration, then the synchronous screen
endpoint, decisioning, async ingestion, alerting, audit, observability, and finally
tests/coverage and Docker/CI polish.

## Stage 1 — Database Schema & Address Risk Cache

**Goal:** Stand up the PostgreSQL schema for `address_risk_cache`, `kyt_screens`,
`kyt_alerts`, and `vendor_responses`, plus a TTL-backed cache abstraction that the
hot screen path will use to avoid repeated vendor calls.

**Tasks:**
- [x] Add migration files for `address_risk_cache`, `kyt_screens`, `kyt_alerts`,
      `vendor_responses` (UUID PKs, indexes on `(address, chain)`, `tx_id`,
      `idempotency_key`).
- [x] Implement a Go migration runner (e.g. `golang-migrate` or `goose`) wired into
      `cmd/kyt` startup.
- [x] Implement `Cache` interface with Redis and PG-backed implementations; select
      implementation via `REDIS_URL`.
- [x] Implement cache get/set with TTL honoring `CACHE_TTL_SECONDS` and
      `SANCTIONED_CACHE_TTL_SECONDS` for sanctioned verdicts.
- [x] Add connection pooling / health check for PG and Redis.

**Acceptance criteria:**
- `go test ./internal/store/...` passes against a ephemeral Postgres/Redis.
- Cache hit returns cached verdict within p99 < 20ms.
- Sanctioned entries use the longer TTL; clean/unknown use the default TTL.

## Stage 2 — Vendor Integration (Chainalysis / TRM)

**Goal:** Wrap Chainalysis and TRM Labs KYT APIs behind a swappable
`ScreenProvider` interface with circuit breaker, timeouts, and idempotency keys.

**Tasks:**
- [x] Define `ScreenProvider` interface: `Screen(ctx, ScreenRequest) (ScreenResponse, error)`.
- [x] Implement `ChainalysisProvider` (REST/HTTPS, API key auth, `CHAINALYSIS_API_URL`).
- [x] Implement `TRMProvider` (REST/HTTPS, API key auth, `TRM_API_URL`).
- [x] Add `VENDOR_TIMEOUT_MS` per-call timeout via `http.Client`.
- [x] Add circuit breaker (gobreaker/sony-gobreaker) honoring
      `VENDOR_CIRCUIT_BREAKER_THRESHOLD`; open circuit degrades to `manual_review`.
- [x] Persist raw request/response to `vendor_responses` with idempotency key
      `(tx_id, address, chain)`.
- [x] Add a mock provider for tests.

**Acceptance criteria:**
- Both providers return `risk_score` and `exposure` for sample addresses.
- Repeated calls with same idempotency key return cached response (no double vendor
  billing).
- Circuit breaker opens after N consecutive failures; no `allow` decision is returned
  while open.

## Stage 3 — Screen Endpoint (REST + gRPC)

**Goal:** Implement the synchronous screen path `POST /v1/kyt/screen` (REST) and
the mirroring `Screen` gRPC method, used by the Transaction Orchestrator on the
transaction path.

**Tasks:**
- [x] Define gRPC proto for `Screen(ScreenRequest) returns (ScreenResponse)` and
      `GetAlert(GetAlertRequest) returns (Alert)`; generate stubs via `buf generate`.
- [x] Implement REST handler with JSON validation (address, chain, amount, tx_id).
- [x] Implement gRPC server mirroring the REST contract.
- [x] Wire handler to cache lookup -> vendor screen -> decision logic -> persist
      `kyt_screens` row.
- [x] Return `screen_id`, `risk_score`, `exposure`, `decision`.
- [x] Add request ID / trace propagation (OpenTelemetry context).

**Acceptance criteria:**
- `POST /v1/kyt/screen` returns 200 with the documented JSON body.
- gRPC `Screen` returns the same verdict for the same request.
- p99 < 500ms on cache miss (mocked vendor), < 20ms on cache hit.

## Stage 4 — Decision Logic (Block / Review / Allow)

**Goal:** Centralize the exposure -> decision mapping and per-chain threshold
evaluation, including the fail-safe behavior for `unknown` and vendor-unreachable.

**Tasks:**
- [x] Implement `DecisionEngine` mapping `exposure` -> `decision` per the spec:
      `sanctioned -> block`, `high_risk -> manual_review`, `clean -> allow`,
      `unknown -> UNKNOWN_DECISION`.
- [x] Apply `BLOCK_THRESHOLD` and `HIGH_RISK_THRESHOLD` (per-chain override)
      to derive exposure when the vendor returns a numeric score only.
- [x] Hot-reload thresholds from environment without restart.
- [x] Vendor-unreachable / circuit-open path returns `manual_review` (never `allow`).

**Acceptance criteria:**
- Unit tests cover all four exposure branches and threshold edge cases.
- Thresholds can be changed via env at runtime and take effect on next request.
- No code path returns `allow` when vendor is unreachable.

## Stage 5 — Webhook Ingestion & HMAC Verification

**Goal:** Accept vendor webhooks (`/v1/webhooks/chainalysis`,
`/v1/webhooks/trm`) with HMAC-SHA256 signature verification and process
re-classifications (e.g. newly tainted address) into alerts and cache invalidation.

**Tasks:**
- [x] Implement `POST /v1/webhooks/chainalysis` and `POST /v1/webhooks/trm` handlers.
- [x] Verify `HMAC-SHA256(secret, raw_body)` against vendor signature header using
      `crypto/subtle.ConstantTimeCompare`; return 401 on mismatch.
- [x] Parse vendor payload, create/update `kyt_alerts` for new exposures.
- [x] Invalidate `address_risk_cache` entries for re-classified addresses.
- [x] Trigger downstream review of already-settled transactions for the affected
      address (best-effort; async).
- [x] Log signature mismatches for security monitoring.

**Acceptance criteria:**
- Unsigned / tampered payloads return 401 and produce no business side effects.
- Valid re-classification webhook creates an alert and invalidates the cache entry.
- Replay of the same webhook is idempotent (no duplicate alerts).

## Stage 6 — Alerting

**Goal:** Surface `block` and `manual_review` decisions (and async webhook
re-classifications) as `kyt_alerts` rows consumable by the compliance dashboard.

**Tasks:**
- [x] Implement `AlertService.Create` writing to `kyt_alerts` with `status=open`.
- [x] Implement `GET /v1/kyt/alerts/:id` returning the alert payload.
- [x] Implement gRPC `GetAlert` mirror.
- [x] Assign default severity from exposure (`sanctioned` -> `critical`,
      `high_risk` -> `high`).
- [x] Expose list/assign/close operations for the compliance dashboard.

**Acceptance criteria:**
- A `block` or `manual_review` screen produces exactly one open alert.
- `GET /v1/kyt/alerts/:id` returns the full alert JSON.
- Closing an alert records `closed_at` and `assignee`.

## Stage 7 — Audit Emission

**Goal:** Emit an audit event for every screen (allow/block/review, cache hit or
miss) to the Audit / Event Log via the async event bus (NATS/Kafka), with DB
fallback when `AUDIT_EVENT_BUS_URL` is unset.

**Tasks:**
- [x] Define audit event schema (request, vendor response or cache source,
      decision, operator/action timestamps, `screen_id`).
- [x] Implement async producer to NATS/Kafka when `AUDIT_EVENT_BUS_URL` is set.
- [x] Implement DB fallback (append to an `audit_events` table) when bus is unset.
- [x] Ensure audit emission never blocks the screen path (bounded queue + drop
      counter metric on overflow).

**Acceptance criteria:**
- Every screen call produces exactly one audit event.
- Events are durable: present in bus or DB fallback after process restart.
- Auditing failures do not break the screen path; drops are observable via metric.

## Stage 8 — Observability (Traces, Metrics, Logs)

**Goal:** Add OpenTelemetry traces, Prometheus metrics, and structured logs
covering the hot path, vendor calls, cache, webhooks, and audit emission.

**Tasks:**
- [x] Instrument REST and gRPC handlers with OTel spans; propagate trace context
      to vendor calls.
- [x] Add Prometheus metrics: `kyt_screen_duration_seconds`, `kyt_screen_total`,
      `kyt_cache_hits_total`, `kyt_cache_misses_total`, `kyt_vendor_errors_total`,
      `kyt_circuit_breaker_state`, `kyt_alerts_open`, `kyt_audit_drops_total`.
- [x] Switch to structured logging (`slog`/`zerolog`) at `LOG_LEVEL`.
- [x] Expose `/metrics` for Prometheus scrape.

**Acceptance criteria:**
- A screen call produces a trace spanning handler -> cache -> vendor -> decision.
- Key metrics are exported on `/metrics` and align with the listed names.
- Log level is configurable via `LOG_LEVEL` and applies to all components.

## Stage 9 — Tests & Coverage

**Goal:** Reach and enforce a high coverage bar across the service with unit,
integration, and contract tests; wire coverage reporting into CI (Codecov).

**Tasks:**
- [x] Unit tests for cache, decision engine, providers (mock), HMAC verification.
- [x] Integration tests for REST + gRPC endpoints against ephemeral PG/Redis.
- [x] Webhook contract tests with sample signed payloads (valid + tampered).
- [x] Add `go test -race ./...` to CI.
- [x] Upload coverage to Codecov; enforce minimum threshold (e.g. 80%).

**Acceptance criteria:**
- `go test -race ./...` is green.
- Coverage report uploaded to Codecov on every CI run.
- Coverage threshold gate fails CI below the configured minimum.

## Stage 10 — Docker & CI

**Goal:** Produce a production-grade container image and CI pipeline that builds,
lints, tests, and publishes the service; integrate with the existing
`ci.yml` workflow.

**Tasks:**
- [x] Refine `Dockerfile` (multi-stage, distroless/non-root, healthcheck).
- [x] Add `docker-compose.yml` for local dev (postgres, redis, mock vendor).
- [x] Wire CI: `go build`, `golangci-lint run`, `go test -race`, `buf generate`
      check, coverage upload.
- [x] Add release workflow that builds and publishes the image on tag.
- [x] Document local dev workflow in README.

**Acceptance criteria:**
- `docker compose up` brings the service up healthy against local deps.
- CI is green on main and PRs; image published on tagged release.
- README `Local Development` section is complete (no TODOs).