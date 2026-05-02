# Backend Development Guide

This file provides backend-specific guidance for the SolidPing monitoring system.

## Core Technologies
- **Language**: Go 1.24+
- **HTTP Router**: bunrouter (lightweight HTTP routing)
- **ORM**: Bun ORM (PostgreSQL)
- **Configuration**: koanf (YAML + environment variables)
- **CLI**: urfave/cli
- **Code Generation**: oapi-codegen (OpenAPI client/server generation)
- **Testing**: testcontainers for integration tests, gotestsum for enhanced test output

## Common Commands

### Development
- **Build and test**: `make build`
- **Run development server**: `make run` or `make air` (with hot reload using air)
- **Database migrations**: `./solidping migrate`
- **Run tests**: `make gotest` (uses gotestsum for enhanced test output)
- **Generate code**: `make generate` (includes OpenAPI client generation and frontend codegen)
- **Lint**: `make lint` (uses golangci-lint)
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
- **`internal/app/server.go`**: HTTP server setup with bunrouter, middleware, route definitions, and service dependency injection
- **`internal/app/services/`**: Centralized service registry (`ServicesList`) for dependency injection
- **`internal/handlers/`**: Domain-specific handlers organized by domain
  - **`handler.go`**: HTTP request/response handling, input validation, error translation
  - **`service.go`**: Business logic, database operations, domain validation
  - **`handler_test.go`** and **`service_test.go`**: Comprehensive test coverage
  - Services are injected into handlers, never the reverse
- **`internal/handlers/base/`**: Common handler functionality (`HandlerBase`) for error handling and JSON responses
- **`internal/models/`**: Bun ORM models for database entities with custom types
- **`internal/migrations/`**: Database migration files
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
- Soft delete support via `deleted_at`

**parameters** - Key-value configuration per organization
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `key` (text) - Configuration key (alphanumeric + underscores + dots)
- `value` (jsonb) - Configuration value
- `secret` (boolean) - Whether value is sensitive

**users** - Organization members with authentication and role-based access
- `uid` (uuid) - Primary key
- `organization_uid` - Foreign key to organizations
- `user_id` (text) - User identifier
- `password_hash` - Hashed password for local auth
- `auth_provider_uid` - Optional link to OAuth provider
- `role` - User role: admin, user, or viewer

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
- `status` - 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error
- `duration` (float32) - Response time
- `metrics` (jsonb) - Per-execution metrics
- `output` (jsonb) - Detailed results and error messages
- `last_for_status` - True if this is the latest result that produced the check's current status

Aggregated-only fields (period_type ∈ 'hour', 'day', 'month'):
- `total_checks`, `successful_checks`, `availability_pct` - Uptime stats over the bucket
- `duration_min`, `duration_max`, `duration_p95` - Response-time stats
- `metrics` - Aggregated by suffix convention (`_min`, `_max`, `_avg`, `_pct`, `_rte`, `_sum`, `_cnt`, `_val`); see `server/internal/jobs/jobtypes/job_aggregation.go`

- `created_at` - Insertion timestamp (set by DB default)

### Monitoring System Features
- **Multi-tenancy**: All resources scoped to organizations via `organization_uid`
- **Soft deletes**: Most tables support `deleted_at` for recovery
- **Flexible authentication**: Email/password, OAuth2, and social providers
- **Distributed workers**: Multiple workers can execute checks with lease-based distribution
- **Results aggregation**: Results table holds both raw rows and rolled-up aggregations (hour/day/month) in the same shape, distinguished by `period_type`
- **Configuration management**: Flexible key-value config per organization via `parameters` table
- **Real-time monitoring**: Sub-minute check frequencies with immediate alerting
- **Domain Expiration Monitoring**: WHOIS-based domain expiration tracking with configurable alert thresholds (days remaining)

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
// Standard error response
h.WriteError(w, http.StatusNotFound, base.ErrorCodeNotFound, "Check not found")

// Internal error (logs and returns 500)
h.WriteInternalError(w, err)

// Success response
h.WriteJSON(w, http.StatusOK, data)
```

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
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
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
- **Email**: `admin@solidping.com`
- **Password**: `solidpass`
- **Organization**: `default`

### Troubleshooting
- If token expires, re-run the login command to get a fresh token
- Check server is running: `curl -s http://localhost:4000/api/mgmt/health`
- Enable debug logging: `LOG_LEVEL=debug ./solidping serve`
