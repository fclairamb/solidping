# Backend Development Guide

This file provides backend-specific guidance for the SolidPing monitoring system.

## Core Technologies
- **Language**: Go 1.24+
- **HTTP Router**: go-chi/chi v5, behind the in-repo `internal/httpx` adapter that preserves error-returning handlers (`func(w, *http.Request) error`) and a `Group`/`Use` middleware tree
- **ORM**: Bun ORM (PostgreSQL)
- **Configuration**: koanf (YAML + environment variables)
- **CLI**: urfave/cli
- **Code Generation**: oapi-codegen (OpenAPI client/server generation)
- **Testing**: testcontainers for integration tests, gotestsum for enhanced test output

## Common Commands

### Development
- **Build and test**: `make build`
- **Run development server**: `make run` or `make dev` (hot reload via `cmd/devloop`, build-then-swap so the API stays up across rebuilds; devloop also supervises the dash0/status0 dev servers and size-rotates `logs/<name>.log` with `.1`/`.2` backups)
- **Database migrations**: `./solidping migrate`
- **Run tests**: `make gotest` (uses gotestsum for enhanced test output)
- **Generate code**: `make generate` (includes OpenAPI client generation and frontend codegen)
- **Lint**: `make lint` (uses golangci-lint)
- **Measure memory**: `make bench-memory` (`cmd/membench`) — boots a fresh
  server per repetition, samples `/api/mgmt/memory` on a fixed protocol and
  reports the inter-run spread, so a claimed reduction can be told apart from
  GC phase. `BENCH_MEM_MODE=docker` runs the shipped image under a real cgroup
  limit (build it from the working tree with `make bench-memory-image`) and is
  the only authoritative mode. Runbook: `wiki/runbooks/memory-profiling.md`.
  The measurement core (median/p95/spread and the "not significant" rule) is
  pure, unit-tested code in `internal/membench`; `internal/meminfo` holds the
  `/proc` + cgroup parsers behind `/api/mgmt/memory`.
- **Set log level**: `LOG_LEVEL=debug ./solidping serve` (valid values: debug, info, warn, error)

## Architecture Overview

### Handler-Service Pattern
Strict separation between HTTP concerns and business logic:

**Handlers** (`*_handler.go`):
- HTTP request/response handling using `base.HandlerBase`
- Input validation and parameter parsing
- Authentication and authorization checks via middleware
- Error translation from domain errors to HTTP status codes
- Response formatting (JSON)
- **No direct database access**

**Services** (`*_service.go`):
- Business logic implementation
- Database operations using Bun ORM
- Transaction management
- Domain-specific validation
- Inter-service communication
- Return domain errors, not HTTP errors

**Service Injection**:
- Services are registered in `services.Registry` in `internal/app/services/`
- Handlers receive services via constructor injection
- Services can depend on other services but never on handlers

### Backend Structure
The Go backend follows a clean architecture pattern with strict separation of concerns:

- **`main.go`**: CLI entry point with serve/migrate commands using urfave/cli
- **`internal/app/server.go`**: HTTP server setup with the `internal/httpx` (chi) router, middleware, route definitions, and service dependency injection
- **`internal/app/services/`**: Centralized service registry (`ServicesList`) for dependency injection
- **`internal/handlers/`**: Domain-specific handlers organized by domain
  - **`handler.go`**: HTTP request/response handling, input validation, error translation
  - **`service.go`**: Business logic, database operations, domain validation
  - **`handler_test.go`** and **`service_test.go`**: Comprehensive test coverage
  - Services are injected into handlers, never the reverse
- **`internal/handlers/base/`**: Common handler functionality (`HandlerBase`) for error handling and JSON responses
- **`internal/models/`**: Bun ORM models for database entities with custom types
- **`internal/db/postgres/migrations/`** and **`internal/db/sqlite/migrations/`**: Database migration files (one consolidated `NNN_vX_Y_Z.up.sql` per release)

### Migration file naming (hard rule)

**A migration file is named after the version it will ship in — never after what it does.**
Format: `NNN_vX_Y_Z.up.sql` / `NNN_vX_Y_Z.down.sql`, where `vX_Y_Z` is the **upcoming**
release (the next version to be cut, not the current one). A feature-named file such as
`012_incident_number.up.sql` is wrong and must be renamed before release.

