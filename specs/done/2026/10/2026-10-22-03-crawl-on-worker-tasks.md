---
model: opus
effort: high
---

# The website crawl runs on its own step machinery inside check_jobs, and never from a private location

Part of the agent-run work series (see 2026-10-22-02). Needs 2026-10-22-02 (`worker_tasks`).

## Problem
The crawl (spec 2026-10-03-03, v0.38.0, wiki `wiki/features/multistep-checks.md`) keeps its run
in `check_jobs` (`step_*` columns, `server/internal/db/models/check_job.go:110-114`). It is
claimed on the bulk lane (`checkworker/checkjobsvc/service.go:1016-1057`) and executed by
`checkworker/worker_steps.go` through the in-process-only `StepBackend`
(`checkworker/backend/backend.go:196-216`).

Agents exclude the bulk lane (`checkjobsvc/service.go:689-691`), and validation refuses a crawl
on a private location (`handlers/checks/validate.go:36-37, 466-478`). An intranet site, a staging
site behind a VPN or a site only reachable from a customer network cannot be crawled. These are
exactly the sites an agent reaches.

## Decisions
Taken 2026-10-22:
1. A crawl run is a `worker_tasks` row of kind `crawl` (one row per run, one claim per slice).
   The `check_jobs` row stays the schedule: period, "run now", lanes for billing projections.
2. A crawl may run from a private location. It is HTTP GETs, like an `http` check an agent
   already runs, so it needs no operator opt-in: an agent runs the `crawl` executor by default.
3. The billing semantics do not change: `UnitsPerSlice` tokens per slice, reserved and refunded
   server-side now (in `PrepareClaim` and at step or finish), for cloud and agent alike.
4. Runs in flight at upgrade restart from scratch. Accepted: a crawl is periodic, and its next
   run reports normally.

## Proposal
1. **Starting a run.** In `workertasks`, before selecting tasks for a region in `Claim`, the crawl
   kind materializes due runs (in the same transaction):
   - **Which jobs:** crawl `check_jobs` of the worker's region (and org, for an agent) with
     `lane = bulk` and `scheduled_at <= now` (or `run_requested_at` set), no live lease, and no
     active task (the unique index of spec 02 backs this).
   - **The task:** a `pending` `crawl` task with `check_uid`, the job's region and
     `run_started_at = now`.
   - **The lease:** the check_job is leased to the run: `lease_worker_uid = 'task:<task uid>'`,
     `lease_expires_at = now + maxRunDuration + 5 min`. An old node still running the v0.38
     bulk claim during a rolling upgrade then skips it, and nothing runs twice.
   - **Release:** when the task ends (`done`, `failed` or `cancelled`), the check_job lease is
     released and `scheduled_at` is the next period anchored on `run_started_at`, as today.
