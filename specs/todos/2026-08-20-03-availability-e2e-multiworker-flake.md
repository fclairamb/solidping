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
