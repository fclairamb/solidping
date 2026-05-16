# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Core technologies
- **Backend**: Go 1.24+ (see server/CLAUDE.md for details)
- **Dashboard**: (see web/dash0/CLAUDE.md for details), do not use `web/dash` for current developments
- **Infrastructure**: Docker Compose with PostgreSQL for monitoring data storage
- **Monitoring**: Multi-protocol ping/health checking with distributed worker system

## Common commands

### Infrastructure
- **Start services**: `docker-compose up -d` (PostgreSQL)
- **Build Docker image**: `make docker-build`

### Backend
See server/CLAUDE.md for backend-specific commands

### Dashboard
See web/dash0/CLAUDE.md for dashboard-specific commands

### Database
- **Reset SQLite database**: Delete the `solidping.db` file or use `SP_DB_RESET=true` environment variable to reset on startup

## Development Workflow
If the server is currently running on port 4000, you can just apply code changes
and test them. The `cmd/devloop` watcher (used by `make dev` / `make dev-test`)
builds the new binary first and only signals the running process to exit once
the build succeeds, so the API stays up across reloads — bounded by graceful
shutdown (sub-second) rather than build time. A failed build leaves the
previous binary running; check the dev log for the compiler error.

1. Start infrastructure: `docker-compose up -d`
2. Run everything: `make dev` (backend + dash0 + status0 with hot reload)
3. Or in test mode: `make dev-test` (same but with `SP_RUNMODE=test`)
4. Database changes: Add migrations, run `make migrate`

## Common Makefile Targets
- **Build**:
  - `make build` - Build complete application (dash + dash0 + status0 + backend)
  - `make build-dash` - Build dash only (using bun)
  - `make build-dash0` - Build dash0 status page only (using bun)
  - `make build-status0` - Build status0 public status page only (using bun)
  - `make build-backend` - Build backend only (Go binary)
  - `make build-cli` - Build standalone CLI (`sp`) binary
  - `make install-cli` - Install standalone CLI to GOPATH
  - `make docker-build` - Build Docker image
- **Development**:
  - `make dev` - Run backend, dash0 and status0 in development mode
  - `make dev-test` - Run backend, dash0 and status0 in development test mode
  - `make dev-backend` - Start backend development server only
  - `make dev-dash` - Start dash development server only
  - `make dev-dash0` - Start dash0 development server only
  - `make dev-status0` - Start status0 development server only
- **Run**:
  - `make run` - Build and run the application
  - `make run-test` - Build and run the application in test mode
- **Testing**:
  - `make test` - Run backend tests
  - `make test-dash` - Run dash tests
- **Linting**:
  - `make lint` - Run all linters (backend + dash)
  - `make lint-back` - Run backend linter (golangci-lint)
  - `make lint-dash` - Run dash linter
  - `make fmt` - Format all code (backend + dash)
- **Other**:
  - `make deps` - Install all dependencies
  - `make migrate` - Run database migrations
  - `make clean` - Remove built binaries and artifacts
  - `make clean-all` - Remove all generated files including node_modules
- **Bench / load**:
  - `make build-loadgen` - Build the loadgen client (`./bin/loadgen`)
  - `make bench-checks-sqlite` - Run loadgen against a SQLite-backed test server, write report to `bench-results/`
  - `make bench-checks-postgres` - Same, against an embedded PostgreSQL
  - `make bench-checks` - Both, sequentially
  - Knobs: `BENCH_CHECKS`, `BENCH_DURATION`, `BENCH_PERIOD`, `BENCH_PORT`, `BENCH_PG_PORT` (e.g. `make bench-checks BENCH_CHECKS=500 BENCH_DURATION=5m`)

## Deployment mode
- `SP_DEPLOYMENT_MODE` — `self-hosted` (default) or `saas`. Drives the per-org entitlement defaults: `self-hosted` caps `MaxSSOUsers` at 30; `saas` caps `MaxChecksPerMinute` at 6. Anything else is unlimited. The two limits are the only fields enforced — `MaxSSOUsers` at every OAuth callback (refusing the 31st new SSO membership for an org), `MaxChecksPerMinute` at the worker dispatch (rate-limited executions are skipped + counted in `solidping_checks_rate_limited_total`). Admins can override either cap per-org via `PUT/PATCH /api/v1/orgs/$org/entitlements`.