Why: there is exactly one consolidated migration per release, and the version is the only
name that stays meaningful once the feature that motivated it is old news. It also makes
"which schema does this deployment need?" answerable by reading the filename.

Mechanics worth knowing before you touch an existing file:

- Bun keys applied migrations on the **numeric prefix only** — `fnameRE` is
  `^(\d{1,14})_([0-9a-z_\-]+)\.`, and `migrationsWithStatus` matches on `Name` (`012`),
  with the rest kept as an informational `Comment`. **Renaming the comment half of an
  already-applied migration is therefore safe** — no existing database re-runs it.
- **Renumbering is NOT safe.** Changing `012` to anything else makes every already-migrated
  database treat it as new (or silently skip a real one), which surfaces as a startup crash
  or 500s. If a consolidation forces a renumber, the dev DB must be reset or
  `bun_migrations` reconciled by hand.
- The comment half must match `[0-9a-z_\-]+` — lowercase only, so `v0_15_0`, never `v0.15.0`.
- Rename **both** dialects (`postgres/` and `sqlite/`) and **both** directions
  (`.up.sql`/`.down.sql`) together, and grep for the filename first: migration tests read
  these files by name via `migrationsFS.ReadFile`.
- Editing an **already-applied** migration in place (renumbering, or rewriting content) is
  caught at boot by `internal/db/migrationguard`, which checksums every applied `.up.sql`.
  Default mode `strict` fails the boot on a mismatch; `db.migration_guard_mode` /
  `SP_DB_MIGRATION_GUARD_MODE=warn` logs and continues instead — the local dev loop
  (`make dev` / `dev-test` / `dev-saas`) always runs warn. `solidping migrate repair`
  re-records checksums for applied migrations (no migration runs) to clear a cosmetic-edit
  mismatch. See `wiki/conventions/database.md` for the full guard/repair writeup.
- **`internal/middleware/`**: Authentication, CORS, logging, and organization context
- **`internal/config/`**: Configuration management using koanf (YAML + environment variables)

### Key Backend Features
- **Distributed Check System**: Multiple workers execute monitoring checks with lease-based job distribution
- **Multi-protocol Monitoring**: HTTP/HTTPS, TCP, ICMP ping, DNS, SSL certificate validation
- **Real-time Results**: Time-series monitoring data with availability calculations and response time metrics
- **Multi-tenant**: Organization-scoped data isolation with proper access control via middleware
- **Notification System**: Slack, Discord, webhook integrations for alerting
- **Authentication**: JWT-based with refresh tokens, Personal Access Tokens (PAT), and OAuth provider support
- **Error Handling**: Standardized error responses with `base.HandlerBase` and consistent error codes
- **Configuration**: Flexible configuration with koanf supporting YAML files and environment variable overrides

## Database Schema

### Core Tables

**organizations** - Multi-tenant structure for isolating monitoring resources
- `uid` (uuid) - Primary key
- `slug` (text) - URL-friendly unique identifier (3-20 chars, alphanumeric + hyphens)
- `logo_url` / `logo_file_uid` - Optional org logo: an external http(s) URL, or `/pub/assets/<file uid>` for an upload (owner-only `PATCH /api/v1/orgs/:org` and `POST /api/v1/orgs/:org/logo`). The uploaded blob is public because its file row carries the topic `organizations/<org uid>/logo` — see `internal/handlers/files/publictopics.go`
- Soft delete support via `deleted_at`

**organization_previous_slugs** - Rename aliases. A renamed org keeps answering on its old slug: lookups fall back to this table and permanently redirect (301 GET/HEAD, 308 otherwise) to the current slug, across the API, status pages, badges, the embed widget and the dash0/status0 SPA URLs. A live `organizations.slug` always wins over an alias, and an alias is released the moment another org claims the slug. A soft-deleted org is never reachable through an alias — its slug 404s immediately (spec 2026-08-08-11). See `wiki/api-specification/orgs.md`.

