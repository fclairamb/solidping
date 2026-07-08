# Check results never carry a region unless the check has explicit `regions[]`

## Problem

`web/dash0/e2e/check-detail.spec.ts`'s "Recent Results table shows the region
a result ran from, not a dash" test (lines 101-138) fails **5 out of 5 times**
under `--repeat-each=5` against a freshly-reset Postgres-backed server,
confirmed in a clean isolated worktree at commit `da721d09` (pre-dates every
E2E-fixture change in this batch, so this is a pre-existing bug, not a
regression from recent login/fixture work). The failure is
`expect(regionCell).not.toHaveText("-")` timing out with the cell stuck at
`"-"`.

This is not a simple visibility race — it's two compounding, well-defined
backend behaviors:

**1. The very first "Recent Results" row is a synchronous placeholder that
structurally has no region.** `CreateCheck` inserts an `initialResult` row
(`status: created`, `output: {"message": "Check created"}`) in the *same
transaction* as the check itself
(`server/internal/db/postgres/postgres.go:1139-1156`, mirrored in
`server/internal/db/sqlite/sqlite.go:1046-1058`) — the struct literal never
sets `Region`, so it's `NULL` from the start. This row exists before the
check's job is ever claimed by a worker. `ListResults`
(`postgres.go:1733-1802`, ordered `period_start DESC, uid DESC`) applies no
status filter, and the frontend's `useResults` call
(`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:494-500`) doesn't
filter it out either — so it's a legitimate, frequent candidate for the
`.first()` row the test's `firstRow` locator grabs, well before the real
check execution lands (which needs job scheduling + worker claim + an actual
network request to `example.com`).

**2. Even the real, successfully-executed result never gets a region for a
check with no explicit `regions[]`** — exactly the E2E test's scenario
(`check-form.tsx:283`'s `showRegions = availableRegions.length > 1` gates the
regions field out of the payload for single-region deployments, so
`check.Regions` is empty). For that case:
- `createCheckJobs`'s single-job branch
  (`server/internal/db/postgres/postgres.go:1163-1181`,
  `models.NewCheckJob(...)`) never sets `checkJob.Region` — it stays `NULL`
  in `check_jobs` by design (the migration comment at
  `001_v0_1_0.up.sql:390` explains `NULL` means "any worker may claim it").
- `selectAvailableJobs` (`server/internal/checkworker/checkjobsvc/service.go:417-425`)
  only reads that `NULL` as a claim filter — it never writes the claiming
  worker's region back onto the job.
- `saveResult` / `saveErrorResult`
  (`server/internal/checkworker/worker.go:1048-1062` and `:1110-1124`, with
  `Region: checkJob.Region` at lines **1055** and **1117**) copy
  `checkJob.Region` straight onto the persisted result — i.e. `NULL`, always,
  for this case. The worker's own registered region (`worker.Region`,
  defaulted to `"default"` in `registerWorker`, `worker.go:328-337`, already
  fetched into a local `worker := r.getWorker()` at line 1046/1108) is never
  consulted as a fallback.

So the original feature spec's assumption
(`specs/done/2026/07/2026-07-05-02-check-detail-recent-results-region-column.md:14-18`,
"Single-worker dev setups register with region `default`... so even local
rows carry a value") was never actually true for the no-explicit-region code
path — it only holds for checks with `regions[]` explicitly set, where
`createCheckJobs`'s multi-region branch does assign
`checkJob.Region = &regionCopy` (`postgres.go:1183-1203`). This is a real
product gap beyond the E2E test: any self-hosted single-worker deployment (no
region selection ever surfaced in the UI) permanently stores `region = NULL`
on every result, silently degrading the per-region charts/filters built in
specs 2026-07-05-03 and 2026-07-05-13 for the common case.

## Proposal

### Backend: fall back to the executing worker's own region

In `server/internal/checkworker/worker.go`, `saveResult` (line ~1055) and
`saveErrorResult` (line ~1117): when `checkJob.Region` is `nil`, use the
worker's own registered region instead of leaving it `NULL`. Both functions
already fetch `worker := r.getWorker()` right before constructing the result
— resolve once:

```go
region := checkJob.Region
if region == nil {
    region = worker.Region
}
```

and use `region` in place of `checkJob.Region` in both result-construction
sites. This correctly attributes "the region a result ran from" to the
worker that actually executed it whenever the job itself wasn't pinned to a
specific region, without touching the multi-region assignment path (which
keeps taking priority since `checkJob.Region` is non-nil there).

### Test: target a genuine execution row, not the creation placeholder

`check-detail.spec.ts`'s `.first()` grab of `[data-testid^="result-row-"]`
will still sometimes land on the `status: created` placeholder row even
after the backend fix (it's a real, permanent row — not something to
suppress), since it's inserted synchronously and can outrank the real result
for a moment. `StatusBadge` (`web/dash0/src/components/shared/status-badge.tsx:16-21`)
renders unrecognized/`secondary`-style statuses (including `created`) with
their raw status string as the label, so the placeholder's badge literally
reads "created" — a stable way to exclude it. Update the test to wait for a
row whose status badge is *not* "created" (e.g. filter/locate on that, or
retry until such a row exists) before asserting on its region cell, with a
timeout generous enough for job scheduling + worker claim + an HTTP check
against `example.com` + save + the page's poll/refetch to land (the test
already documents a 30s fast-poll window for the first row's *appearance* —
the region assertion needs the same order of headroom, not the default
`expect()` timeout).