## Observability surfaces (independently toggleable)
Each of the three telemetry surfaces is opt-out / opt-in via its own config knob; metric **collection** keeps running regardless, only the **export surface** is gated. The collected histograms (DB pool, query duration, check stages, HTTP per-route, claim outcomes, busy-retry counters) sit in package-level memory at near-zero cost, so leaving them on while the export endpoint is disabled is safe.
- `SP_PROMETHEUS_ENABLED` — defaults `true`. Gates `/metrics` HTTP handler registration; path is `SP_PROMETHEUS_PATH` (default `/metrics`). Set `false` in regulated deployments that scrape metrics out-of-band.
- `SP_PROFILER_ENABLED` — defaults `false`. When true, mounts `net/http/pprof` on a dedicated listener (`SP_PROFILER_LISTEN`, default `localhost:6060`). Use for CPU/heap profiling under load.
- `SP_OTEL_ENABLED` — defaults `false`. When true, the OpenTelemetry SDK initializes a tracer/meter/logger provider and exports via OTLP (`SP_OTEL_ENDPOINT`, `SP_OTEL_PROTOCOL=http|grpc`, `SP_OTEL_INSECURE`). The check worker already emits a `check.execute` span per job ([server/internal/checkworker/worker.go:425](server/internal/checkworker/worker.go:425)); broader auto-instrumentation is opt-in via this flag.

### Metric families (key ones for performance debugging)
- `solidping_db_pool_*{backend}` — `sql.DB.Stats()` snapshot at scrape time (open/in_use/idle, wait_count, wait_duration). The single most important number on SQLite is `wait_duration_seconds_total` — non-zero ⇒ the pool is the bottleneck.
- `solidping_db_query_duration_seconds{operation,backend,status}` — histogram per SQL verb (SELECT / INSERT / UPDATE / DELETE / BEGIN / COMMIT). Emitted from the Bun sloghook.
- `solidping_db_busy_retries_total{backend}` — SQLITE_BUSY / PG serialization-failure errors. Non-zero rate = write contention.
- `solidping_check_stage_duration_seconds{stage}` — per-stage timing inside `executeJob`. Stages: `fetch`, `claim`, `execute`, `save_result`, `process_incident`, `release_lease`.
- `solidping_http_request_duration_seconds{method,route,status}` — per-route latency, keyed by the matched bunrouter pattern (low cardinality).
- `solidping_claim_jobs_result_total{outcome}` — outcome of each ClaimJobs call: `jobs`, `empty`, `lock_conflict`, `error`. Distinguishes "no due jobs" from "due jobs were locked".
- All existing product metrics (`solidping_check_executions_total`, `solidping_check_duration_seconds`, `solidping_check_scheduling_delay_seconds`, rate-limit counters) are unchanged.

## HTTP rate limiting (per-IP)
- Two independent middlewares protect every route on `mainGroup`, with `/api/v1/workers/`, `/api/v1/heartbeat/`, `/api/mgmt/health`, and `/metrics` excluded by prefix. Over-limit responses are 429 immediately (no queuing); rate-limited responses include `Retry-After: 60`.
- `SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE` — token-bucket refill per client IP (default `300`). Set `0` to disable the rate limiter.
- `SP_SERVER_RATE_LIMITING_BURST` — instantaneous burst above the sustained rate (default `60`).
- `SP_SERVER_RATE_LIMITING_MAX_CONCURRENT` — semaphore size for in-flight requests per IP (default `20`). Set `0` to disable the concurrency limiter.
- `SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES` — number of trusted reverse-proxy hops (default `0` = use `RemoteAddr` directly). Set `1` behind a single nginx/ingress that sets `X-Forwarded-For`; the middleware strips the last N hops to find the real client IP. Trusting the header without a configured proxy count is an IP-spoofing vector.
- 429 counts are exposed as `solidping_http_rate_limited_total{reason="rate"|"concurrency"}` for operator alerting.

## Credentials encryption at rest
- `SP_ENCRYPTION_MASTER_KEY` — base64-encoded 32-byte KEK. When set, secret keys in `checks.config`, `integration_connections.settings`, and `check_jobs.config` are split into a public column and an AES-256-GCM-encrypted private column (`*_private`). The dashboard never echoes secret values back; it gets a `configPrivateKeys: [...]` hint instead.
- `SP_ENCRYPTION_MASTER_KEY_FILE` — file path containing the base64 key. Wins over the env var when both are set (k8s secret-mount pattern).
- `SP_ENCRYPTION_AUTO_MIGRATE` — defaults to `true`. Encrypts any pre-existing plaintext secrets on startup. Set `false` to opt out and run `./solidping encrypt-credentials [--dry-run]` manually.
- **Threat model caveat:** encryption-at-rest only protects against database theft. It does not protect against a compromised server process, a malicious admin, a worker leaking credentials in its logs, or an over-permissive RBAC config. The fallback when no master key is set is plaintext (V1) — safe default for self-hosted, called out in startup logs.

