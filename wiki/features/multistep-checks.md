# Multi-step checks (website crawl)

Spec: `specs/todos/2026-10-03-03-multistep-bulk-checks-website-crawl.md`.

A multi-step check runs as a series of short **slices** instead of one
execution. The `crawl` type is the only one today.

## How a run works

- The checker implements `checkerdef.StepChecker` (`Step` + `UnitsPerSlice`) and
  its `CheckTypeMeta` has `MultiStep: true` (registry test pins both).
- Its `check_jobs` rows are created on the **bulk lane** (`lane = 2`,
  `models.InitialLaneForType`). Cost never moves a job out of it.
- Claim order in one transaction: slow, bulk, fast. Bulk is bounded by
  `scheduling.bulk_lane_max` (default 4 per worker) and shares
  `poolSize − fast_lane_reserved` with slow, slow first
  (`scheduling.LaneLimits`). Bulk is only claimed when due (no claim-ahead) and
  leases for `checkjobsvc.BulkLeaseDuration` (2 min), not a period.
- One slice: reserve `UnitsPerSlice` tokens (`ReserveCheckExecutionsUpTo`),
  run `Step` under `scheduling.bulk_slice_budget_ms` (default 10 s, max 60 s)
  + 1 s, refund the unused units.
  - Not done: the state goes to a `checks/<uid>/step-state` attachment (envelope
    `{v, runUid, checkType, payload}`, keep 2, 1 MiB cap, group `check-state`)
    and `SubmitStep` moves `step_state_file_uid`, bumps `step_count` and makes
    the job due now. No result row.
  - Done: the report goes to `checks/<uid>/crawl-report` (keep 5), the result is
    diffed against the previous report (`ReportDiffer`), and `SubmitResult`
    with `StepRunUID` clears the run. The next run is anchored on
    `step_run_started_at`.
- Every step write is fenced on `lease_worker_uid` and on the run uid the claim
  saw, so a worker whose lease expired changes nothing.
- A state over 1 MiB is not saved: the slice re-runs `Step` with `Final`.
  `maxRunDuration` (default 30 min, max 2 h) also forces `Final`.
- Three failed slices in a row (`step_failures`) end the run with an error
  result.
- A drained token bucket defers the slice: one minute for a run in progress,
  the next tick otherwise.

## Limits

- Cloud workers only: agents never claim bulk jobs and may not upload either
  attachment kind. Validation refuses a crawl on a private location or on
  several regions; auto placement gives it one region.
- Billing: `UnitsPerRunHint` (crawl: `maxPages`) multiplies the check's weight in
  the checks-per-minute projection.
