---
model: sonnet
effort: medium
---

# E2E specs that read `E2E_API_BASE` (or hardcode `localhost:4000`) silently talk to the wrong server

## Problem

`web/dash0/e2e/kubernetes-clusters.spec.ts:3` builds its own API base URL from
the wrong environment variable:

```ts
const API_BASE = process.env.E2E_API_BASE ?? "http://localhost:4000";
```

`E2E_API_BASE` is set by nothing. The rest of the suite — and
[`web/dash0/e2e/fixtures.ts:14-16`](../../web/dash0/e2e/fixtures.ts), which
already exports a correct `API_BASE` — reads **`E2E_BASE_URL`**, which is what
`playwright.config.ts` and the side-car test-server recipe actually set.

So whenever the suite runs against a side-car server on a non-default port, this
file's `getAuthToken()` and its integration create/read requests never reach the
server under test. The failure mode is confusing rather than obvious, and
depends on what happens to be listening on `:4000`:

- **Something is listening** (e.g. a `make dev` devloop): the requests silently
  succeed against a *different server and database*, and the test fails
  downstream with `expect(detail.type).toBe("kubernetes")` → `undefined` — an
  assertion failure that looks like a product bug.
- **Nothing is listening**: `connect ECONNREFUSED ::1:4000`.

Note the test's own login call always *succeeds* in both cases (it just logs
into the wrong server), so the failure never points at the base URL.

This was verified during
[`specs/done/2026/08/2026-08-05-02-sessions-e2e-shared-session-flake.md`](done/2026/08/2026-08-05-02-sessions-e2e-shared-session-flake.md)
— see its **"Gap 2"** verification section for the full evidence.
`kubernetes-clusters.spec.ts:36` failed in **all three** full-suite runs there
(one pre-fix baseline, two post-fix), independently of that spec's fix, with
each of the two symptoms above.

### It is not just one file

A sweep of `web/dash0/e2e/` and `web/status0/e2e/` turns up three tiers of the
same defect:

| Tier | Files | Behavior |
|---|---|---|
| **A — wrong env var** | `web/dash0/e2e/kubernetes-clusters.spec.ts:3`, `web/dash0/e2e/discovery-scan-method.spec.ts:26` | Read `E2E_API_BASE`, which nothing sets → always `localhost:4000` |
| **B — right env var, duplicated logic** | `web/dash0/e2e/webpush.spec.ts:5-7`, `discovery.spec.ts:5-7`, `global-setup.ts:27-29`, plus the `?? "http://localhost:4000/dash0/"` variants in `check-incidents-slug.spec.ts:19`, `live-updates.spec.ts:12`, `live-updates-handshake.spec.ts:21`, `dashboard.spec.ts:4`, `check-live-subscription-slug.spec.ts:15` | Correct, but each re-derives what `fixtures.ts` already exports |
| **C — fully hardcoded, no env var at all** | `web/status0/e2e/status-page.spec.ts:3`, `maintenance-badge.spec.ts:3`, `status-updates.spec.ts:13`, `subscribe.spec.ts:3` | `const BASE = "http://localhost:4000"` — cannot target a side-car server at all |

**Tier A is the actual bug** and the reason this spec exists. Tier B is
duplication that works today and is what lets Tier A hide. Tier C is a separate
surface (status0 has no `fixtures.ts` equivalent) and is scoped as a
lower-priority follow-up below, not required for this spec to land.

## Proposal

### 1. Fix Tier A (required)

In `web/dash0/e2e/kubernetes-clusters.spec.ts` and
`web/dash0/e2e/discovery-scan-method.spec.ts`: import `API_BASE` from
`./fixtures` like the majority of the suite does, and delete the local
`const API_BASE = process.env.E2E_API_BASE ?? …` line. Do not keep an
`E2E_API_BASE` fallback — no tooling sets it, so it is dead configuration that
only re-creates this bug.

### 2. Consolidate Tier B onto the shared export (required)

Replace each file's re-derived origin with the `API_BASE` already exported by
`fixtures.ts`. For the files whose local constant is a *dash0 page URL* rather
than an API origin (`…?? "http://localhost:4000/dash0/"` — `check-incidents-slug`,
`live-updates`, `live-updates-handshake`, `dashboard`,
`check-live-subscription-slug`), that's a different value than `API_BASE`; add a
second export to `fixtures.ts` (e.g. `DASH_BASE`) derived from the same
`E2E_BASE_URL` and use it, rather than leaving each file to spell out the
fallback.

`global-setup.ts` runs before fixtures are available in the normal way — check
whether importing from `fixtures.ts` is safe there; if it isn't, leave it as-is
and say so in the spec's follow-up notes rather than forcing it.

