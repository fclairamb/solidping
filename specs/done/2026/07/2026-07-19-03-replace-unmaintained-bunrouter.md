---
model: opus
effort: high
---

# Replace the unmaintained bunrouter HTTP router with an actively maintained alternative

## Problem

The entire HTTP layer is built on `github.com/uptrace/bunrouter` v1.0.23
(`server/go.mod:69`), which no longer appears to be maintained — commits and
releases have dried up, and issues/PRs sit unanswered. For a product whose job
is uptime monitoring, an abandoned router is a liability: no fixes for future
`net/http` changes, no security patches, and a shrinking ecosystem.

The dependency is deep, not incidental:

- **115 Go files** reference bunrouter (~671 occurrences) across
  `server/internal/app`, `server/internal/handlers/*`,
  `server/internal/middleware`, `server/internal/oauth`,
  `server/internal/mcp`, and `server/internal/integrations/slack`.
- All handlers use bunrouter's **error-returning signature**
  `func(http.ResponseWriter, bunrouter.Request) error` — errors bubble up to a
  central error responder instead of each handler writing its own 500s.
- **278 `req.Param(...)` call sites** read path parameters.
- **163 routes** in `server/internal/app/server.go` use bunrouter's `:param` /
  `*path` syntax, including an `OPTIONS /*path` CORS catch-all
  (`server.go:423`) and static-segment-vs-param precedence the code explicitly
  relies on (comment at `server.go:460`).
- Route **groups** (`bunrouter.Group`, the `orgGroup` helper at
  `server.go:488`) and **9 `.Use(...)` middleware chains** (auth, org access,
  rate limit, timeout, metrics, sentry, logging, CORS) shape the routing tree.
- One `bunrouter.HTTPHandler(...)` compat shim wraps the Prometheus handler
  (`server.go:1251`).
- Many `_test.go` files construct bunrouter routers directly.

## Proposal

### Recommended replacement: `go-chi/chi` v5

Candidates considered:

| Candidate | Fit |
|---|---|
| **chi v5** | Actively maintained, MIT, zero-dependency, pure `net/http` handler signature, subrouters/`Route`/`Group`/`Use` map 1:1 to bunrouter's groups, `chi.URLParam` for params. Closest structural match; migration is mostly mechanical. |
| **stdlib `net/http.ServeMux`** (Go 1.22+ patterns) | Zero deps forever, `{param}`/`{path...}` wildcards. But no route groups, no per-group middleware, pattern-conflict panics, and we'd hand-roll everything bunrouter gave us. More custom code to own, for a codebase with 9 middleware chains and deep group nesting. |
| **echo** | Maintained and also has error-returning handlers, but imposes its own `echo.Context` — every one of the ~700 handler/middleware signatures changes shape, plus framework buy-in (binding, renderer) we don't need. |
| **gin** | Same framework-buy-in problem, and loses the error-returning handler style entirely. |
| **httprouter** | Effectively as unmaintained as bunrouter. Non-starter. |

**chi v5 is the best fit.** It is a router (not a framework), keeps
`http.Handler` compatibility so `promhttp` and other stdlib handlers plug in
directly, and its `Route`/`Group`/`Mount`/`Use` model maps directly onto the
existing bunrouter group tree.

### Migration shape

The key to keeping the diff mechanical is a small in-repo adapter that
preserves today's handler ergonomics:

1. **Adapter package** (e.g. `server/internal/httpx`):
   - `type HandlerFunc func(http.ResponseWriter, *http.Request) error`
   - `Wrap(h HandlerFunc) http.HandlerFunc` — invokes the same central error
     responder that bunrouter's error return feeds today (see
     `handlers/base/`), so handler bodies keep `return err`.
   - `Param(r *http.Request, name string) string` delegating to
     `chi.URLParam` — a find/replace target for the 278 `req.Param` sites.
2. **Route table conversion** in `server/internal/app/server.go`:
   `:uid` → `{uid}`, `*path` → chi's `/*` (read via `chi.URLParam(r, "*")`).
   Reproduce the `orgGroup` helper on `chi.Router`. Verify chi's routing
   precedence covers the static-vs-param ordering the comment at
   `server.go:460` depends on, and re-implement the `OPTIONS /*path` CORS
   catch-all (chi has `MethodFunc`/`cors` patterns for this).
3. **Middleware conversion** (`server/internal/middleware/*`): change
   signatures from `bunrouter.HandlerFunc → bunrouter.HandlerFunc` to the
   adapter's `HandlerFunc → HandlerFunc` (or plain
   `func(http.Handler) http.Handler` where no error flow is needed). Preserve
   `req.WithContext` context-threading semantics.