**parameters** - Key-value configuration per organization
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `key` (text) - Configuration key (alphanumeric + underscores + dots)
- `value` (jsonb) - Configuration value
- `secret` (boolean) - Whether value is sensitive

**Parameter key convention.** Param keys mirror the config struct path: dots for hierarchy (`email.host`, `auth.google.client_id`), snake_case within a segment for word breaks (`email.from_name`, `aggregation.retention_raw`). New keys must follow this — never add a top-level snake_case key.

**users** - Organization members with authentication and role-based access
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `user_id` (text) - User identifier
- `password_hash` - Hashed password for local auth
- `auth_provider_uid` - Optional link to OAuth provider
- `role` - User role: owner, admin, user, or viewer (hierarchical — an owner passes every admin gate; only an owner may delete the org or grant ownership)

**auth_providers** - Authentication methods per organization
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `slug` - URL-friendly provider identifier within organization
- `type` - Authentication type: email, password, google, github, gitlab, microsoft, twitter, oauth2
- `config` (jsonb) - Provider-specific configuration

**workers** - Distributed service workers that execute monitoring checks
- `uid` (uuid) - Primary key
- `identifier` - Unique system identifier (e.g., hostname, container ID)
- `name` - Human-readable name
- `context` (jsonb) - Worker metadata (e.g., {"region": "eu"})
- `last_active_at` - Last heartbeat timestamp

**checks** - Monitoring configurations and target definitions
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `name` - Check name
- `slug` - URL-friendly unique identifier (unique per organization)
- `type` - Check type (ping, http, tcp, dns, ssl, etc.)
- `config` (jsonb) - Check-specific configuration (URLs, ports, timeouts, etc.)
- `enabled` - Whether check is active
- `period` - Check frequency (default: 1 minute)

**check_jobs** - Scheduler state for distributed check execution
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `check_uid` - One-to-one relationship with checks (unique)
- `context_conditions` (jsonb) - Criteria to match on workers.context
- `period` - Execution interval
- `scheduled_at` - Next execution time
- `lease_worker_uid` - Worker assigned to execute
- `lease_expires_at` - Lease timeout
- `lease_starts` - Execution attempt counter (0-1 normal, 10 indicates crash)

**results** - Time-series monitoring data — both raw check executions and rollups
- `uid` (UUIDv7, PK) - Time-ordered identifier; the embedded millisecond timestamp is used for fallback lookups when a row has been rolled up and deleted
- `organization_uid`, `check_uid` - Foreign keys
- `period_type` - Aggregation level: `raw` | `hour` | `day` | `month`. Aggregation job rolls `raw → hour → day → month` and deletes the source rows; retention thresholds are configurable
- `period_start` (notnull) - Start of the period (raw: execution time; aggregated: bucket start)
- `period_end` (nullable) - Bucket end, exclusive. Set for aggregated rows; nil for raw
- `region` (nullable) - Region the check ran in. Aggregations are per-region (one row per period × region)

Raw-only fields (period_type = 'raw'):
- `worker_uid` - Worker that executed the check
- `status` - 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error, 8=warning, 9=abandoned (7=degraded is aggregated-only). 9 is server-minted by the abandoned-result reaper and, like the created/running lifecycle markers, is excluded from every availability calculation — see `models.ResultStatus.ExcludedFromAvailability`
- `duration` (float32) - Response time
- `metrics` (jsonb) - Per-execution metrics (the HTTP checker leaves this NULL — response time lives in `duration`)
- `output` (jsonb) - Detailed results and error messages

Aggregated-only fields (period_type ∈ 'hour', 'day', 'month'):
- `total_checks`, `successful_checks` - Uptime stats over the bucket; availability % is derived at read time (`successful_checks / total_checks × 100`, null when `total_checks = 0`), never stored
- `duration_min`, `duration_max`, `duration_p95` - Response-time stats
- `metrics` - Aggregated by suffix convention (`_min`, `_max`, `_avg`, `_pct`, `_rte`, `_sum`, `_cnt`, `_val`); see `server/internal/jobs/jobtypes/job_aggregation.go`

- `created_at` - Insertion timestamp (set by DB default)