## Default credentials
- User: `admin@solidping.com`
- Pass: `solidpass`
- Org: `default`

## Test mode credentials (SP_RUNMODE=test)
- User: `test@test.com`
- Pass: `test`
- Org: `test`

## REST API choices
- Never return an array directly. It should always be inside another element like `data`.
- Always use $uid in paths
- Use `q` for search parameters
- Use `PATCH` for all APIs allowing updates
- Use camelCase consistently for both JSON properties and query parameters (e.g., `checkUid` in JSON and `?checkUid=abc` in URLs)
- When using query parameters that can contain multiple values, use them in their singular form, for example `checkUid` and not `checkUids`. If there are multiple values, separate them with `,`.
- The page-size query parameter must be named `limit`. Default and max values are per-endpoint; the name is not. Use `base.ParsePageLimit(query, def, max)` from `server/internal/handlers/base/pagination.go` so legacy `?size=` clients keep working during the deprecation window.

### API Endpoints (key routes, see `docs/api-specification.md` for full list)
- GET /api/mgmt/version - Version info
- GET /api/mgmt/health - Health check
- POST /api/mgmt/report - In-app bug report (multipart, public)
- GET /api/v1/features - Frontend feature flags (auth)
- POST /api/v1/auth/login - Login (org optional in body)
- POST /api/v1/auth/logout - Logout
- POST /api/v1/auth/refresh - Refresh token
- GET /api/v1/auth/me - Current user info
- PATCH /api/v1/auth/me - Update current user
- POST /api/v1/auth/switch-org - Switch organization
- POST /api/v1/auth/register - Register new user
- POST /api/v1/auth/2fa/setup|confirm|verify|recovery - 2FA management
- GET /api/v1/auth/tokens - List all user tokens
- DELETE /api/v1/auth/tokens/$tokenUid - Revoke token
- GET /api/v1/auth/providers - List OAuth providers
- POST /api/v1/orgs - Create organization
- GET/PATCH /api/v1/orgs/$org/settings - Org settings
- GET/POST /api/v1/orgs/$org/tokens - Org tokens
- GET/POST/DELETE /api/v1/orgs/$org/invitations - Invitations
- POST/GET /api/v1/auth/membership-requests - Self request to join an org by slug
- DELETE /api/v1/auth/membership-requests/$uid - Cancel own request
- GET /api/v1/orgs/$org/membership-requests - Admin: list incoming requests
- POST /api/v1/orgs/$org/membership-requests/$uid/approve|reject - Admin: decide
- GET/PUT/PATCH /api/v1/orgs/$org/entitlements - Per-org limits + features (service-token or admin)
- GET /api/v1/orgs/$org/entitlements/audits - Entitlement audit log (admin / service-token)
- CRUD /api/v1/orgs/$org/members - Members
- CRUD /api/v1/orgs/$org/checks - Checks
- POST /api/v1/orgs/$org/checks/validate - Validate check config
- GET/POST /api/v1/orgs/$org/checks/export|import - Import/export
- PUT /api/v1/orgs/$org/checks/$slug - Upsert by slug
- CRUD /api/v1/orgs/$org/check-groups - Check groups
- CRUD /api/v1/orgs/$org/connections - Integration connections
- GET/PUT/POST/DELETE /api/v1/orgs/$org/checks/$check/connections - Check connections
- GET /api/v1/orgs/$org/results - Results
- GET /api/v1/orgs/$org/incidents[/$uid[/events]] - Incidents
- GET /api/v1/orgs/$org/events - Events
- CRUD /api/v1/orgs/$org/status-pages (with nested sections/resources)
- CRUD /api/v1/orgs/$org/maintenance-windows (with nested checks)
- CRUD /api/v1/orgs/$org/jobs - Background jobs
- GET /api/v1/check-types - List check types (public)
- GET /api/v1/check-types/samples - Sample configs (public)
- GET /api/v1/orgs/$org/check-types - Org check types
- GET /api/v1/regions - Global regions (public)
- GET/POST /api/v1/heartbeat/$org/$identifier - Heartbeat ingestion (public)
- GET /api/v1/orgs/$org/checks/$check/badges/$format - Status badges (public)
- GET /api/v1/status-pages/$org[/$slug] - Public status pages
- POST /api/v1/workers/register|heartbeat|claim-jobs|submit-result - Worker API
- POST /api/v1/mcp - MCP endpoint
- GET/PUT/DELETE /api/v1/system/parameters - System params (super admin)