### 3. Guard against regression (required)

The reason this survived is that nothing catches it. Add a cheap guard so the
next one is caught at lint time rather than by a confusing assertion failure
months later — pick whichever fits the repo's existing tooling:

- an ESLint `no-restricted-syntax` / `no-restricted-properties` rule banning
  `process.env.E2E_API_BASE` and literal `http://localhost:4000` inside
  `web/dash0/e2e/**` (allowing `fixtures.ts` itself), **or**
- a tiny test in the e2e directory asserting no spec file contains either
  pattern.

Prefer the ESLint rule if dash0's config can scope a rule to a directory
without touching the pre-existing `react-hooks` debt. Whichever is chosen, it
must fail on the exact code being removed in step 1 — verify that by running the
guard against the pre-fix content, not just the post-fix content.

### 4. Verify against a non-default port (required)

The whole point is behavior on a port that is *not* 4000, so verification must
use one:

```bash
make build-dash0 copy-dash0 build-backend
PORT=4321 SP_RUNMODE=test SP_DB_RESET=true \
  SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE=0 \
  SP_SERVER_RATE_LIMITING_MAX_CONCURRENT=0 ./solidping serve &
cd web/dash0 && E2E_BASE_URL=http://localhost:4321/dash0/ CI=true \
  bunx playwright test e2e/kubernetes-clusters.spec.ts e2e/discovery-scan-method.spec.ts --retries=0
```

Run `playwright` from `web/dash0`, **not** the repo root — the root picks up a
different config and fails with *"did not expect test.describe() to be called
here"*. `make build-backend` alone does **not** re-embed the dash0 frontend;
`make build-dash0 copy-dash0` first, or the server serves a stale bundle.

Also confirm **nothing is listening on `:4000`** while verifying (`lsof -i :4000`)
— with a devloop running, a broken base URL still "works" against the wrong
server and the verification proves nothing. This is exactly how the bug stayed
invisible.

Then re-run the files touched in step 2 to confirm the consolidation didn't
break them.

### 5. Follow-up, out of scope for this spec

`web/status0/e2e/`'s four Tier-C files hardcode `http://localhost:4000` with no
env var at all, so status0's E2E cannot target a side-car server. status0 has no
`fixtures.ts` to import from, so fixing it means introducing one (or a small
shared `base.ts`) — a larger change than this spec. Record it as a follow-up
rather than expanding scope here.

## Open questions

- Should `E2E_API_BASE` be kept as a *deliberate* override (documented, and
  distinct from `E2E_BASE_URL`) for pointing tests at an API on a different host
  than the dash0 bundle? Leaning **no** — nothing sets it, no recipe documents
  it, and its only observed effect has been to mask the base URL. Delete it; a
  real need can reintroduce it explicitly.
- Is `discovery.spec.ts:357`'s intermittent failure related? It failed once on
  the baseline run in spec 2026-08-05-02 and passed twice after, and
  `discovery.spec.ts` is Tier B (correct env var), so probably not — but it is
  worth re-checking after this change lands, since `discovery-scan-method.spec.ts`
  in the same area *is* Tier A.

## Resolved open questions

> **Q:** Should `E2E_API_BASE` be kept as a *deliberate* override (documented, and
> distinct from `E2E_BASE_URL`) for pointing tests at an API on a different host
> than the dash0 bundle?

**Decision: no — delete it entirely.** Remove every `process.env.E2E_API_BASE`
reference from the suite. All e2e files must obtain their origin by importing
`API_BASE` (API origin) or the new `DASH_BASE` (dash0 page URL) from
`web/dash0/e2e/fixtures.ts`, and those exports derive **solely** from
`E2E_BASE_URL`. Do not add an `E2E_API_BASE` fallback anywhere, including in
`fixtures.ts`. The regression guard of Proposal §3 must therefore ban
`process.env.E2E_API_BASE` outright, not merely constrain it. If a real
split-host need appears later, it can be reintroduced deliberately in its own
spec.

> **Q:** Is `discovery.spec.ts:357`'s intermittent failure related?

**Decision: in scope — root-cause it.** This spec is not complete until that
intermittent failure is understood and fixed. Do not dismiss it as a flake and do
not "fix" it by re-running, adding a retry, increasing a timeout, or marking the
test skipped/`fixme`. Reproduce it, identify the actual race or ordering bug
(whether it lives in the test or in the product code), fix that cause, and add or
tighten an assertion/guard so the same race fails deterministically if it
regresses. If the root cause turns out to be product code rather than test code,
fix the product code. Report in the final `COVERAGE`/`NOTES` what the cause was
and how many consecutive passing runs of `discovery.spec.ts` you observed after
the fix (run it repeatedly — a single green run is not evidence).
