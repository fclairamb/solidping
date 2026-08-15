---
model: opus
effort: high
---

# `POST /api/v1/orgs/:org/jobs` lets any org member send arbitrary email and make arbitrary outbound HTTP

## Problem

The generic job-creation endpoint accepts **any registered job type** from **any
authenticated org member**, with no allowlist, no role gate, and no runmode gate.

Route ([server/internal/app/server.go:771](server/internal/app/server.go:771)):

```go
orgJobsGroup := api.NewGroup("/orgs/:org/jobs").
    Use(orgSlugRedirect.Middleware, authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess)
orgJobsGroup.POST("", jobHandler.CreateJob)
```

`RequireOrgAccess` is membership-only — a `viewer` passes it. The handler
([server/internal/handlers/jobs/handler.go:30](server/internal/handlers/jobs/handler.go:30))
decodes `{type, config}` straight into `jobSvc.CreateJob` with no filtering, and
[registry.go](server/internal/jobs/jobtypes/registry.go) registers every job type
generically: `sleep`, `email`, `webhook`, `startup`, `aggregation`,
`state_cleanup`, `notification`, `snooze_sweep`, `escalation_step`, and the
discovery family (network / kubernetes / container / freebox).

Two of those are directly abusable:

**1. `email` — arbitrary content through the org's SMTP sender.**
`buildMessage` ([job_email.go:191-195](server/internal/jobs/jobtypes/job_email.go:191))
has a raw-content branch: with no `Template`, it assigns
`msg.HTML = r.config.HTML` / `msg.Text = r.config.Text` verbatim.
`validateEmailConfig` ([job_email.go:68-93](server/internal/jobs/jobtypes/job_email.go:68))
explicitly blesses that path via `hasContent := cfg.HTML != "" || cfg.Text != ""`.
So:

```json
{"type":"email","config":{"to":["victim@example.com"],"subject":"...","html":"<raw html>"}}
```

delivers attacker-authored HTML from the org's configured sender, bypassing
`email.Formatter` entirely — a phishing primitive wearing the deployment's own
From: address and SPF/DKIM alignment. Recipients are unconstrained (no
same-org / verified-address check), so this is also an open relay for spam.

**2. `webhook` — arbitrary server-side HTTP (SSRF).**
`WebhookJobConfig` ([job_webhook.go:26-31](server/internal/jobs/jobtypes/job_webhook.go:26))
takes `url`, `method`, `headers`, `body` with no validation at all — no scheme
check, no private-range block. A member can make the server issue authenticated
requests to `http://169.254.169.254/`, cluster-internal services, or
`http://localhost:4000/api/...`, with attacker-chosen headers. Arguably worse
than the email path.

