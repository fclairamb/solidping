# Test Endpoints & Static Routes

## Test Endpoints (Development Only)

`GET /api/v1/fake` is the one route in this section that is always
available — it is intentional, documented product surface (see
`specs/done/2026/01/2026-01-02-fake-api.md`) with live dashboard callers, not
a dev-only leftover. It is unauthenticated by design, so its `delay` /
`slowResponse` params are capped (5000ms) and it carries its own strict
per-IP rate limit (see `server/internal/app/server.go`).

### GET /api/v1/fake
Fake API endpoint for testing. Auth: public (always available, rate-limited)

Every other endpoint below is only available when `SP_RUNMODE=test` — outside
test mode they 404, same as any unregistered route.

### POST /api/v1/test/jobs
Create a test email job. Auth: public (test mode only)

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

### GET /d
### GET /d/*path
The embedded dashboard SPA. Auth: public (the SPA authenticates itself against
the API).

### GET /s
### GET /s/*path
The embedded public status-page SPA. Auth: public.

### GET /dash0, /dash0/*path
### GET /status0, /status0/*path
The prefixes the two SPAs were mounted at before spec 2026-09-09-01. Each
answers `301 Moved Permanently` onto the matching `/d` / `/s` path, with the
remaining path and the query string preserved verbatim. **Permanent, no
sunset** — emails sent long ago, bookmarks, chat messages and search-engine
indexes all still point here. `redirectRenamedOrgSPA` runs *after* this hop, so
a URL carrying both a retired prefix and a renamed org slug costs two hops, one
per concern. Auth: public.

### GET /docs
### GET /docs/*path
The embedded **Docusaurus** documentation site (Docusaurus `baseUrl` is
`/docs/`). Served on every host, so `solidping.io/docs` works with no extra
infra. Auth: public.

### GET /llms.txt
### GET /llms-full.txt
`docusaurus-plugin-llms`-generated manifests from the embedded docs build,
served both at `/docs/llms.txt` / `/docs/llms-full.txt` and, for crawler
convenience, at the conventional root path (same embedded file, no
duplication). Missing file returns 404 (the docs 404 page), not the SPA
shell. Auth: public.

### GET /openapi
Interactive OpenAPI (Swagger) explorer. Auth: public

### GET /openapi.yaml
Raw OpenAPI schema definition. Auth: public

### GET /metrics
Prometheus metrics. Gated by `SP_PROMETHEUS_ENABLED` (default true); returns
404 when disabled. Auth: public

### GET /*path
Catch-all. `/` redirects (`302`) to `/d/`; the configured `SP_REDIRECTS` dev
proxy rules are applied; anything else answers a plain **HTML 404**. There is
no longer an SPA shell behind this route — the legacy `web/dash` app that used
to render for every unmatched URL was retired in spec 2026-09-09-01. Auth:
public

### OPTIONS /api/v1/*path
CORS preflight no-op. Auth: public
