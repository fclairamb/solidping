---
model: sonnet
effort: medium
---

# Two unauthenticated test-only routes are registered in every deployment, and one of them enqueues email to any address

## Problem

`server/internal/app/server.go:1977-1990` registers the test API. The comment
says these routes exist for "development/testing", and all but two of them sit
behind `if s.config.RunMode == "test"`. The first two do not:

```go
// Test API routes (no authentication for development/testing)
testHandler := testapi.NewHandler(s.jobSvc, s.dbService, s.services.EventNotifier)
api.POST("/test/jobs", testHandler.CreateEmailJob)   // <-- NOT gated
api.GET("/fake", testHandler.FakeAPI)                // <-- NOT gated

if s.config.RunMode == "test" {
    api.GET("/test/state-entries", testHandler.ListStateEntries)
    api.POST("/test/users", testHandler.CreateUser)
    api.POST("/test/checks/bulk", testHandler.BulkCreateChecks)
    ...
}
```

`api` is `mainGroup.NewGroup("/api/v1")` (`server.go:604`) — the bare group, with
no `RequireAuth` in its chain. Both routes are therefore reachable
**unauthenticated on every production instance**.

### `POST /api/v1/test/jobs` is an open mail relay

This is not a theoretical exposure. `CreateEmailJob`
(`server/internal/handlers/testapi/handler.go:102`) reads an arbitrary
recipient straight from the request body and enqueues a real email job:

```go
emailConfig := jobtypes.EmailJobConfig{
    To:           []string{body.To},                          // handler.go:127 — caller-controlled
    Subject:      h.getSubjectForType(body.Type, body.Params),
    Template:     template,
    TemplateData: body.Params,                                // handler.go:130 — caller-controlled
}
...
job, err := h.jobSvc.CreateJob(req.Context(), "", string(jobdef.JobTypeEmail), configBytes, nil)  // handler.go:139
```

There is no auth check, no org scoping (it is created as a system job with an
empty org), and no rate limit. Any unauthenticated caller can send `welcome` or
`incident` templated mail, from our domain and our IP reputation, to any address
they choose, with attacker-influenced template data and subject. That is a spam
and phishing vector, and it burns sender reputation for every self-hosted
operator too — the repository is public, so the endpoint is discoverable.

### `GET /api/v1/fake` is a different case — probably deliberate

`FakeAPI` (`handler.go:226`) is a configurable fixture endpoint: it returns
chosen status codes on a cycle, honours `delay`, `slowResponse`, `requiredAuth`
and `requiredHeader`. It has its own shipped spec
(`specs/done/2026/01/2026-01-02-fake-api.md`) and **live callers in the
dashboard** that build URLs from `window.location.origin`:

- `web/dash0/src/routes/orgs/$org/test.bulk.tsx:37`
- `web/dash0/src/routes/orgs/$org/test.templates.tsx:97`

Neither of those two dash0 routes contains any run-mode check of its own, so
gating `/fake` on `RunMode == "test"` may break them. A self-test target that
users can point a check at is a reasonable thing for a monitoring product to
offer publicly — so unlike `/test/jobs`, this one needs a decision rather than a
fix.

It is not free of risk, though: `delay` accepts up to 30 000 ms
(`handler.go:26-27`) and `slowResponse` streams on a timer, both unauthenticated,
so it is a modest connection-holding amplifier against our own instance.

## Provenance

Found 2026-08-23 by the independent completeness audit of spec
`2026-08-23-04` (forced rotation for the seeded admin). It is **pre-existing and
unrelated** to that spec — the audit confirmed the diff to `server.go` over that
spec's commit range was empty. It was deliberately left out of scope there and
filed here instead.

## Proposal

Treat the two routes separately; they are not the same problem.

1. **Move `POST /test/jobs` inside the `RunMode == "test"` block.** There is no
   argument for it being reachable in production: it exists so the local dev loop
   can render an email, and every sibling that does comparable damage
   (`/test/users`, `/test/checks/bulk`, `/test/generate-data`,
   `/test/checks/all`) is already gated. This half needs no decision.

2. **Decide `/fake` deliberately** (see the open question below), then either
   gate it the same way or leave it public **with an explanatory comment at the
   registration site**, so the next reader — or the next audit — does not "fix"
   it back and break the dashboard test pages.

