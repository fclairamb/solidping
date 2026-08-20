---
model: opus
effort: high
---

# availability.spec.ts is flaky under parallel Playwright workers — check-create navigation never happens

## Problem

`web/dash0/e2e/availability.spec.ts` is flaky when Playwright runs with more than one
worker (`--workers=2`). It is **not** flaky at `--workers=1`, which is what CI uses
(`web/dash0/playwright.config.ts` sets `workers: process.env.CI ? 1 : undefined`), so CI
is currently unaffected — but local runs are unreliable.

The failure signature is always the same:

```
TimeoutError: page.waitForURL: Timeout 10000ms exceeded.
waiting for navigation until "load"
at createCheckAndOpen (web/dash0/e2e/availability.spec.ts:84)
```

i.e. after `page.getByTestId("check-submit-button").click()` in the shared
`createCheckAndOpen` helper (`web/dash0/e2e/availability.spec.ts:70-86`), the SPA never
navigates to `/checks/<uid>`. The server is **not** the bottleneck: with `LOG_LEVEL=info`
the corresponding `POST /api/v1/orgs/test/checks` completes in ~18 ms. So either the
click does not actually submit, or the post-create navigation races something in the
client.

### Evidence that this is pre-existing (unrelated to the SLO/SLA spec 2026-08-20-01)

- Reproduced **4 out of 4 runs** against a build of commit `72b4c710e` (the commit
  immediately before that spec started), using a git worktree, a freshly created
  Postgres database per run, and
  `bunx playwright test e2e/availability.spec.ts --workers=2 --retries=0`.
- At HEAD the same command fails ~2 out of 4 runs — i.e. no worse than base.
- It reproduces on Postgres as well as SQLite, and on a fresh database each run, so it
  is neither connection-pool contention nor accumulated test data.

### How to reproduce locally

1. `make build-dash0 && make copy-dash0 && cd server && go build -o /tmp/sp .`
2. Run a side-car test server on an alternate port against its own database, e.g.
   `PORT=4010 SP_RUNMODE=test SP_DB_TYPE=postgres SP_DB_URL='postgres://...' /tmp/sp serve`
3. `cd web/dash0 && CI=true E2E_BASE_URL=http://localhost:4010/dash0/ bunx playwright test e2e/availability.spec.ts --workers=2 --reporter=line --retries=0`

Repeat 4× with a fresh database each time.

## Proposal

**Root-cause it — do not paper over it.** Explicitly out of bounds:

- do NOT add retries,
- do NOT lengthen the timeout,
- do NOT replace `waitForLoadState("networkidle")` with a sleep.

(Repo convention: a flaky test is a bug to root-cause, never a flake to re-run — see
`feedback_flaky_tests_are_bugs`.)

Likely places to look:

- The check-create form's submit handling in
  `web/dash0/src/components/shared/check-form.tsx` — the submit button is
  `disabled={isPending}` (check-form.tsx:1535) and the form goes through
  `handleSubmit` (check-form.tsx:887): is the button briefly disabled, or does an
  async validation re-render swallow the click?
- The create route `web/dash0/src/routes/orgs/$org/checks.new.tsx` — `onSubmit`
  (checks.new.tsx:160) awaits `createCheck.mutateAsync` then navigates
  (checks.new.tsx:200): can that navigation be lost under load?
- Whether `createCheckAndOpen` should await a response / `expect` on the button
  state before clicking, rather than clicking into a transient state.

Once the actual mechanism is identified, fix it at the source and prove the fix by
re-running the reproduction loop above (multiple `--workers=2` runs, fresh database
each time, `--retries=0`).

If the root cause turns out to live in the shared create flow (form or route) rather
than in this spec's helper, audit the other check-creating E2E specs — they likely
benefit from the same fix.

## Implementation Plan

### 1. Root cause (verified, not assumed)

The failure is server-side, not a lost click and not a lost navigation.

- `createCheckAndOpen` (`web/dash0/e2e/availability.spec.ts:70-86`) fills a **fixed
  target** — `https://example.com/avail-test` — for every check it creates, varying only
  the *name*.
- An auto-generated check slug is derived from the **target**, not the name:
  `server/internal/checkers/checkhttp/checker.go:112` sets
  `spec.Slug = "http-" + strings.ReplaceAll(hostname, ".", "-")` → every worker resolves
  the same base slug `http-example-com`.
- `ensureUniqueSlug` (`server/internal/handlers/checks/service.go:2269`) is a
  SELECT-then-INSERT: it asks `GetCheckByUidOrSlug` whether the slug is free, then the
  insert happens later. Two workers both see it free and both insert.
- The loser violated `checks_slug_idx` (`create unique index checks_slug_idx on checks
  (organization_uid, slug) where deleted_at is null and slug is not null`). That raw
  23505 / `UNIQUE constraint failed` reached the handler's `default:` arm →
  **HTTP 500**.
- In the SPA, `onSubmit` (`web/dash0/src/routes/orgs/$org/checks.new.tsx:167`) awaits
  `createCheck.mutateAsync` and only calls `navigate` afterwards
  (`checks.new.tsx:199`). A rejected mutation therefore never navigates — which is
  exactly `page.waitForURL: Timeout 10000ms exceeded`, with the server logging a fast
  (~18 ms) request, as the spec observed.

This is `--workers=1`-clean by construction: with one worker the two creates are
sequential, so `ensureUniqueSlug` sees the first check and picks `http-example-com-2`.

### 2. Fix

`7fb8d5e4e` already landed the source fix (`insertCheckResolvingSlugRace` +
`db.IsCheckSlugCollision`) with **no tests**. This spec pins it and closes the gap the
tests expose:

- Extract the retry loop into a free function taking `insert` / `resolve` closures, the
  way `db.CreateIncidentWithNumber` is written, so the retry can be pinned
  deterministically instead of being left to the scheduler.
- Charge the retry budget only to **non-progress** attempts. A lost race means a
  competing create committed, so the re-resolved slug is a *different* one — that is
  contention to ride out, not a livelock. A fixed budget of 8 breaks under a burst of
  N simultaneous creates for one target (the unlucky one loses up to N-1 times), which
  is the same lesson `incidentnumber.go` already learned. Termination still holds:
  `ensureUniqueSlug` itself gives up after `-99`.

### 3. Tests (the bulk of the new code)

- `server/internal/handlers/checks/slug_race_internal_test.go` (package `checks`) —
  deterministic: retries a slug collision and uses the re-resolved slug; does **not**
  retry a different unique violation or an ordinary error; a user-provided slug maps to
  `ErrSlugConflict` without retrying; rides out more lost races than the stall budget;
  still terminates when the slug never moves.
- `server/internal/handlers/checks/slug_race_test.go` (package `checks_test`) — N
  concurrent `CreateCheck` calls for the same target on in-memory SQLite and on embedded
  Postgres: all succeed, all slugs distinct. Plus the rename path: a lost `UpdateCheck`
  race maps to `ErrSlugConflict` (400), never a 500.
- `server/internal/db/check_slug_collision_test.go` (package `db_test`) — feeds
  `IsCheckSlugCollision` violations produced by a **real** database on each engine, per
  its own doc comment: the `checks_slug_idx` violation matches; a violation of a
  different unique index does not.

### 4. Proof

Rebuild dash0 + embed, run the spec's own loop (`--workers=2 --retries=0`, fresh
Postgres database per run) against the HEAD binary, and against a **pre-fix** binary
built from `7fb8d5e4e~1` in a throwaway worktree as the negative control.

### 5. Follow-up audit

Grep `web/dash0/e2e/` for the other check-creating helpers and record whether they were
exposed to the same race.

## Verification results

### Reproduction loop (the spec's own commands)

Side-car server on port 4021, `SP_RUNMODE=test`, a **freshly created Postgres database
per run**, `--workers=2 --retries=0`, dash0 rebuilt and re-embedded.

| Binary | Runs | Failed | `checks_slug_idx` violations in the server log |
|---|---|---|---|
| pre-fix (`7fb8d5e4e~1`, same dash0 build) | 4 | **3** | runs 2/3/4: 1 each — run 1 (the only pass): **0** |
| HEAD | 6 | **0** | 1 in every run |

The correlation is the proof, not the pass count: on the pre-fix binary the runs that
failed are exactly the runs that logged a `checks_slug_idx` violation, and the one that
passed logged none. On HEAD the collision still fires on **every** run — so the
reproduction keeps its teeth — and is absorbed, with the create returning 201 and the SPA
navigating.

Failure line on the pre-fix binary:

```
level=WARN msg="SQL query failed" operation=INSERT
  error="ERROR: duplicate key value violates unique constraint \"checks_slug_idx\" (SQLSTATE=23505)"
```

### A second bug the tests exposed

The committed fix's retry ceiling was a flat 8 **attempts**. Covering it with a 24-way
concurrent create on embedded Postgres failed **3 runs out of 3** with a bare
`checks_slug_idx` violation: the unlucky goroutine spends one attempt per competing
commit, so N racers need up to N-1, and N belongs to the caller. The budget now counts
only *consecutive non-progress* inserts (`checkSlugStallAttempts`); after the change the
same test is green 3/3. This is the same distinction `db.IncidentNumberAttempts` already
documents.

### Nothing papered over

No retry, no lengthened timeout, no sleep, and no change to `availability.spec.ts` — the
spec file is untouched. The only client-side edit anywhere is a corrected comment (below).

## Follow-up audit — other check-creating E2E specs

28 specs create checks through the new-check form (or the API). The relevant fact is that
an auto-slug is derived from the target **hostname only**, so:

- **18 specs all resolve the same base slug `http-example-com`** in org `test` —
  availability, check-chart-point-preview, check-chart-zoom, check-dependencies,
  check-detail, check-edit-period-persistence, check-form-progressive-disclosure,
  check-groups, check-http-basic-auth, check-http-expected-status-codes,
  check-http-verify-ssl-follow-redirects, check-region-spread,
  check-result-detail-navigation, checks, command-menu, duty-cycle-warning,
  escalation-assignment (API), integrations (API). Two more share `http-httpbin-org`
  (check-labels, checks), and `checks.spec.ts`'s three heartbeat checks share the
  constant slug `heartbeat`. Every one of them was exposed to the same race and every one
  is fixed server-side by this change — **no client-side edit is needed**.
- `dns-check.spec.ts` is the only spec that pins an explicit `check-slug-input`, stamped
  with `Date.now()`. It was never exposed.
- The rest use file-local hostnames (`acme.com`, `example-domain-*.test`,
  `app.example.test`, `ssh.internal.example`, …) so they only ever raced themselves.

**One genuinely misleading helper was corrected** (comment only): `checks.spec.ts` built
`https://httpbin.org/anything/${timestamp}-${random}` under the comment *"Generate a
unique check name and URL to avoid slug conflicts"*. The path is discarded when the slug
is derived, so that comment taught the opposite of the truth; the same false assumption
is repeated in check-dependencies, check-groups, check-labels and integrations. Nothing
else in those files needs to change — an auto-slug colliding is now the server's problem,
which is the whole point of fixing it at the source.

Noted but deliberately **out of scope**: `checks.spec.ts`'s URL-empty validation test
asserts after a fixed `page.waitForTimeout(500)` rather than on a condition. It is a
latent flake of the same family but a different bug, and touching it here would mix
concerns.
