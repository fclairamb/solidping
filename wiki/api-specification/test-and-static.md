# Test Endpoints & Static Routes

## Test Endpoints (Development Only)

These endpoints are always available:

### POST /api/v1/test/jobs
Create a test email job. Auth: public (dev only)

### GET /api/v1/fake
Fake API endpoint for testing. Auth: public (dev only)

These endpoints are only available when `SP_RUNMODE=test`:

### GET /api/v1/test/state-entries
List internal state entries. Auth: public (test mode only)

### POST /api/v1/test/checks/bulk
Bulk-create checks for testing. Auth: public (test mode only)

### DELETE /api/v1/test/checks/bulk
Bulk-delete checks for testing. Auth: public (test mode only)

### POST /api/v1/test/generate-data
Generate synthetic monitoring data. Auth: public (test mode only)

### DELETE /api/v1/test/checks/all
Delete all checks. Auth: public (test mode only)

### POST /api/v1/test/users
Create a user directly, bypassing registration and email confirmation, so E2E
suites can seed accounts. Auth: public (test mode only)

## Static & catch-all routes

### GET /dash0
### GET /dash0/*path
The embedded dashboard SPA. Auth: public (the SPA authenticates itself against
the API).

### GET /status0
### GET /status0/*path
The embedded public status-page SPA. Auth: public.

### GET /docs
### GET /docs/*path
The embedded **Docusaurus** documentation site (Docusaurus `baseUrl` is
`/docs/`). Served on every host, so `solidping.io/docs` works with no extra
infra. Auth: public.

### GET /openapi
Interactive OpenAPI (Swagger) explorer. Auth: public

### GET /openapi.yaml
Raw OpenAPI schema definition. Auth: public

### GET /metrics
Prometheus metrics. Gated by `SP_PROMETHEUS_ENABLED` (default true); returns
404 when disabled. Auth: public

### GET /*path
SPA catch-all — anything not matched by an API or static route falls through to
the dashboard's index so client-side routing works on deep links. Auth: public

### OPTIONS /api/v1/*path
CORS preflight no-op. Auth: public