### Credential Encryption
Secret-bearing fields in `checks.config`, `integration_connections.settings`, and `check_jobs.config` are split into a public column (queryable JSONB) and an AES-256-GCM-encrypted private column (`*_private` TEXT envelope) when `SP_ENCRYPTION_MASTER_KEY` (or `SP_ENCRYPTION_MASTER_KEY_FILE`) is set. Per-org DEKs wrapped by the master KEK live in `parameters` (`secret=true`). When unset, secrets fall back to plaintext (intentional V1 fallback for self-hosted, logged at startup). PATCH semantics: secret keys absent from the request preserve the encrypted value; explicit empty/null clears. **Decrypt-and-merge always happens at the claim/dispatch boundary**, never inside `CheckWorker` or a checker: the in-process path merges in `checkworker/backend.DirectBackend.ClaimJobs`/`ClaimJobsForCheck` and deported agents unseal their region-sealed envelope in `backend.WSBackend`, so every job reaching the worker loop carries one merged plaintext `Config` and no envelope. The merge rule itself lives once in `checkjobsvc.MergeJobSecrets`. A job whose envelope cannot be opened (no master key on this process, or a decrypt failure) is **never dispatched without its secrets and never silently skipped** — it is dropped from the claim batch and reported as an explicit `StatusError` result naming the fix, which also releases the lease. With no master key configured at all there is no envelope and the plaintext public config passes through untouched (the documented V1 fallback). The dashboard never sees secrets — `GET` returns the public side plus `configPrivateKeys: [...]` for placeholder rendering. See `internal/crypto/credentials/` and `internal/credmigrate/`. Threat model: protects against DB theft only — not against process compromise, malicious admins, or worker log leakage.

### Monitoring System Features
- **Multi-tenancy**: All resources scoped to organizations via `organization_uid`
- **Soft deletes**: Most tables support `deleted_at` for recovery
- **Flexible authentication**: Email/password, OAuth2, and social providers
- **Distributed workers**: Multiple workers can execute checks with lease-based distribution
- **Results aggregation**: Results table holds both raw rows and rolled-up aggregations (hour/day/month) in the same shape, distinguished by `period_type`
- **Configuration management**: Flexible key-value config per organization via `parameters` table
- **Real-time monitoring**: Sub-minute check frequencies with immediate alerting
- **Domain Expiration Monitoring**: RDAP-based domain expiration tracking (WHOIS fallback) with configurable alert thresholds (days remaining)

## Error Handling

### Standard Error Response
All errors return JSON with:
```json
{
  "title": "Human-readable description",
  "code": "MACHINE_READABLE_CODE",
  "detail": "Detailed explanation"
}
```

### Error Codes
Define error codes in `internal/handlers/base/`:
- `ErrorCodeInternalError` - Unexpected server error
- `ErrorCodeValidation` - Input validation failed
- `ErrorCodeNotFound` - Resource not found
- `ErrorCodeUnauthorized` - Authentication required
- `ErrorCodeForbidden` - Permission denied
- `ErrorCodeConflict` - Resource conflict
- `ErrorCodeOrganizationNotFound` - Organization does not exist
- `ErrorCodeUserNotFound` - User does not exist
- `ErrorCodeCheckNotFound` - Check does not exist

### Handler Error Methods
```go
// Standard error response (no internal error to attach, never reported)
h.WriteError(w, http.StatusNotFound, base.ErrorCodeNotFound, "Check not found")

// Error response carrying an internal error. Takes the request: a 5xx written
// this way is reported to Sentry, a 4xx never is.
h.WriteErrorErr(w, r, http.StatusNotFound, base.ErrorCodeNotFound, "Check not found", err)

// Internal error (returns 500 and reports to Sentry)
h.WriteInternalError(w, r, err)

// Success response
h.WriteJSON(w, http.StatusOK, data)
```

**`WriteInternalError` and `WriteErrorErr` both take the request** — that is what
makes error reporting structural rather than opt-in (spec 2026-08-20-10). They capture
on the request-scoped Sentry hub that `SentryMiddleware` installs, so a handler cannot
return a 500 that Sentry never hears about. `WriteErrorErr` reports only when the status
is >= 500; 4xx is a client fault and must never mint an event. With no hub on the request
(a unit test, a non-HTTP caller) the capture is a silent no-op.

