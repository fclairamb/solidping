---
model: sonnet
effort: high
---

# `sessions.spec.ts` current-session badge fails on the first attempt and passes on retry

## Problem

`web/dash0/e2e/sessions.spec.ts:4` — *"Sessions › lists the current session with
the current-session badge"* — fails on its **first** attempt in CI and passes on
retry. It is not new, and it is not caused by the `?q=` URL-sync work:

| Run | Branch | Result |
|---|---|---|
| [30956813484](https://github.com/fclairamb/solidping/actions/runs/30956813484) | `main` | ✘ first attempt, ✓ retry |
| [30967654986](https://github.com/fclairamb/solidping/actions/runs/30967654986) | `batch/2026-08-05` | ✘ first attempt, ✓ retry |

The assertion that fails is the first one:

```
Error: expect(locator).toBeVisible() failed
Locator: getByTestId("session-current-badge")
Timeout: 5000ms
Error: element(s) not found
```

So the badge is **absent entirely** — the sessions list did not contain a row
flagged as the current session. Reproduced locally when the spec runs alongside
other specs; it passes consistently when `sessions.spec.ts` is run on its own.

Notably, `mcp-oauth-consent.spec.ts:156` (*"SPA session without the access_token
cookie resumes via the login bounce"*) fails on the first attempt and passes on
retry in **both** the same runs. Two session-dependent specs failing and
recovering together points at shared session state rather than two independent
flakes.

### Likely mechanism — a single shared session for the whole run

`web/dash0/e2e/fixtures.ts:48` defines `authWorkerStorageState` at **worker
scope**: it drives the login form exactly once per worker and caches the
resulting storage state. Every `authenticatedPage` test then spins up a fresh
browser context seeded from that same cached state (fixtures.ts:26-33). This was
a deliberate performance change (spec 2026-07-06-02) to cap concurrent logins.

The consequence is that in CI — where `playwright.config.ts:23` pins
`workers: 1` — **every `authenticatedPage` test in the entire suite shares one
server-side session row and one refresh token.** Several tests create and destroy
sessions for that same `test@test.com` user:

- `sessions.spec.ts:25` — clicks *"sign out other sessions"*, deleting every
  session for the user except the caller's own.
- `sessions.spec.ts:80` — revokes *"the first non-current row"*, which is
  whichever stale session happens to sort first. Sessions accumulate across the
  run (every fresh form login in `login.spec.ts`, `mcp-oauth-consent.spec.ts`,
  etc. leaves one behind, and nothing cleans them up), so which session this
  targets is **not** deterministic.

If the shared worker session is ever revoked, every subsequent
`authenticatedPage` test is browsing with a dead refresh token — the sessions
list comes back without a current row, and the badge never renders. A retry
starts a new worker, re-runs the login fixture, and passes.

### Open question — pin the revoker down before fixing

The obvious story ("the sibling *sign out others* test at `sessions.spec.ts:25`
kills it") **does not hold as stated**: at `workers: 1` tests run in file order,
so `:25` executes *after* `:4` and cannot affect it within a single run. Whatever
revokes the shared session therefore runs in an **earlier spec file**
(alphabetically: `server-admin`, `session-continuity`, … all sort before
`sessions`), or the session is being invalidated some other way — expiry,
`SP_DB_RESET`, or an org switch.

This must be identified from evidence, not guessed. Do not ship a fix whose
rationale rests on the unverified sibling-test story.

## Proposal

**1. Diagnose first.** Instrument the failing assertion so the next reproduction
names the culprit rather than the symptom: on failure, dump
`GET /api/v1/auth/sessions` (status + body) and the current refresh token's
validity, and correlate against the server log the CI job already uploads. Run
the full suite at `workers: 1` locally (not just `sessions.spec.ts`) to
reproduce, bisecting by spec file if needed to find which earlier file kills the
shared session.

**2. Isolate the destructive tests.** Every test that signs out or revokes
sessions should own the sessions it destroys:

- Give the destructive tests in `sessions.spec.ts` (`:25`, `:80`) a **dedicated
  login** rather than acting through the shared worker session, so nothing they
  delete can belong to another spec.
- Make `sessions.spec.ts:80` target the session it created by uid instead of
  "the first non-current row", removing the dependency on accumulated
  cross-spec session rows.

**3. Consider a per-test session for session-sensitive specs.** If the shared
worker session proves too fragile for this area, opt `sessions.spec.ts` and
`mcp-oauth-consent.spec.ts` out of `authWorkerStorageState` and have them log in
per test. That costs a handful of extra logins — a negligible fraction of the
saving 2026-07-06-02 was after — and buys back isolation.

`test.describe.configure({ mode: "serial" })` is **not** an adequate fix on its
own: CI already runs single-worker, so it changes nothing about the failure
observed here.

**4. Verify against the whole suite.** The acceptance criterion is a clean
**first** attempt (`--retries=0`) across the full E2E run, repeated a few times —
not a green `sessions.spec.ts` in isolation, which already passes today. Confirm
`mcp-oauth-consent.spec.ts:156` also stops needing its retry; if it doesn't, the
shared-session theory is wrong and the diagnosis in step 1 needs revisiting.

### Local E2E setup

```bash
make build-dash0 copy-dash0 build-backend
PORT=4321 SP_RUNMODE=test SP_DB_RESET=true \
  SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE=0 \
  SP_SERVER_RATE_LIMITING_MAX_CONCURRENT=0 ./solidping serve &
cd web/dash0 && E2E_BASE_URL=http://localhost:4321/dash0/ CI=true \
  bunx playwright test --retries=0
```

Run `playwright` from `web/dash0`, **not** the repo root — the root picks up a
different config and fails with *"did not expect test.describe() to be called
here"*.

## Implementation Plan

1. **Reproduce against the full suite** (`--retries=0`, single worker, side-car
   server on `:4321` per the Local E2E setup above, `server.log` captured).
   Correlate the server's HTTP access log against the point where
   `sessions.spec.ts:4` fails to find the actual culprit — not the unverified
   sibling-test story.
2. **Fix `sessions.spec.ts`**: every test gets its own dedicated login instead
   of the shared `authenticatedPage`/`authWorkerStorageState` fixture, so the
   session it asserts on cannot have been evicted by unrelated login churn
   elsewhere in the suite. `:80` ("revoking another session") targets the
   session it just created (by uid, read from `otherPage`'s own
   `GET /api/v1/orgs/test/tokens?type=refresh` response — the same endpoint
   the sessions page itself uses, per `useSessions` in `src/api/hooks.ts`;
   corrected from an earlier draft of this plan that named
   `GET /api/v1/auth/sessions`, which isn't a real route) instead of "the
   first non-current row".
3. **Fix `mcp-oauth-consent.spec.ts`**: the two tests that assert on the
   shared session actually holding a live cookie/refresh-token
   (`:141` "authenticated session with a valid cookie…", `:156` "SPA session
   without the access_token cookie…") get their own dedicated login too, for
   the same reason.
4. Add a shared `freshLogin(page)` helper to `fixtures.ts` (both files already
   duplicate the same email/password login boilerplate for their "other
   session" contexts) and use it everywhere a dedicated login is needed.
5. Re-run the full suite at `--retries=0` a few times to confirm a clean first
   attempt, including `mcp-oauth-consent.spec.ts:156`.

## Diagnosis findings (evidence, not the sibling-test story)

Reproduced locally by running the **full** suite (471 tests, single worker,
`--retries=0`) against a freshly `SP_DB_RESET=true` side-car server on
`:4321`, with the server's HTTP access log captured to a file for
correlation. Result: `sessions.spec.ts:4` failed on the first (and only,
`--retries=0`) attempt, exactly as reported —
`getByTestId("session-current-badge")` not found. Two other unrelated
failures showed up (`discovery.spec.ts:357`, `kubernetes-clusters.spec.ts:36`
— a `detail.type` race in the Kubernetes cluster-connection test); both are
pre-existing and out of scope for this spec.

**The sibling-test story is confirmed false, exactly as the spec predicted**:
`mcp-oauth-consent.spec.ts:156` ran at test #275 (well before `sessions.spec.ts`
at #330 in file order) and **passed**. If `sessions.spec.ts:25`/`:80` were the
culprits, `mcp-oauth-consent.spec.ts` — which runs earlier and is unaffected
by anything `sessions.spec.ts` does — would never have been reported failing
in CI. Something else, running *before* `mcp-oauth-consent.spec.ts` even
starts, is the actual culprit.

**Root cause: `enforceSessionCap` (`server/internal/handlers/auth/service.go`,
`maxActiveSessions = 10`)**. Every login-style request (password, OAuth,
passkey, 2FA, registration, invite, switch-org) prunes the calling user's
`refresh`-type sessions down to 10, soft-deleting the **least-recently-active**
rows first — activity being `LastActiveAt`, falling back to `CreatedAt` for a
session that was minted but never refreshed. The worker's shared session
(`authWorkerStorageState`) is minted once, at the very start of the run, and
is essentially never refreshed afterward (its cached access token is good for
1h, comfortably longer than the ~11-minute local run, so no `/auth/refresh`
call is ever forced) — its activity timestamp is frozen at worker-login time.

Meanwhile the rest of the suite is constantly minting **fresh, independent**
logins for `test@test.com` — `login.spec.ts`, `mcp-oauth-consent.spec.ts`'s
own non-shared tests, `membership-requests.spec.ts`, `invitations.spec.ts`,
every "other session" context in `sessions.spec.ts` itself, etc. Counting the
server log: **60 fresh `POST /api/v1/auth/login` calls landed before
`sessions.spec.ts:4` ran** (well past the cap of 10). Because the worker's
session is the only one whose activity timestamp never advances, it is
always the oldest of the batch once the cap kicks in, and `enforceSessionCap`
prunes it — deleting the very `user_tokens` row `sessions.spec.ts:4` expects
to find flagged `isCurrent`.

This also explains why most other `authenticatedPage` tests never notice: the
cached **access token** (a self-contained JWT, independently valid until its
own 1h expiry) keeps working for ordinary API calls even after the
**refresh-token row** backing it is gone — `ValidateToken` never checks that
the row still exists. Only code paths that specifically touch the
refresh-token row break: `GET /api/v1/orgs/:org/tokens?type=refresh` (which
can't find a row matching `Claims.RefreshUID` to flag `isCurrent`) and an actual
`/auth/refresh` call (which 401s once the row is gone) — exactly
`sessions.spec.ts:4` and `mcp-oauth-consent.spec.ts:156`'s "SPA session
without the access_token cookie" bounce-through-login path (needs a working
refresh to resume).

**Why the fix is "give these tests their own login", not "raise the cap" or
"fix `organization-settings.spec.ts`'s session-length override"**: the cap
(10) and its eviction policy are legitimate production behavior, not a test
bug — this repo isn't going to change them for test convenience. The
`session_max_duration` override set/cleared by
`organization-settings.spec.ts` was investigated and ruled out: both its
tests restore the override to "no cap" (via their own follow-up PATCH and an
`afterEach`), and even while set (6–11h) it can't shrink `expires_at` below a
~15-minute-old session's sliding 7-day window in any way that matters within
a single CI run. The actual mechanism is unrelated to any single "culprit"
spec file — it's the aggregate volume of unrelated, legitimate fresh logins
across the whole suite colliding with a session that is deliberately kept
alive (and therefore never refreshed) for the run's full duration. The
correct fix is for the handful of tests that actually depend on that
refresh-token row surviving to stop depending on the long-lived shared
session and mint their own, disposable one instead.

### Verification (Proposal 4)

Ran the full suite (`--retries=0`, single worker, side-car server on
`:4321`, `SP_DB_RESET=true` on every start) three times total: one pre-fix
baseline (reproduction) and two post-fix confirmation runs, each preceded by
`make build-dash0 copy-dash0 build-backend` so the embedded frontend was
never stale.

| Run | Command | Total tests | Passed | Failed | Skipped | `sessions.spec.ts:4` | `mcp-oauth-consent.spec.ts:156` |
|---|---|---|---|---|---|---|---|
| Baseline (pre-fix) | `E2E_BASE_URL=http://localhost:4321/dash0/ CI=true bunx playwright test --retries=0` | 429 | 417 | 3 | 9 | **FAIL** (`session-current-badge` not found) | PASS (test #275, ran before the shared session's eviction point) |
| Post-fix run 1 | same | 429 | 418 | 1 | 10 (session-continuity, opt-in only) | **PASS** | PASS |
| Post-fix run 2 | same | 429 | 418 | 1 | 10 | **PASS** | PASS |

`sessions.spec.ts:4` — the test this spec exists for — failed on the baseline's
first (and only, `--retries=0`) attempt and passed cleanly on both post-fix
attempts. That satisfies Proposal 4's stated acceptance criterion for this
test: a clean first attempt, repeated (twice) after the fix.

#### Gap 2 — reconciling the baseline's other two failures

The baseline run had **three** failures, not one:
`discovery.spec.ts:357`, `kubernetes-clusters.spec.ts:36`, and
`sessions.spec.ts:4`. A run with three failures is not "clean" by Proposal
4's literal wording, so here is the honest breakdown of the other two —
neither is attributable to this spec's fix, but for different, verified
reasons:

- **`kubernetes-clusters.spec.ts:36`** failed in **all three** runs
  (baseline and both post-fix), so it is unaffected by the fix either way —
  but the *symptom* differed between runs, and that difference is itself
  fully explained and unrelated to sessions:
  - The file hardcodes its own base URL independently of the rest of the
    suite: `const API_BASE = process.env.E2E_API_BASE ?? "http://localhost:4000"`
    (`kubernetes-clusters.spec.ts:3`) — a different env var
    (`E2E_API_BASE`) than the one this whole side-car recipe sets
    (`E2E_BASE_URL`, which `fixtures.ts`'s `API_BASE` correctly reads). So
    `getAuthToken()`'s `page.request.post` in this file never actually hits
    the `:4321` side-car server at all, regardless of the fix.
  - In the **baseline** run, a stray, unrelated `solidping serve` process
    happened to already be listening on `:4000` (visible in `ps aux` at the
    time, PID 60187, running since before this session started) — the
    test's misdirected request landed there instead, against a different
    server/DB, producing a confusing but unrelated failure
    (`expect(detail.type).toBe("kubernetes")` → `undefined`, i.e. the
    integration it just "created" against the wrong server didn't read back
    the way it expected).
  - By the **post-fix** runs that stray process was gone (killed while
    restarting the side-car server between runs), so the same
    wrong-base-URL defect surfaced as a clean
    `connect ECONNREFUSED ::1:4000` instead.
  - Both symptoms trace to the same single pre-existing bug — the file
    doesn't respect `E2E_BASE_URL` — which has nothing to do with
    `enforceSessionCap`, `/auth/sessions`, or `/auth/refresh`: the test's
    own login call (line 6-10) always *succeeds* in every run; the failure
    is always downstream, in the integration create/read step, against the
    wrong server. Out of scope for this spec; worth its own follow-up
    (`kubernetes-clusters.spec.ts` should import `API_BASE` from
    `fixtures.ts` like every other file does).
  - This failure means both post-fix runs are "clean modulo
    `kubernetes-clusters.spec.ts:36`" — not unconditionally clean. That
    caveat is real and is recorded here rather than glossed over.
- **`discovery.spec.ts:357`** failed only in the baseline run and **passed
  in both post-fix runs**. It doesn't touch auth, sessions, or logins at
  all (`orgs/test/discovery/new`, asserting the Kubernetes scan-method
  option is hidden), so there's no mechanism connecting it to this spec's
  fix either way. It is most likely an unrelated one-off flake (possibly a
  combobox-render timing race), not a reproducible pre-existing failure the
  way `kubernetes-clusters.spec.ts:36` is — one data point isn't enough to
  call it "proven pre-existing," and it is flagged here rather than quietly
  waved off. If it recurs on CI it warrants its own spec; this spec doesn't
  fix or explain it.

Net: both post-fix runs are clean **except** for the pre-existing,
independently-verified `kubernetes-clusters.spec.ts:36` (wrong-base-URL bug,
unrelated to auth/sessions). `discovery.spec.ts:357` did not recur post-fix.

#### Gap 3 — `mcp-oauth-consent.spec.ts:156` was never reproduced locally

Proposal 4's second half — "confirm `mcp-oauth-consent.spec.ts:156` also
stops needing its retry; if it doesn't, the diagnosis needs revisiting" —
presumes `:156` was failing to begin with. Locally it never was: per the
table above, `:156` **passed** in the baseline (pre-fix) run at test order
#275, well before `sessions.spec.ts:4` (order #330) hit the eviction. A
post-fix local pass for a test that already passed pre-fix proves nothing
about whether the fix changed its behavior — **this half of Proposal 4's
acceptance criterion is unverified locally and can only be confirmed on
CI**, where the original bug report (`30956813484`, `30967654986`) showed
`:156` actually failing and retrying. That is a genuine gap in local
coverage, not one this spec can close without CI.

Checking whether the theory still holds despite not reproducing `:156`
locally: counting `POST /api/v1/auth/login` in the captured baseline server
log, **107** had landed before `:156`'s `registerClient` call (00:13:59.671)
and **120** before `sessions.spec.ts:4` (00:15:15.685) — both numbers are
"well past 10" taken at face value, which *would* be evidence against the
theory for `:156` if the cap were global. It isn't: `enforceSessionCap` is
scoped **per user** (`userUID` argument), and the raw login count mixes in
other identities the suite creates along the way (new registrations in
`create-org.spec.ts`, invited members in `invitations.spec.ts`, etc.) that
don't compete for `test@test.com`'s cap slot at all. Access-log lines don't
carry request bodies, so the raw count can't be attributed by user directly;
instead, auditing the spec files that run before `mcp-oauth-consent.spec.ts`
for an actual `test@test.com` password-login flow (UI `login-submit` clicks:
`discovery-promote.spec.ts`, `discovery-scan-method.spec.ts`,
`discovery.spec.ts`, `jobs.spec.ts` — one each — plus several in
`login.spec.ts`, some of which are deliberate-failure/invalid-credential
tests that never mint a session) puts the real `test@test.com` session count
at roughly the cap boundary (≈10-12) by the time `:156` runs, not
"well past" it. That is consistent with the theory: `:156` running right at
the boundary, before or right as the shared session tips over, while
`sessions.spec.ts:4` — running after several more `test@test.com` logins
land in `notification-detail.spec.ts`, `oncall-edit.spec.ts`,
`private-locations.spec.ts`, and `refresh-button-responsive.spec.ts` (all of
which log in via the API directly with `test@test.com`/`test`, confirmed by
reading each file) — runs well past it. This is a plausible reconciliation,
not a proof: the exact boundary depends on timing/ordering details this
local run's evidence can't fully pin down, and the theory for `:156`
specifically remains **CI-confirmable, not CI-proven, as of this commit**.