**Nothing legitimate uses this endpoint.**
- dash0 never POSTs to it. The jobs UI is read-only (`api/hooks.ts` around
  line 4882: *"Read-only views over the background-jobs queue and the check
  schedule"*); no `createJob` mutation exists anywhere in `web/dash0/src`.
- The two in-repo email enqueue paths — `internal/handlers/auth/service.go`
  `enqueueEmail` and `internal/handlers/testapi/handler.go` — call the job
  service directly and always set `Template`, never raw content.
- Everything else (aggregation, notification, discovery, escalation) is enqueued
  internally by schedulers, or has its own purpose-built, properly-gated
  endpoint. The org-admin and super-admin job surfaces
  ([server.go:787-800](server/internal/app/server.go:787)) are all `GET`.

So the endpoint appears to exist only because the handler was written generically.
It is documented in [openapi.yaml:3262](server/internal/app/openapi/openapi.yaml:3262)
(`createJob`) and [wiki/api-specification/jobs.md:15](wiki/api-specification/jobs.md:15),
which is the only reason an external client might plausibly be using it.

Found during the audit of
`specs/done/2026/08/2026-08-15-01-email-clean-rendering-and-incident-numbers.md`
and deliberately left out of scope there — that spec was about SolidPing's own
transactional templates, not about who may enqueue a job.

**Secondary defect, same handler:** `CreateJob` maps *every* `jobSvc.CreateJob`
error to `writeInternalError` → **500 / `INTERNAL_ERROR`**
([handler.go:47-49](server/internal/handlers/jobs/handler.go:47)). A malformed
config (e.g. `ErrEmailNoRecipients`) is a client error and must be
**400 / `VALIDATION_ERROR`**. Whatever gating lands, unknown/blocked job types
must likewise be a 4xx, not a 500.

**Dead duplicate:** `Handler.RegisterRoutes`
([handler.go:174-180](server/internal/handlers/jobs/handler.go:174)) registers the
same four routes and is used only by `handler_test.go:52` — production wiring is
the explicit block in `server.go`. Any gating added in `server.go` alone would be
invisible to the handler tests. Fix the divergence rather than gating in two
places that can drift.

## Proposal

**Decision: close it. Deny by default, with a narrow, explicit allowlist.**

Given that no first-party client uses `POST /jobs`, an allowlist is strictly
better than the alternatives: role-gating still hands an org admin an SSRF
primitive and an unbranded mail relay, and "require `Template` on `email`" fixes
one job type while leaving `webhook` wide open.

### 1. Job-type allowlist on the public create endpoint

Add to `jobdef` (next to the type constants) an explicit predicate for what may
be created over the public API — something like
`jobdef.IsPubliclyCreatable(t JobType) bool`, backed by a small allowlist
literal, **not** a blocklist. Default to deny, so a job type added later is
closed until someone opts it in.

Initial allowlist: **`sleep` only** (harmless, and it is the natural smoke-test
for the endpoint's own tests). Every other type — `email`, `webhook`,
`aggregation`, `notification`, `startup`, `state_cleanup`, `snooze_sweep`,
`escalation_step`, and the four discovery types — is rejected.

Enforce it in `jobs.Handler.CreateJob`, before calling `jobSvc.CreateJob`, so
internal enqueue paths (which go through the service directly) are unaffected and
keep full access to raw-content email. Reject with
**403 / `FORBIDDEN`** and a message naming the type, e.g.
`Job type "email" cannot be created through this endpoint`.

Do **not** put the check in `jobsvc.CreateJob` — that would break
`auth/service.go` `enqueueEmail`, `testapi`, and every scheduler.

### 2. Also require org admin

Belt and braces, and it costs nothing given nobody calls this: move the `POST`
onto a group that adds `authMiddleware.RequireOrgAdmin` (the pattern is already
right there at [server.go:791-793](server/internal/app/server.go:791)). Keep
`GET`/`DELETE` on the existing membership-gated group — the read views and
cancel are legitimately used. A read-only `viewer` enqueueing background work was
never intended.

### 3. Fix the status-code mapping

In `CreateJob`, distinguish client errors from server errors: config
validation failures (unmarshal errors, the `ErrEmail*` family, unknown job type
from the registry) → **400 / `VALIDATION_ERROR`**; blocked-by-allowlist → **403 /
`FORBIDDEN`**; genuine infrastructure failures → 500. Use
`errors.Is`/`errors.As` against the sentinel errors rather than string matching.

### 4. Collapse the duplicate route registration

Delete `Handler.RegisterRoutes` and have `handler_test.go` build the same group
chain `server.go` uses, **or** make `server.go` call `RegisterRoutes` and move the
middleware wiring into it. Either way there must be exactly one place that
decides which middleware guards `POST /jobs`, and the handler tests must exercise
it.

### 5. Keep raw-content email available internally — but assert the boundary

No change to `validateEmailConfig` or `buildMessage`: the raw-content branch stays
for internal callers. What changes is that it is no longer reachable from the
public API. Add a comment on the raw-content branch in `buildMessage` recording
that it is internal-only and why.

### Tests (this is the point of the spec — prove the negatives)

In `server/internal/handlers/jobs/handler_test.go`, against the **production**
middleware chain:

- `POST /jobs` with `{"type":"email", config:{to, subject, html}}` as an org
  **admin** → 403, and **no job row is created** (assert via the job service /
  DB, not just the status code).
- Same as a `viewer` / plain `user` → 403 (role gate), no job row.
- `POST /jobs` with `{"type":"webhook", config:{url:"http://169.254.169.254/"}}`
  → 403, no job row.
- Positive control: `{"type":"sleep", ...}` as an org admin → **201**, job row
  exists. Without this the suite would pass if `CreateJob` were simply broken.
- Positive control: `{"type":"sleep"}` as a `viewer` → 403 (proves the role gate
  is what rejects, independently of the allowlist).
- `{"type":"nonexistent"}` → 4xx, **not** 500.
- `{"type":"sleep", "config": <invalid>}` → 400 / `VALIDATION_ERROR`, not 500.
- A test that pins the allowlist: iterate every type in the `jobtypes` registry
  and assert `IsPubliclyCreatable` is false for all but the allowlisted set — so
  adding a job type later fails loudly if someone opts it in without thinking.
- Regression test that the **internal** path still works: enqueue a raw-content
  email through `jobsvc.CreateJob` directly and assert it validates and builds a
  message (the existing `job_email_test.go` coverage of raw content must stay
  green).

### Docs

- [openapi.yaml:3262](server/internal/app/openapi/openapi.yaml:3262): document
  the admin requirement on `createJob`, note that only `sleep` is accepted, and
  add the `403` response.
- [wiki/api-specification/jobs.md:15](wiki/api-specification/jobs.md:15): change
  `Auth: required` → `Auth: admin`, state the allowlist, and say why (arbitrary
  email / SSRF via generic job types). While there: the section preamble claims
  *"Routes are registered without authentication middleware at the router level
  (auth may be checked in handlers)"* — that is false today
  ([server.go:772-773](server/internal/app/server.go:772) uses `RequireAuth` +
  `RequireOrgAccess`); fix it.
- CHANGELOG: this is a **behaviour-breaking security fix** for anyone scripting
  against `POST /jobs`. Call it out explicitly under a security/breaking heading
  rather than burying it.

### Open question

Whether to keep `sleep` on the allowlist at all, or ship the endpoint fully
closed (403 for every type) and keep it only for `GET`/`DELETE`. The spec assumes
`sleep` stays — it makes the endpoint testable and harmless. If the implementer
finds no argument for keeping any public create path, closing it entirely and
deleting the `POST` route is acceptable, provided openapi/wiki are updated to
match and the tests assert the 404/405.

**Resolved:** `sleep` stays on the allowlist. The `POST` route is kept (gated),
not deleted — it remains the endpoint's own smoke test and the only way to prove
the gate rejects rather than the route simply being gone.

## Implementation Plan

1. **`jobdef.IsPubliclyCreatable`** (`internal/jobs/jobdef/types.go`, next to the
   `JobType` constants). Backed by `publiclyCreatableJobTypes`, an explicit
   allowlist map literal containing `JobTypeSleep` only. Deny by default: a job
   type added later is closed until someone edits this literal.

2. **Gate + error mapping in `jobs.Handler.CreateJob`**
   (`internal/handlers/jobs/handler.go`), in this order, all *before*
   `jobSvc.CreateJob`:
   - body decode failure → 400 `VALIDATION_ERROR` (unchanged);
   - type not in the `jobtypes` registry → 400 `VALIDATION_ERROR`
     (`Unknown job type "…"`), never a 500;
   - type known but `!IsPubliclyCreatable` → 403 `FORBIDDEN`,
     `Job type "email" cannot be created through this endpoint`;
   - config dry-run through the definition's `CreateJobRun` → any error is a
     client error → 400 `VALIDATION_ERROR`. This is what makes the `ErrEmail*`
     family reachable as a 400 for any type later added to the allowlist.
   - the `jobSvc.CreateJob` error is classified by `createErrorStatus`, which
     uses `errors.Is` (`jobsvc.ErrInvalidJobConfig`, the `ErrEmail*` sentinels)
     and `errors.As` (`*json.SyntaxError`, `*json.UnmarshalTypeError`) — never
     string matching — and falls through to 500 for anything else.
   The check lives in the handler only; `jobsvc.CreateJob` is untouched, so
   `auth/service.go` `enqueueEmail`, `testapi` and every scheduler keep full
   access to all job types including raw-content email.

3. **`jobsvc.ErrInvalidJobConfig`** sentinel (`internal/jobs/jobsvc/errors.go`),
   wrapped by `parseJobConfig`, so a malformed `config` is classifiable by the
   handler without matching on strings.

4. **One route-wiring place.** `Handler.RegisterRoutes` grows a
   `jobs.RouteMiddleware{Shared, CreateOnly}` parameter and becomes the single
   definition of the job route table; `server.go` calls it with
   `Shared = {orgSlugRedirect, RequireAuth, RequireOrgAccess}` and
   `CreateOnly = {RequireOrgAdmin}`. The inline `orgJobsGroup` block in
   `server.go` is deleted, so `GET`/`DELETE` stay membership-gated while `POST`
   additionally requires org admin, and the handler tests exercise the very same
   function.

5. **`buildMessage` raw-content comment** (`internal/jobs/jobtypes/job_email.go`)
   recording that the branch is internal-only, why it exists, and that the public
   endpoint's allowlist is what keeps it unreachable from outside.

6. **Tests.**
   - `internal/handlers/jobs/handler_test.go`: the fixture is rebuilt on the real
     chain — sqlite DB, real users/members, real JWTs from
     `auth.Service.GenerateTokensForOAuth`, real `middleware.AuthMiddleware`,
     routes registered through `RegisterRoutes` with the production middleware
     sets. Covers: `email` as admin → 403 + no row; `email` as viewer → 403 + no
     row; `sleep` as viewer → 403 + no row (role gate, independent of the
     allowlist); `webhook` → `http://169.254.169.254/` → 403 + no row; `sleep` as
     admin → 201 + row present (positive control); `nonexistent` → 4xx not 500;
     `sleep` with a malformed config → 400 `VALIDATION_ERROR` not 500. "No row"
     is asserted by counting `jobs` rows in the DB, not by trusting the status.
   - `internal/jobs/jobtypes/allowlist_test.go`: iterates the whole registry and
     pins `IsPubliclyCreatable` false for every type except `sleep`.
   - `internal/jobs/jobtypes/job_email_test.go`: regression test that raw-content
     email still validates and builds a message through the internal path.

7. **Docs.** `openapi.yaml` `createJob` (admin requirement, `sleep`-only, 403
   response), `wiki/api-specification/jobs.md` (`Auth: admin`, the allowlist and
   its rationale, plus the false "registered without authentication middleware"
   preamble), and a breaking/security entry in `CHANGELOG.md` Unreleased.