An error-translation helper that writes these responses therefore needs the request too —
`func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error`
is the shape the codebase uses.

## Testing
- **Framework**: Table-driven tests with testcontainers for integration tests
- **Assertions**: Use `testify/require` for all test assertions (NOT standard `testing` package assertions)
- **Test runner**: gotestsum for enhanced test output
- **Coverage**: Comprehensive test coverage expected for new features
- **Pattern**: Separate `handler_test.go` and `service_test.go` files for each domain

### Testing Standards
- **Always use `testify/require`** for assertions instead of manual `t.Error()` or `t.Fatal()` calls
- **Always call `t.Parallel()`** at the start of every test function (enforced by `paralleltest` linter)
- **Preallocate slices** when the capacity is known (enforced by `prealloc` linter)
This is how we initialize the required package:
```go
r := require.New(t)
```
- Use `r.NoError(err)` instead of `if err != nil { t.Fatal(err) }`
- Use `r.Equal(expected, actual)` instead of `if actual != expected { t.Errorf(...) }`
- Use `r.NotNil(value)` instead of `if value == nil { t.Error(...) }`
- Use `r.True(condition)` instead of `if !condition { t.Error(...) }`
- Use `r.Contains(haystack, needle)` for substring checks
- Use `r.Len(slice, expectedLen)` for length checks

## API Testing with curl

### Quick Start
The easiest way to test the API is to get a JWT token and save it to a file:

```bash
# 1. Login and save token to file (org is optional in body)
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.io","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' \
  | jq -r '.accessToken' > /tmp/token.txt

# 2. View the token (optional)
cat /tmp/token.txt

# 3. Use the token in subsequent requests
TOKEN=$(cat /tmp/token.txt)
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

### Common API Examples

**List all checks:**
```bash
curl -s -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

**Create a check:**
```bash
curl -s -X POST \
  -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Google","slug":"google","type":"http","config":{"url":"https://google.com"}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

**Get check by UID or slug:**
```bash
# By UID
curl -s -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  'http://localhost:4000/api/v1/orgs/default/checks/63d49e55-97e3-4e8c-b7ab-c862de7a43f3' | jq '.'

# By slug
curl -s -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  'http://localhost:4000/api/v1/orgs/default/checks/google' | jq '.'
```

**Update a check:**
```bash
curl -s -X PATCH \
  -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Google Updated"}' \
  'http://localhost:4000/api/v1/orgs/default/checks/google' | jq '.'
```

**Delete a check:**
```bash
curl -s -X DELETE \
  -H "Authorization: Bearer $(cat /tmp/token.txt)" \
  'http://localhost:4000/api/v1/orgs/default/checks/google'
```

### Tips for curl Testing

1. **Always save tokens to files** to avoid shell parsing issues with `$(...)` substitutions
2. **Use single-line commands** - avoid backslash line continuations in complex shells
3. **Pipe to jq** for pretty-printed JSON responses
4. **Use `-s` flag** to suppress curl progress output
5. **Check HTTP status** with `-w "\nHTTP: %{http_code}\n"` when needed

**Example with inline token (single line):**
```bash
curl -s -H "Authorization: Bearer eyJhbGci..." 'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'
```

### Default Credentials
- **Email**: `admin@solidping.io`
- **Password**: `solidpass`
- **Organization**: `default`

On a **fresh database** this account is seeded with `must_change_password`, so
the login above succeeds but every other endpoint answers `403` /
`PASSWORD_CHANGE_REQUIRED` until you rotate:

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"currentPassword":"solidpass","newPassword":"something-else"}' \
  'http://localhost:4000/api/v1/auth/change-password'
```

The flag is a general user-level capability (`internal/handlers/auth/password_rotation.go`),
enforced in `RequireAuth`, `RequireMCPAuth` and the realtime WebSocket handshake —
not a special case for the seeded admin. Test mode (`test@test.com`) is not flagged.

### Troubleshooting
- If token expires, re-run the login command to get a fresh token
- Check server is running: `curl -s http://localhost:4000/api/mgmt/health`
- Enable debug logging: `LOG_LEVEL=debug ./solidping serve`
