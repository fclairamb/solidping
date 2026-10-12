---
model: sonnet
effort: medium
---

# The server answers HEAD with 405 on every route, so external uptime monitors see status pages as DOWN

## Problem
`curl -sI https://solidping.io/`, `/d/` and `/api/mgmt/version` return `405 Method Not Allowed` with `Allow: GET`. RFC 9110 says a server should support HEAD wherever it supports GET, and many external uptime monitors probe with HEAD. Status pages served by SolidPing therefore look down to them.

Cause: since the bunrouter to chi migration (#156), `server/internal/httpx/httpx.go` registers every route with `g.mux.Method(method, pattern, ...)` (`httpx.go:264`) and nothing maps HEAD onto the GET handler. chi ships `middleware.GetHead` for this.

`server/internal/checkers/checktcp/samples.go` was switched from HEAD to GET in PR #531 because of this bug. Leave it on GET.

## Proposal
1. In `httpx.New()` (`server/internal/httpx/httpx.go:170`), call `mux.Use(chimw.GetHead)` (`github.com/go-chi/chi/v5/middleware`). It must be a mux-level middleware, not a `Group.Use` one: `Group.Use` only wraps handlers per route (`httpx.go:231-250`), whereas `GetHead` has to run before chi routing to rewrite the lookup. No duplicate route registration, so `RegisteredRoutes` / the duplicate-registration check stay untouched.
2. Explicit `HEAD` registrations (`Group.HEAD`, `httpx.go:283`) must keep priority. GetHead only rewrites when the HEAD lookup finds no route; confirm that and grep for existing `.HEAD(` callers.
3. Check the interactions and fix whatever breaks:
   - Route-table tests `server/internal/app/*route_table_test.go` (demo guard, viewer write guard, already treat HEAD as a read) and `RegisteredRoutes`/`Walk` consumers.
   - Compression wrapper (`server/internal/app/compression_test.go`): HEAD must keep `Content-Length`/`Content-Encoding` sanity and not write a body.
   - CORS middleware (`s.corsMiddleware`, `server.go:673`): HEAD must carry the same headers as GET.
   - `server/internal/app/custom_domain_routing.go:327`, which already special-cases HEAD (503 page): confirm it still takes precedence and stays consistent.
   - Auth/guards that already list HEAD as a read: `middleware/auth.go:701`, `middleware/orgslug.go:108`, `handlers/auth/demo_guard.go:103`, `handlers/auth/impersonation_guard.go:91`.
4. Handlers write bodies freely; net/http discards the body on HEAD responses, but verify no handler (SSE, streaming, SPA fallback, `/docs`, static assets) behaves badly when the body is discarded.

## Tests
- `server/internal/httpx/httpx_test.go`: a router with `GET /x` returning 200 + a header + a body. `HEAD /x` returns 200, the same header, empty body. `HEAD /missing` stays 404. `POST`-only route still answers `HEAD` with 405. A route with an explicit `HEAD` handler uses that handler, not the GET one.
- `server/internal/app/` (new `head_requests_test.go`, or extend `routing_test.go`): through the real server router, `HEAD /` and `HEAD /api/mgmt/version` return the same status as the GET with an empty body; a protected API GET route (e.g. `/api/v1/orgs/test/checks`) answers HEAD with the GET's 401 without auth, not 405; a POST-only route still returns 405 on HEAD.
- Existing route-table tests, `compression_test.go` and `cors_test.go` still pass.
- `make test` and `~/go/bin/golangci-lint run ./...` from `server/` (not through rtk) before pushing.

## Delivery
Branch `fix/head-requests` from `origin/main`. Open a PR, do not merge it. Add a `CHANGELOG.md` entry per `wiki/conventions/changelog.md`.

## To verify
- That `chimw.GetHead` behaves as described when routes are registered with `mux.Method(...)`, and its behaviour on wildcard/catch-all routes (SPA fallback, static assets).
- Whether the sentry/logging/metrics middleware record HEAD sensibly (no double-count, correct route label).