4. **Handler sweep**: mechanical signature change
   `bunrouter.Request` → `*http.Request` plus the `Param` helper across
   `handlers/*`, `oauth`, `mcp`, `integrations/slack`, `app`.
5. **Tests**: update router construction in `_test.go` files; the E2E
   Playwright suites and the existing backend integration tests are the safety
   net that routing behavior (precedence, wildcards, 404/405 shapes,
   trailing-slash handling) is unchanged.
6. Drop `uptrace/bunrouter` from `go.mod`.

### Risks / open questions

- **Trailing-slash and 405 semantics** differ subtly between routers; add
  table-driven route-matching tests for the tricky cases (docs `/docs` vs
  `/docs/*`, dash0/status0 static serving, org-scoped API precedence) before
  swapping.
- **Context threading**: bunrouter carries context on `bunrouter.Request`;
  with chi it's plain `r.Context()`/`r.WithContext` — audit the
  `RequestTimeout` middleware (`server.go:411` comment) during conversion.
- Whether to fold the error-returning adapter into `handlers/base` instead of
  a new package — decide during implementation; either is fine.

Scope is wide (115 files) but the work is dominated by two mechanical sweeps
(signatures, params) once the adapter and the route table land.

## Implementation Plan

Chosen approach: a small in-repo routing adapter (`server/internal/httpx`) over
`go-chi/chi/v5` that preserves bunrouter's ergonomics — error-returning
handlers, an error-returning middleware chain, and a `Group` tree with
`NewGroup`/`Use`/verb methods that map 1:1 onto the existing route table. chi
owns path matching, params, static-vs-param precedence, and 404/405. This keeps
the sweep mechanical and the diff small, and preserves the error-flow up the
middleware chain (logging/metrics still observe handler errors), which a
per-leaf chi-native wrap would drop.

Verified up front (see `httpx_test.go`): chi assigns param names from each
matched route's own `paramKeys`, so `:checkUid` / `:slug` / `:check` at the same
tree position coexist with **no** rename and no panic; static beats param;
`*path` → `*` read via `URLParam(r,"*")`; middleware run outermost-first;
handler errors propagate up the chain and are discarded at the top (as
bunrouter's `ServeHTTP` did).

Steps (each its own commit, `go build ./...` green before moving on):

1. **Adapter package** `internal/httpx`: `HandlerFunc`, `Middleware`, `Wrap`,
   `HTTPHandler`, `Param`, `RoutePattern`, and a chi-backed `Router`/`Group`
   (`New`, `NewGroup`, `Use`, `GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS`), plus
   `:param`/`*path` → chi pattern conversion. Unit-tested.
2. **Middleware conversion** (`internal/middleware/*`, `handlers/base`): swap
   `bunrouter.HandlerFunc`/`bunrouter.Request`/`bunrouter.MiddlewareFunc` for
   the `httpx` equivalents, `req.Param` → `httpx.Param`, `req.Request` → `req`,
   `req.Route()` → `httpx.RoutePattern(req)`. Context threading unchanged
   (`req.WithContext`). Audit the RequestTimeout goroutine handoff.
3. **Handler sweep** (`handlers/*`, `oauth`, `mcp`, `integrations/slack`, `app`):
   mechanical `bunrouter.Request` → `*http.Request`, `req.Param(` →
   `httpx.Param(req, `, `req.Request` → `req`, import fixups.
   `RegisterRoutes(*bunrouter.Group)` → `*httpx.Group`; slack `VerifyMiddleware`
   → `httpx` signature.
4. **Route table** in `app/server.go`: `bunrouter.New()` → `httpx.New()`, the
   group tree, the `OPTIONS /*path` CORS catch-all, the Prometheus
   `HTTPHandler` shim, and the `serve*` handlers.
5. **Tests**: update router construction in `_test.go` files (`bunrouter.New()`
   → `httpx.New()`, unwrap `bunrouter.NewRequest(x)` → `x`, param helpers). Add
   table-driven route-matching tests (precedence, wildcard, 405, trailing
   slash) in `app`.
6. **Cleanup**: drop `uptrace/bunrouter` from `go.mod` (`go mod tidy`), make chi
   a direct dependency, update `server/CLAUDE.md`.
7. **QA**: `make build-backend lint-back test`; boot the server and smoke-test
   representative routes (router swaps fail at runtime, not compile time).