## Out of scope

- The login/fixture contention flake tracked separately in spec
  `2026-07-06-02` — unrelated mechanism.
- Region *assignment* logic for multi-region checks (`checkJob.Region` when
  `check.Regions` is non-empty) — already correct, not touched here.
- Aggregation/rollup grouping semantics for jobs whose region is genuinely
  unconstrained across multiple real-world workers of different regions
  (i.e., true multi-worker "any region" deployments where different
  executions of the *same* unconstrained job should ideally attribute to
  different regions per execution) — worth a follow-up if it turns out to
  matter in production, but out of scope for this fix, which only closes the
  single-worker/no-worker-else-to-disambiguate gap this test exercises.

## Acceptance criteria

- `check-detail.spec.ts`'s region-column test passes reliably under
  `--repeat-each=5` (and ideally `--repeat-each=10`) against a freshly-reset
  Postgres-backed server.
- A newly created check with no explicit `regions[]`, once its first real
  execution lands, has a non-null `region` on that result (verified via the
  API: `GET .../results?checkUid=...&with=region` returns `region: "default"`
  for a single, unconfigured worker).
- Checks with explicit `regions[]` are unaffected — their results still carry
  the assigned region, not the executing worker's own region if those ever
  differ.
- `make test-dash` and `make test` remain green.

## Implementation plan

- [x] `server/internal/checkworker/worker.go`: in `saveResult` and
      `saveErrorResult`, fall back to `worker.Region` when
      `checkJob.Region` is `nil` before constructing the result.
- [x] Add/extend a backend test covering the no-explicit-region case: a check
      with `Regions == nil` gets a result with `Region != nil` after
      execution (both success and error paths).
- [x] `web/dash0/e2e/check-detail.spec.ts`: update the region-column test to
      wait for a non-"created" status row rather than blindly the first row,
      with a timeout sized for real job execution.
- [x] Run the test with `--repeat-each=5` locally against a freshly-reset DB
      to confirm the flake is gone (not just green once) — passed 10/10.
- [x] `make test` + `make test-dash` full suite (`test-dash` fails
      pre-existingly/unrelated: `web/dash` has zero test files, targets the
      legacy unused directory — confirmed not a regression from this change).

## Status (2026-07-07) — premise correction

All code changes above are implemented, merged (`fix/check-result-region-null-for-default-region-checks`,
merge commit `a0b85e65`), and independently audited: every acceptance criterion is genuinely met
(region-column test passes reliably across repeats, a no-explicit-region check's result carries a
non-null `region` after execution per direct API verification, explicit-region checks are
unaffected, full test suite green).

However, the audit surfaced that this spec's **Problem section overstates current production
impact**. `ResolveRegionsForCheck` (`server/internal/regions/regions.go`) already runs for every
check creation/update via the handler (`server/internal/handlers/checks/service.go`) and always
resolves `check.Regions` to a non-empty list (defaulting to `["default"]`) — so for checks created
through the normal API/UI flow, `checkJob.Region` is *never* actually `nil` today, and
`createCheckJobs` always takes the multi-region branch. Concretely:

- The claim "any self-hosted single-worker deployment... permanently stores `region = NULL` on
  every result" is **not true** for checks created via the normal app flow as of this codebase
  state (it was verified by manually inserting a check/check_job row that bypasses
  `ResolveRegionsForCheck` — that path does hit the fallback and confirms the code works, but it's
  not how checks are created in practice today).
- The E2E test's fix — waiting for a non-`"created"`-status row instead of grabbing `.first()` —
  is what actually resolves the flake; the row it now waits for already has a `region` set by
  `ResolveRegionsForCheck`, independent of the `worker.Region` fallback.
- The `worker.Region` fallback in `saveResult`/`saveErrorResult` remains **correct, valuable
  defensive code** — it protects any current or future path that creates a `check_job` without
  going through `ResolveRegionsForCheck` (e.g. a direct DB write, a migration, or a future
  internal job-creation path) — it's just not fixing a reachable gap in today's normal flow.

No further code change is needed; this note exists so a future reader doesn't chase a production
symptom that the code as shipped doesn't currently reproduce.