3. **Add a regression test that the gate holds**, asserting the ungated routes
   answer `404` when `RunMode` is not `test`. Check first whether the existing
   gated routes have such coverage; if they do not, this is the moment to cover
   the whole block rather than only the routes being moved — the failure mode is
   silent and identical for all of them.

4. **Fix the misleading comment.** "Test API routes (no authentication for
   development/testing)" sits directly above routes that are neither
   test-scoped nor development-only. Whatever the outcome, the comment must
   describe what is actually registered where.

Follow the repo conventions: `testify/require`, `t.Parallel()`, and the standard
`{"title","code","detail"}` error shape (`CLAUDE.md`, `server/CLAUDE.md`).

## Open questions

**Must be answered before implementation starts.**

1. **Should `GET /api/v1/fake` stay publicly reachable in production?**

   - **Keep it public** — it is a documented, intentional fixture with a shipped
     spec and dashboard callers, and a public self-test target is a genuinely
     useful thing for a monitoring product. If so: does it need a rate limit or a
     lower `delay` ceiling to blunt the connection-holding vector, and should the
     dash0 test pages keep pointing at `window.location.origin`?
   - **Gate it on test mode** — smallest attack surface, consistent with every
     other `/test/*` route. If so, the two dash0 pages
     (`test.bulk.tsx`, `test.templates.tsx`) must either be gated to test mode
     themselves or be pointed at a different target, and that follow-on work is
     part of this spec.

   *Recommendation:* keep it public and bound it. It is documented product
   surface with real callers, and removing it in a patch release would break
   anyone whose checks point at it — whereas `delay`/`slowResponse` limits
   address the actual risk. But this is a judgment call about product surface,
   not a security question with one right answer, so it needs the maintainer.

2. **Does gating `/test/jobs` break anything that currently depends on it?**
   A grep of the repo, the docs under `web/docs/`, and the e2e suites should be
   part of the implementation. If a dev workflow or an e2e test calls it outside
   test mode today, say so rather than silently breaking it — but note that
   "something depends on it" is not a reason to leave an unauthenticated mail
   relay in production; it is a reason to move that caller into test mode too.

## Non-goals

- Auditing the rest of the route table for other ungated surfaces. If the
  implementation happens to notice one, file it separately rather than widening
  this spec.
- Reworking `EmailJobConfig`, the job system, or email rate limiting in general.

## Resolved open questions

**Maintainer decision, 2026-08-23.**

> 1. **Should `GET /api/v1/fake` stay publicly reachable in production?**

**Keep it public, and bound it.** `/fake` is documented, intentional product
surface with a shipped spec and live dashboard callers; gating it would break
`web/dash0/src/routes/orgs/$org/test.bulk.tsx` and `test.templates.tsx` and any
user whose check points at it. Implement, in this spec:

- Leave the route registered outside the `RunMode == "test"` block, and add an
  explanatory comment at the registration site saying it is deliberately public
  — a documented self-test fixture with dashboard callers — so a future reader
  or audit does not "fix" it back and break those pages.
- **Bound the connection-holding vector**: lower the `delay` ceiling from
  30 000 ms to a value that is still useful for latency testing but no longer a
  meaningful amplifier, and apply the same bound to `slowResponse`. Pick the
  ceiling to match whatever the repo already uses for comparable public limits;
  if there is no precedent, use **5 000 ms** and say so in the comment.
- **Rate-limit it** using the repo's existing rate-limiting middleware rather
  than a bespoke mechanism, per-IP. If the existing middleware cannot be applied
  to a single route without restructuring the group chain, say so in the final
  report and ship the `delay`/`slowResponse` bound alone rather than
  restructuring the route table for it.
- The dash0 test pages keep pointing at `window.location.origin` — no frontend
  change is required by this decision. If the lowered `delay` ceiling makes a
  dash0 page send a now-out-of-range value, adjust that page's input bounds.

> 2. **Does gating `/test/jobs` break anything that currently depends on it?**

**Settled by the spec's own directive** — no separate decision needed. Grep the
repo, `web/docs/`, and the e2e suites as part of the implementation. Report any
caller you find in the final report. If a caller exists outside test mode, move
that caller into test mode; do **not** leave the route ungated for it. An
unauthenticated mail relay in production is not negotiable.

The rest of the Proposal stands unchanged: move `POST /test/jobs` inside the
`RunMode == "test"` block, add regression coverage that the gated routes answer
`404` when `RunMode` is not `test` (covering the whole block, not only the moved
route, if the existing routes lack such coverage), and fix the misleading
comment above the registration site.
