# Multi-step checks (website crawl)

Specs: `2026-10-03-03-multistep-bulk-checks-website-crawl.md` (the crawl),
`2026-10-22-03-crawl-on-worker-tasks.md` (the run on worker tasks, private
locations). Mechanism: `internal/workertasks`, the agent routes in
[../api-specification/agents.md](../api-specification/agents.md) and the
`worker_tasks` table in [../database-model/jobs.md](../database-model/jobs.md).

A multi-step check runs as a series of short **slices** instead of one
execution. The `crawl` type is the only one today.

## How a run works

- The checker implements `checkerdef.StepChecker` (`Step` + `UnitsPerSlice`) and
  its `CheckTypeMeta` has `MultiStep: true` (registry test pins both).
- Its `check_jobs` rows are created on the **bulk lane** (`lane = 2`,
  `models.InitialLaneForType`). Cost never moves a job out of it. The row is
  the schedule only (period, "run now", billing projections): no runner claims
  lane 2 (`applyCloudRegionScope` and the agent claim both exclude it).
- A run is one `worker_tasks` row of kind `crawl` (`internal/crawltask`), one
  claim per slice. Cloud workers run it in-process (`DirectTaskClient`, up to
  `scheduling.bulk_lane_max` at once, default 4); agents over signed HTTP.
- **Start.** `workertasks.Claim` first calls the kind's `Materialize`, in the
  claim's transaction: due crawl jobs of the worker's scope (scheduled_at
  reached or a parked `run_requested_at`, no live lease, no active task)
  become a pending task (payload `checkJobUid`, region = the claiming worker's
  region), and the job is leased to it: `lease_worker_uid` stays NULL (it is a
  `workers` foreign key) and `lease_expires_at = now + maxRunDuration + 5 min`.
  A v0.38 node's bulk claim reads only the expiry, so it skips the job during a
  rolling upgrade. `run_started_at` is set by the first claim that runs a slice.
- **Each slice** (`PrepareClaim`): load the check and the job (gone, paused or
  re-typed cancels the task with `workertasks.ErrCancelTask`), reserve
  `UnitsPerSlice` tokens (`ReserveCheckExecutionsUpTo`, kept in
  `worker_tasks.units_granted`), set `final` once `maxRunDuration` (default
  30 min, max 2 h) has passed since `run_started_at`, and hand out the job's
  type and config with `scheduling.bulk_slice_budget_ms` (default 10 s, max
  60 s). A drained bucket defers the claim without counting an attempt: one
  minute for a run in progress, the job's next tick for a run not started yet.
- **Worker** (`crawltask.Executor`, capability `crawl`): decode the config, run
  `Step` under the budget + 1 s and the worker's egress guard, then:
  - not done: `step` with the state (`tasks/<uid>/step-state`, keep 2, 1 MiB
    cap) and the progress. The server refunds the unused units;
  - a state over 1 MiB is refused (413): the same claim re-runs `Step` with
    `Final` and finishes;
  - done: `finish` with the result, the report and the units used (sent again
    without the report if the body is over 1 MiB).
- **Finish** (`Complete`, server side): refund, read the previous report and
  call `ReportDiffer.DiffAgainst`, store the `checks/<uid>/crawl-report`
  attachment (keep 5), write the one result through
  `workers.Service.RecordResult` (incidents, notifications), with the job's
  region (the task's for a region-less job) and the task's worker when it has
  a `workers` row.
- **Failures** (`Fail`): 3 failed slices in a row, 3 claims of one slice
  without a report (lost leases), or a lost state write one error result.
- **End** (`End`, after done, failed or canceled): the job's lease is dropped
  and it is due at its next period anchored on `run_started_at`, or at a "run
  now" parked during the run.
- Pausing, deleting or re-configuring the check cancels its active task
  (`checks.Service.SetTaskCanceler`); a path without it (MCP) is caught at the
  next claim. `GET /checks/{uid}/run` reads the active task (`slice_count`,
  `progress`, `run_started_at`); `DELETE` cancels it.

## Limits

- One region per crawl (`ErrMultiStepSingleRegion`); auto placement gives it
  one cloud region. A private location is accepted with no live agent; the
  dashboard disables one whose agents report `capabilities.crawl = "no"` (an
  agent older than v0.41).
- System agents never run worker tasks: their `crawl` capability is stripped
  (`regions.StripTaskCapabilities`).
- `${param:}` in a crawl config resolves on cloud workers only, like any check
  on an org agent: an agent's claim carries the reference, never a value, and
  the run ends on an error result naming it.
- The `step_*` columns of `check_jobs` are no longer written. They stay for
  v0.38 nodes during a rolling upgrade; dropping them is a later spec.
- Billing: `UnitsPerRunHint` (crawl: `maxPages`) multiplies the check's weight in
  the checks-per-minute projection. Units are reserved and refunded server-side,
  for cloud workers and agents alike.
