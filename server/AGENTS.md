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

## Database Schema

Every table is org-scoped via `organization_uid`, uses `uid` UUID keys and soft deletes (`deleted_at`). Column tables: [wiki/database-model/](../wiki/database-model/README.md). Read [wiki/database-model/schema-notes.md](../wiki/database-model/schema-notes.md) before touching organizations/slug aliases, `parameters` keys (dotted lowercase, `usr.` prefix is org-managed), users/roles, results statuses and aggregation, or credential encryption.

Credential encryption in one rule: secrets are decrypted and merged at the claim/dispatch boundary (`checkjobsvc.MergeJobSecrets`), never inside `CheckWorker` or a checker; a job whose envelope cannot be opened is reported as an explicit error result, never dispatched without secrets and never silently skipped. See [wiki/features/credentials-encryption.md](../wiki/features/credentials-encryption.md).

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
Codes are `ErrorCode*` constants in `internal/handlers/base/` (`INTERNAL_ERROR`, `VALIDATION_ERROR`, `NOT_FOUND`, `UNAUTHORIZED`, `FORBIDDEN`, `CONFLICT`, ...). Wire list: [wiki/api-specification/errors.md](../wiki/api-specification/errors.md).

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

**`make test` is `-short` and therefore tests SQLite only.** Every
`*_postgres_test.go` self-skips there. The Postgres layer runs via
`make test-postgres` and in the `backend-postgres` CI job; the live-network and
Docker suites sit behind `//go:build slowtests` and run nightly. A new
Postgres-backed test routes its startup failure through
`testsupport.PostgresUnavailable` / `testsupport.PostgresInitFailed`, never a
bare `t.Skipf` — under `SP_TEST_REQUIRE_POSTGRES=1` that turns an unusable
database into a failure instead of a green run that proved nothing. Full layer
map: `wiki/testing/test-layers.md`.

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

## API testing with curl

Log in at `POST /api/v1/auth/login`, save the token to a file, send `Authorization: Bearer $(cat /tmp/token.txt)`. Recipes, default credentials, forced password rotation and troubleshooting: [wiki/runbooks/api-testing-with-curl.md](../wiki/runbooks/api-testing-with-curl.md).