### Errors
All errors should return:
- `title`: The description as it could be presented to the user
- `code`: As it can be handled by the client code
- `detail`: A more detailed explanation

**Standard Error Codes** (defined in `base.HandlerBase`):
- `INTERNAL_ERROR` - Unexpected server error
- `VALIDATION_ERROR` - Input validation failed
- `NOT_FOUND` - Resource not found
- `UNAUTHORIZED` - Authentication required
- `FORBIDDEN` - Permission denied
- `CONFLICT` - Resource conflict (duplicate, etc.)
- `INVALID_CREDENTIALS` - Wrong email/password
- `INVALID_TOKEN` / `NO_TOKEN` - Token issues
- `REGISTRATION_DISABLED` / `EMAIL_NOT_ALLOWED` / `REGISTRATION_EXPIRED` - Registration errors
- `INVITATION_NOT_FOUND` / `INVITATION_EXPIRED` - Invitation errors
- `PASSWORD_RESET_EXPIRED` - Password reset timeout
- `2FA_REQUIRED` / `INVALID_2FA_CODE` / `INVALID_RECOVERY_CODE` - 2FA errors
- `CHECK_HAS_ACTIVE_INCIDENTS` - Cannot delete check with active incidents
- `ORGANIZATION_NOT_FOUND` / `USER_NOT_FOUND` / `CHECK_NOT_FOUND` / `CONNECTION_NOT_FOUND` - Resource not found
- `STATUS_PAGE_NOT_FOUND` / `STATUS_PAGE_SECTION_NOT_FOUND` / `CHECK_GROUP_NOT_FOUND` - Resource not found
- `MAINTENANCE_WINDOW_NOT_FOUND` / `TOKEN_NOT_FOUND` - Resource not found
- `INVALID_AUTO_JOIN_REGEX` - Auto-join email pattern is missing or too permissive
- `ALREADY_A_MEMBER` / `REQUEST_PENDING` / `REQUEST_NOT_FOUND` / `REQUEST_COOLDOWN_ACTIVE` - Membership request errors
- `ENTITLEMENT_EXCEEDED` / `FEATURE_NOT_ENTITLED` / `ENTITLEMENTS_STALE` - Entitlements errors

### API Testing
```bash
# Login and get JWT token (org is optional in body)
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' 'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# With JWT token
curl -s -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/default/checks'
```

### CLI Client
The CLI client is integrated into the main `solidping` binary as a `client` subcommand:
```bash
# Using the integrated client command
./solidping client checks list
./solidping client auth login
./solidping client results list

# Or build the standalone sp binary for convenience (shorter commands)
make build-cli
./bin/sp checks list
```

## Frontend Error Handling
See `docs/conventions/frontend-errors.md` for full details.

**Key rules:**
- **401**: Redirect to login with `?returnTo={currentPath}` to preserve navigation
- **403**: Show "Permission Denied" - **never redirect to login** (causes loops)
- **500**: Show user-friendly error with retry button
- **502/503/504**: Auto-retry with exponential backoff (transient errors)

## Specs
- All spec files must be prefixed with `YYYY-MM-DD-NN-` where `NN` is a zero-padded two-digit order number (e.g., `2026-02-21-01-adaptive-incident-resolution.md`)
- The order number `NN` resets per date: it must be unique among specs sharing the same `YYYY-MM-DD` day, across both `specs/todos/` and `specs/done/YYYY/MM/`, but two specs on different dates may reuse the same number (e.g., `2026-05-01-01`, `2026-05-01-02`, `2026-05-01-03`, `2026-05-02-01`, `2026-05-02-02`). Before creating a new spec, list existing files in those locations for the current date and pick the next available number.
- `specs/done/` contains completed specs in `YYYY/MM/` subdirectories (e.g., `specs/done/2025/12/2025-12-07-01-auth.md`)
- `specs/backlog/` contains specs planned for future implementation
- `specs/cancelled/` contains abandoned specs (same `YYYY/MM/` structure)

## Testing
- **Backend**: Table-driven tests with testcontainers for integration tests (see server/CLAUDE.md)
- **Dash0**: Playwright for E2E testing (see web/dash0/CLAUDE.md)
- **Both**: Comprehensive test coverage expected for new features
