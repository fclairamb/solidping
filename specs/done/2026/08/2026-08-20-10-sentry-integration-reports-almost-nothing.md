---
model: opus
effort: high
---

# The Sentry integration is wired but reports almost nothing

## Problem

Sentry is initialized, configured, release-tagged and flushed on shutdown
correctly — but the three code paths that would actually produce events are all
broken or unreachable. A production deployment with a DSN set receives close to
nothing.

### 1. 5xx responses are not reported (~141 of 142 sites)

The original spec
([2026-03-28-03](../done/2026/03/2026-03-28-03-sentry-integration.md), "Error
Reporting in Handlers") called for capturing inside `WriteInternalError()`
itself. What shipped instead is an *opt-in* sibling,
`WriteInternalErrorR` ([base.go:211](../../server/internal/handlers/base/base.go:211)),
which differs only in taking the request so it can find the hub:

```go
func (h *HandlerBase) WriteInternalError(w http.ResponseWriter, err error) error
func (h *HandlerBase) WriteInternalErrorR(w http.ResponseWriter, r *http.Request, err error) error
```

Call-site counts across `server/internal` (excluding tests and the definitions):

| Function | Call sites |
|---|---|
| `WriteInternalError` (no reporting) | 142 |
| `WriteInternalErrorR` (reports) | **1** — [discovery/handler.go:317](../../server/internal/handlers/discovery/handler.go:317) |

Opt-in reporting on a 142-site surface is reporting that does not happen. Every
new handler written since March has silently defaulted to invisible.

### 2. User and organization context can never attach

[`SentryMiddleware`](../../server/internal/middleware/sentry.go:28) reads the JWT
claims off the request context:

```go
if claims, ok := req.Context().Value(base.ContextKeyClaims).(*auth.Claims); ok && claims != nil {
    hub.Scope().SetUser(sentry.User{ID: claims.UserUID})
    hub.Scope().SetTag("organization", claims.OrgSlug)
}
```

But the middleware is registered second in the global chain
([server.go:540](../../server/internal/app/server.go:540)), while the claims are
only put on a *downstream* context by `RequireAuth`
([auth.go:83](../../server/internal/middleware/auth.go:83)) and `RequireMCPAuth`
([auth.go:127](../../server/internal/middleware/auth.go:127)), which run later on
protected subgroups. The value is provably absent when this code executes, so
the block is dead and every event Sentry receives is anonymous and untagged.

### 3. The panic path re-panics into a recovery middleware that does not exist

[sentry.go:39-44](../../server/internal/middleware/sentry.go:39) captures the
panic and then re-panics, commented:

> `// Re-panic so the existing recovery middleware can handle the HTTP response`

There is no recovery middleware. `grep -rn "Recoverer"` over the repo returns
nothing, `internal/httpx` has no `recover()`, and
[`Router.ServeHTTP`](../../server/internal/httpx/httpx.go:94) delegates straight
to `chi.Mux`, which does not recover unless `chi/middleware.Recoverer` is
mounted. The panic therefore unwinds all the way to `net/http`, which logs
`http: panic serving` and **drops the connection** — the client sees a reset
rather than the 500 the comment promises. This appears to have been lost in the
bunrouter→chi migration
([2026-07-19-03](../done/2026/07/2026-07-19-03-replace-unmaintained-bunrouter.md)).

### 4. Check and job panics never reach Sentry

Phase 3 of the original spec ("Check Execution Errors") was never implemented.
Both worker panic handlers convert the panic into a plain error and move on:

- [checkworker/worker.go:1233](../../server/internal/checkworker/worker.go:1233) — `ErrCheckerPanic`
- [jobs/jobworker/worker.go:471](../../server/internal/jobs/jobworker/worker.go:471) — `ErrJobPanic`

No `CaptureException`, no `check.type` / `check.uid` / `organization` tags.

### 5. No tests, and no default environment

`internal/middleware/` has no `sentry_test.go` — which is exactly why #2 and #3
survived review. Separately, `SentryConfig.Environment` has no default
([config.go:1442](../../server/internal/config/config.go:1442) sets only
`TracesSampleRate`), so an operator who sets just `SP_SENTRY_DSN` gets events
with an empty `environment` and no way to split dev noise from production in
the Sentry UI.

### Not in scope

- **Frontend `@sentry/react`** (original spec Phase 2 + source maps) — nothing
  is installed in `web/dash0` or `web/status0`. Real, but a separate spec: it
  needs a second DSN, a public-config plumb, an error-boundary swap and CI
  source-map upload. Leave it out unless it falls out for free.
- **`writeJSONError` leaking `internalErr.Error()` into the response `Detail`
  unconditionally** despite its "In development mode" comment
  ([base.go:158](../../server/internal/handlers/base/base.go:158)) — an adjacent
  issue, not a Sentry one. Do not fix it here; file it separately if confirmed.

## Proposal

### A. Make 5xx reporting non-optional

Change the signature to require the request and delete the opt-in variant:

```go
func (h *HandlerBase) WriteInternalError(w http.ResponseWriter, r *http.Request, err error) error
```

- Capture on the request-scoped hub (`sentry.GetHubFromContext`), falling back to
  no-op when absent — so tests and non-HTTP callers stay safe.
- Update all 142 call sites. Mechanical, but the request variable is named
  inconsistently (`r`, `req`, `request`) — verify with a build, not a blind `sed`.
- Delete `WriteInternalErrorR` and migrate its single caller.
- Also report from `WriteErrorErr` **when the status is >= 500** — it is used for
  some 5xx paths too. 4xx must never produce an event.

If the churn proves unworkable, the fallback is to keep the two-function shape
but forbid the non-reporting one with a lint rule; the 142-site rewrite is
preferred because it makes the correct thing the only thing.

### B. Set the Sentry user where the claims exist

Move the `SetUser` / `SetTag("organization")` block out of `SentryMiddleware`
and into `RequireAuth` and `RequireMCPAuth`, right after the claims are placed
on the context, applying them to the hub already on that context. Delete the
dead block from [sentry.go](../../server/internal/middleware/sentry.go). Keep
`SentryMiddleware` responsible only for cloning the hub, setting the request,
and putting the hub on the context.

### C. Add the missing recovery middleware

Add `middleware.Recoverer` (in `internal/middleware`, alongside the others) that
recovers, reports once, writes the standard 500 JSON error body, and returns
normally. Requirements, to be pinned by tests rather than by a prescribed order:

1. A panicking handler produces a **500 with the normal JSON error shape**, not a
   dropped connection.
2. The panic yields **exactly one** Sentry event, carrying the request scope and
   (when authenticated) the user/org from B.
3. The logging and HTTP-metrics middleware observe it as an ordinary 500 — the
   request is logged and counted, not silently missing from `/metrics`.

Requirement 2 means only one component may capture: with the recoverer sitting
inside `SentryMiddleware` (so the hub is on the context), `SentryMiddleware`
should stop recovering entirely and the recoverer becomes the single reporter.
Requirement 3 pushes it inside the logging/metrics middleware. Fix the stale
comment either way.

### D. Report worker panics with context

In both worker recovery paths, report through `sentry.WithScope` with the tags
the original spec asked for (`check.type`, `check.uid`, `organization` for
checks; job type and job UID for jobs) before converting to an error. A check
whose *target* is down is normal operation and must stay unreported — only the
checker itself crashing counts.

### E. Default the environment

Default `sentry.environment` to the run mode when unset — `test` when
`cfg.RunMode == "test"`, otherwise `production` — so no event ever lands
environment-less. Explicit config keeps winning. Update the table in
[web/docs/docs/features/observability.md](../../web/docs/docs/features/observability.md:66)
to document the default.

### F. Tests

Drive them off a `sentry.Init` with a capturing `Transport` so assertions are on
real events, not on mocks of our own wrappers. Each must include a **positive
control** — a case that proves the assertion can fail:

- `middleware/sentry_test.go`
  - authenticated request through the real chain → event carries `user.id` and
    the `organization` tag; **control:** an unauthenticated request produces an
    event with neither.
  - panicking handler → response is 500 JSON, body is the standard error shape,
    and exactly **one** event is captured (guards against double-reporting).
- `handlers/base` — `WriteInternalError` emits an event; **control:**
  `WriteError`/`WriteErrorErr` at 4xx emit none.
- worker tests — a panicking checker and a panicking job each produce one event
  with the expected tags; **control:** a check that merely reports its target
  down produces none.

## Implementation Plan

### A. `WriteInternalError` reports unconditionally
- `internal/errorreport/`: one tiny package owning the "which hub?" rule, so
  handlers, middleware and workers can never drift.
  - `CaptureOnRequest(r, err)` — request-scoped hub only, **no-op when absent**
    (HTTP paths always have one via `SentryMiddleware`; tests and non-HTTP
    callers stay silent).
  - `CaptureWithTags(ctx, err, tags)` — ctx hub, falling back to
    `sentry.CurrentHub()` (workers have no middleware).
- `base.HandlerBase`:
  - `WriteInternalError(w, r, err)` — captures, then writes the 500 body.
  - `WriteErrorErr(w, r, status, code, message, err)` — captures **only** when
    `status >= 500`; 4xx never emits.
  - `WriteInternalErrorR` deleted; its one caller
    (`discovery/handler.go:317`) moves to `WriteInternalError`.
- Call sites are rewritten by an AST tool (locate `Args[0].End()`, insert the
  enclosing function's `*http.Request` parameter name), never by `sed`: the
  request variable is `w`/`writer` + `r`/`req`/`request` depending on the file.
  The compiler is the acceptance test — 142 `WriteInternalError` +
  175 `WriteErrorErr` sites.

### B. Identity attaches where the claims exist
- Delete the provably-dead claims block from `SentryMiddleware`; it keeps only
  clone-hub / set-request / put-on-context.
- `RequireAuth` and `RequireMCPAuth` call a shared `setSentryIdentity(ctx, claims)`
  right after the claims land on the context, applying `SetUser{ID: UserUID}` +
  `SetTag("organization", OrgSlug)` to the hub already on that context.

### C. Recovery middleware
- New `internal/middleware/recover.go` — `Recoverer`:
  - wraps the writer in the existing `statusRecorder` so it only writes the
    standard 500 JSON body when the handler wrote nothing yet;
  - reports **once** through the ctx hub (`RecoverWithContext`);
  - re-panics `http.ErrAbortHandler` untouched and unreported (stdlib contract);
  - logs the panic + stack, and returns an `ErrPanic`-wrapped error so the
    logging middleware records it as a normal failed request.
- `SentryMiddleware` stops recovering/re-panicking entirely (single reporter),
  and the stale comment goes with it.
- Chain order (`server.go:540`): `cors → Sentry → logging → HTTPMetrics →
  Recoverer → timeout → rateLimit → concurrency`. Inside Sentry (hub on ctx),
  inside logging + metrics (observed as an ordinary 500).

### D. Worker panics carry context
- `checkworker.runCheckerGuarded` — tags `check.type`, `check.uid`,
  `organization` (the org UID; a worker never holds the slug) from the
  `*models.CheckJob` before the panic becomes `ErrCheckerPanic`.
- `jobworker.executeWithRecovery` — tags `job.type`, `job.uid`, plus
  `organization` when the job has one, before it becomes `ErrJobPanic`.
- A checker that merely returns "target down" is untouched — no capture on any
  non-panic path.

### E. Environment default
- `config.Load` defaults `Sentry.Environment` to `test` under
  `RunMode == "test"`, otherwise `production`, only when unset — explicit
  config/env keeps winning (both are applied before the default runs).
- `web/docs/docs/features/observability.md` table documents the default.

### F. Tests (real `sentry.Init`/client + capturing `Transport`)
Per-test hubs seeded on the request/worker context, so no global Sentry state is
mutated and `t.Parallel()` stays honest.
- `internal/middleware/sentry_test.go` — authed request through the real
  `SentryMiddleware → RequireAuth` chain carries `user.id` + `organization`;
  **control:** unauthenticated request carries neither. Panicking handler →
  500 + standard JSON shape + **exactly one** event; **control:** a
  non-panicking handler emits none.
- `internal/handlers/base/base_test.go` — `WriteInternalError` emits one event;
  **controls:** `WriteError` at 4xx, `WriteErrorErr` at 4xx emit none, and
  `WriteErrorErr` at 5xx emits one.
- worker tests — panicking checker / panicking job each emit one event with the
  expected tags; **control:** a checker reporting its target down emits none.