2. **Kind `crawl`, server side** (`TaskKind`):
   - `PrepareClaim`:
     - load the check and its config;
     - reserve `UnitsPerSlice` with `ReserveCheckExecutionsUpTo` (logic moved from
       `worker_steps.go:187-217`). A drained bucket defers by 1 min for a run in progress, or to
       the next tick for a new run (today's rule);
     - set `final` when `maxRunDuration` (default 30 min, max 2 h) has elapsed since
       `run_started_at`, or when the previous state was refused for size;
     - set the slice budget from `scheduling.bulk_slice_budget_ms` (default 10 s, max 60 s).
   - `Step`: refund unused units.
   - `Complete`:
     - refund unused units;
     - read the previous report and call `ReportDiffer.DiffAgainst` server-side (moved from
       `worker_steps.go:343-404`);
     - store the `crawl-report` attachment (keep 5);
     - write the result through the same path as `SubmitResult` (incidents, notifications), with
       the task's region and worker.
   - `Fail`: write the error result (`worker_steps.go:404-430`), so 3 failed slices or 4 lost
     leases still produce a result as today.
   - A state over 1 MiB is not saved. The next claim sets `final` (today's rule).
3. **Kind `crawl`, worker side** (`TaskExecutor`, capability `crawl`): build the checker from the
   registry, decode the config, and call `StepChecker.Step` with the claim's state, budget, units
   and `final`, under the worker's egress guard. Then call `step`, `finish` or `fail`.
   `StepChecker`, `StepInput` and `StepOutput` (`checkers/checkerdef/interface.go:179-224`) do
   not change.
4. **Remove the old path:**
   - the bulk-lane claim (`selectDueBulkJobs`, the bulk share of `claimJobs`);
   - `executeStepJob` and the rest of `worker_steps.go`;
   - `StepBackend` and `direct_steps.go`.

   Keep the `step_*` columns and lane 2: an old node reads them during a rolling upgrade, and
   lane 2 keeps old agents away from crawl jobs. Dropping the columns is a later spec, after a
   tagged release. Keep `checkjobsvc.BulkLeaseDuration` only if `worker_tasks` reuses it.
5. **Private locations:**
   - drop `ErrMultiStepPrivateLocation` and its check in `validateMultiStepPlacement`;
   - keep `ErrMultiStepSingleRegion`, since a run has one region;
   - auto placement still gives a crawl one region;
   - a private region needs no live agent at creation (as for any check). The dashboard shows
     the `crawl` capability as a hint, like ipv6 in `check-form.tsx`.
6. **Progress and "run now"** (`handlers/checkruns/service.go`, `handlers/checks/service.go`):
   - read the run's progress, slice count and start time from the active task instead of the
     `step_*` columns;
   - "run now" while a run is active keeps today's answer;
   - pausing or deleting a check cancels its active task (delete also cascades).
7. **Dashboard.**
   - The crawl form's region picker lists private regions.
   - A private region whose agent lacks `capabilities.crawl` is disabled, with a hint to upgrade
     the agent.
   - `data-testid`s on the new options.
8. **Docs:**
   - `wiki/features/multistep-checks.md`: rewrite "How a run works" around `worker_tasks`, and
     the "Cloud workers only" limit;
   - `web/docs/docs/` crawl page and `features/private-locations.md`: a crawl can run from a
     private location;
   - `CHANGELOG.md` note: crawl runs in flight at upgrade restart.

## Tests
- Materialization, both engines:
  - a due crawl job creates exactly one task, and leases the job to `task:<uid>`;
  - a second claim does not create a second task;
  - the task's end releases the job with `scheduled_at` anchored on `run_started_at`;
  - a non-crawl job is untouched.
- Rolling upgrade: a check_job leased to a task is not returned by the v0.38 bulk claim query
  (keep a copy of that query in the test as the old node's).
- Full run with the fake-free crawl checker against an `httptest` site:
  - several slices, with state round-tripping through the server;
  - a report stored and diffed against the previous one;
  - one result row;
  - units reserved and refunded (positive control: the bucket moves).
- Rate limit: a drained bucket defers without counting an attempt.
- Failures: 3 failed slices → one error result. An expired lease is reclaimed and the run goes
  on from the last saved state.
- `maxRunDuration` reached → the next claim is `final`.
- Agent path: an agent of `@dc1` claims a crawl of a `@dc1` check of its own org, and crawls a
  site reachable only from its test listener. Another org's agent never claims it.
- Validation: a crawl on `@dc1` is accepted, and a crawl on two regions is still refused.
- `checkworker`: no code path claims lane 2 from `check_jobs` any more (grep test or unit test).
- Dash0 e2e: the crawl form lists private regions, and one without `crawl` is disabled.
- Existing `checkcrawl` tests pass unchanged. `make test-postgres`.

## To verify
- Where `claimJobs` computes the bulk share (`checkjobsvc/service.go:242-293`), so the
  fast/slow budget stays the same once bulk is removed. The task loop takes the bulk slots
  instead.
- That the crawl config has no secret fields (`checkcrawl/config/config.go:79-91`, none on
  2026-10-22). If one appears, it must be sealed for agents as `check_jobs.config_sealed` is
  (`checkworker/backend/ws.go:316-330`).
- Who reads `step_*` today besides the worker (`handlers/checkruns`, `entitlements/check_rate.go:139`).
