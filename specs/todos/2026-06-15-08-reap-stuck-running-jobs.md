# Reap stuck "running" background jobs (timeout → retry → fail)

## Context

Background jobs in the `jobs` table can get stuck in `running` forever. The lifecycle today:

- A worker claims a pending job in a transaction and flips it to `running`
  ([`jobsvc/service.go:466-527`](../../server/internal/jobs/jobsvc/service.go)),
  setting `updated_at = now()` **once** at claim time.
- The worker runs the job, then writes a terminal status — `success`, `retried`, or `failed` —
  via `handleResult` ([`jobworker/worker.go:247-287`](../../server/internal/jobs/jobworker/worker.go)).
- There is **no heartbeat, no lease, and no timeout** on the `jobs` table. `updated_at` does not
  move again until the job finishes.

The failure mode: if the worker process dies, is redeployed, OOMs, or the job hangs, the row is
left in `running` and **nobody ever transitions it**. It is invisible to the claim query
(`status = pending`), so it neither completes nor gets retried — it just rots.

The existing retry machinery is healthy and we should reuse it, not reinvent it:
- `RetryJob` ([`jobsvc/service.go:565-588`](../../server/internal/jobs/jobsvc/service.go)) marks the
  original `retried`, clones it with `retry_count+1` and `previous_job_uid`, and schedules the
  clone with exponential backoff (1m / 5m / 15m).
- The retry cap is `maxRetryCount = 2` ([`jobworker/worker.go:32`](../../server/internal/jobs/jobworker/worker.go)),
  i.e. **3 attempts total** (original + 2 retries). When the cap is hit the job goes to `failed`.
- Statuses are `pending | running | success | retried | failed`
  ([`db/models/job.go:12-23`](../../server/internal/db/models/job.go)). **There is no `cancelled`
  status** — `CancelJob` is a soft-delete and only works on `pending` jobs
  ([`jobsvc/service.go:404-429`](../../server/internal/jobs/jobsvc/service.go)).
- The codebase already has the right pattern for a periodic maintenance task: a self-rescheduling
  global job (see `snooze_sweep` [`job_snooze_sweep.go:100-113`](../../server/internal/jobs/jobtypes/job_snooze_sweep.go)
  and `state_cleanup` [`job_state_cleanup.go:60-71`](../../server/internal/jobs/jobtypes/job_state_cleanup.go)),
  provisioned once at startup ([`job_startup.go:338-382`](../../server/internal/jobs/jobtypes/job_startup.go))
  and registered in [`registry.go`](../../server/internal/jobs/jobtypes/registry.go).

This builds directly on the recent job-observability work (admin jobs page `2026-06-15-05`,
Prometheus job metrics `2026-06-15-06`) — a reaper is what makes those dashboards actionable.

## My honest opinion

You asked, so here it is — I agree with the *intent* but want to change two things and flag one risk.

**1. The symptom diagnoses the cause — and it changes the framing.** The job pool runs only a
handful of concurrent runners (default 2). You can never have more rows in `running` than there are
runner slots *on live workers*. So "lots of jobs running" is almost certainly **orphaned jobs left
behind by worker restarts/crashes/deploys**, not jobs that are genuinely executing too long. That's
good news: the fix is mostly about *recovering orphans*, and a time-based sweep handles it cleanly.
"Without any update" can only mean "`status=running` and `updated_at` older than a threshold,"
because nothing currently bumps `updated_at` mid-run.

**2. Don't call the terminal state "cancellation."** This is my main pushback. "Cancelled" implies a
deliberate human decision to stop something. A job that kept getting stuck and exhausted its retries
isn't cancelled — it **failed**, and we already have `failed` for exactly that. Adding a `cancelled`
status would (a) fragment a status model that already owns this meaning with `failed`, (b) require a
migration to widen the `status` CHECK constraint on **both** Postgres and SQLite, and (c) need new
handling in every consumer (metrics, admin page, filters). My recommendation: **terminal state =
`failed`**, with a machine-readable reason in `output` (`{"error":"stuck: lease expired","reason":"stuck_timeout"}`)
so it's distinguishable from an error-failure without a new status. If we later decide the dashboard
must visually separate "abandoned" from "errored," that's a cheap follow-up driven by the reason
field — not a reason to invent a status now.

**3. The real risk is double-execution, not stuck rows.** Retrying a `running` job assumes its
worker is dead. If the worker is actually *alive but slow* (a legit long job, or a stall), the sweeper
re-queues it, a second worker picks up the clone, and **the job runs twice**. For idempotent jobs
(aggregation, cleanup) that's harmless. For `email` / `webhook` / `notification` jobs it means
**duplicate alerts to customers** — which is worse than a stuck row. We have no `worker_uid`/lease on
the `jobs` table (unlike `check_jobs`), so the sweeper *cannot* tell a dead worker from a slow one.
Two consequences for this spec:
   - The stuck-timeout must be **generously larger than the slowest legitimate job**, and configurable.
   - We must add a status guard on the worker's terminal write so a worker that lost its job to the
     reaper cannot silently clobber the reaper's decision (see Implementation §4). It can't prevent
     the duplicate side-effect, but it prevents a corrupted status and makes the collision observable.
   - A fully correct fix (lease + `worker_uid` on jobs, mirroring `check_jobs`) is the right V2 and is
     scoped out below.

**4. Reconciling "third retry."** The system today is 2 retries / 3 attempts. Rather than introduce a
second, conflicting retry-limit concept, a stuck job should ride the **existing** retry chain and
counter. When `retry_count` reaches `maxRetryCount` it goes `failed`. If you genuinely want one more
attempt, bump `maxRetryCount` to 3 — one knob, no parallel concept. (Decision point below.)

Net: same feature you asked for, but terminal = `failed` (not "cancelled"), reusing the existing
retry chain, with explicit guardrails against double-execution.

## Goals

- A periodic sweeper detects jobs stuck in `running` (claimed but `updated_at` older than a
  configurable timeout) and recovers them.
- Recovery reuses the existing retry chain: stuck job → `retried` + backoff clone, until the existing
  `maxRetryCount` is reached, after which it becomes `failed` with reason `stuck_timeout`.
- The terminal write from a worker is guarded so it can no longer overwrite a status that the reaper
  has already changed (no silent status corruption on lease collision).
- The number of jobs reaped is observable (log + Prometheus counter), because a spike indicates worker
  instability, not normal operation.
- No schema migration required for the MVP (reuse `status` + `updated_at`).

## Out of scope (candidates for a follow-up V2)

- Adding `worker_uid` + `lease_expires_at` to the `jobs` table and correlating with
  `workers.last_active_at` to distinguish dead vs. slow workers. This is the *correct* long-term fix
  for double-execution; it needs a migration and is a separate spec.
- A first-class `cancelled` job status and any UI to show it.
- Per-job-type custom timeouts (one global timeout for now).
- Making individual job handlers idempotent (tracked separately if double-execution proves real).
- User-facing manual "cancel a running job" action.

## Design

A new self-rescheduling global job type, `stuck_job_reaper`, mirroring `snooze_sweep`:

1. On each run, in a single statement per job, find jobs where
   `status = 'running' AND updated_at < now() - timeout AND deleted_at IS NULL`.
2. For each, decide via the **existing** rule (`job.RetryCount < maxRetryCount`):
   - **Retries remaining** → call the existing `RetryJob` path (marks original `retried`, schedules a
     backoff clone). The clone re-enters the normal pending queue.
   - **Cap reached** → `UpdateJobStatus(..., failed, {"error":"stuck: no update within <timeout>","reason":"stuck_timeout"})`.
3. Increment a `solidping_jobs_reaped_total{outcome="retried|failed"}` counter and log a warning with
   the count.
4. Reschedule itself one interval out.

**Timeout & interval** (configurable, koanf + `SP_` env; remember multi-word keys need the manual
reader per [[project_koanf_env_quirk]]):
- `jobs.stuck_timeout` — default **15m** (must exceed the slowest legitimate job; network discovery
  and aggregation are the long ones to sanity-check).
- `jobs.reaper_interval` — default **1m** (matches `snooze_sweep`).

**Concurrency safety of the reap itself:** the transition must be atomic and guarded so two reaper
runs (or a reaper racing the original worker) can't both act:
```sql
UPDATE jobs SET status='retried'/'failed', updated_at=now()
 WHERE uid = ? AND status='running' AND updated_at < ?   -- re-assert the stuck condition
```
If `RowsAffected = 0`, the job already moved on — skip it. Reuse the same pattern `claimNextJob`
already uses for SQLite optimistic locking ([`jobsvc/service.go:496-518`](../../server/internal/jobs/jobsvc/service.go)).

## Implementation

### 1. Register the new job type
- Add `JobTypeStuckJobReaper JobType = "stuck_job_reaper"` to
  [`jobdef/types.go`](../../server/internal/jobs/jobdef/types.go) (alongside `JobTypeSnoozeSweep`).
- Add the `case` to [`jobtypes/registry.go`](../../server/internal/jobs/jobtypes/registry.go).
- If the job `type` CHECK constraint still restricts values, confirm `stuck_job_reaper` passes it
  (migration `024_fix_jobs_type_constraint` relaxed this; verify no new migration is needed).

### 2. New job runner `jobtypes/job_stuck_job_reaper.go`
Model it on [`job_snooze_sweep.go`](../../server/internal/jobs/jobtypes/job_snooze_sweep.go):
- `StuckJobReaperJobDefinition` / `...JobConfig{}` (empty) / `...JobRun`.
- `Run`: read timeout from config/parameters, call a new service method
  `ReapStuckJobs(ctx, timeout)` that returns counts `{retried, failed}`, emit metrics + a
  `WarnContext` when count > 0, then `rescheduleSelf` one `reaper_interval` out (guard
  `jctx.Services != nil && jctx.Services.Jobs != nil`, like the others).

### 3. Service method `ReapStuckJobs` in `jobsvc`
- Select stuck candidates (`status=running`, `updated_at < now()-timeout`, `deleted_at IS NULL`).
- For each, branch on `RetryCount < maxRetryCount`. **Note:** `maxRetryCount` currently lives in the
  `jobworker` package ([`worker.go:32`](../../server/internal/jobs/jobworker/worker.go)); either export
  it or move it somewhere both packages share so the reaper and worker agree on the cap. Don't
  duplicate the literal.
- Retries-remaining branch: reuse `RetryJob` (it already does the atomic `retried` flip + backoff
  clone). Cap-reached branch: `UpdateJobStatus(... failed, reason)`.
- Both branches must re-assert `status='running' AND updated_at < ?` in the WHERE and check
  `RowsAffected` so a concurrent transition is a no-op, not a double-action.

### 4. Guard the worker's terminal write (anti-clobber)
In `UpdateJobStatus` ([`jobsvc/service.go:529-563`](../../server/internal/jobs/jobsvc/service.go)) the
update is `WHERE uid = ?` with no status guard, so a slow worker finishing after the reaper already
moved the job would overwrite the reaper's decision. Add an **optional** `expectedStatus` guard (or a
dedicated `CompleteRunningJob` helper used by `handleResult`) that asserts `status='running'`. If
`RowsAffected = 0`, the worker lost its job to the reaper — log a warning
(`"job lease lost, result discarded"`) and emit a metric, but do not error the worker. This is the
single most important correctness fix in the spec.

### 5. Provision the reaper at startup
Add `ensureStuckJobReaperJob` to [`job_startup.go`](../../server/internal/jobs/jobtypes/job_startup.go)
following `ensureSnoozeSweepJob` ([lines 362-382](../../server/internal/jobs/jobtypes/job_startup.go)),
and call it from the startup `Run` alongside the others (around
[lines 61-66](../../server/internal/jobs/jobtypes/job_startup.go)). The startup pass also gives us an
immediate first sweep after a deploy, which is exactly when orphans appear.

### 6. Metric
Add `solidping_jobs_reaped_total{outcome}` next to the existing job-outcome metrics introduced by
`2026-06-15-06-export-background-job-metrics-prometheus`. Reuse that registration site/pattern.

### 7. Config
- Add `jobs.stuck_timeout` (15m) and `jobs.reaper_interval` (1m) to the config struct + defaults, and
  to the manual `SP_` env reader (multi-word keys, per [[project_koanf_env_quirk]]).

## Open questions / decisions for the user

1. **Terminal status: `failed` (recommended) or a new `cancelled` status?** Spec is written for
   `failed` + `reason:"stuck_timeout"`. Choosing `cancelled` adds migrations (PG + SQLite CHECK) and
   consumer changes — say the word and I'll scope that instead.
2. **Retry budget: keep `maxRetryCount = 2` (3 attempts) or bump to 3** to match your literal "third
   retry"? Default in this spec is to keep the existing cap.
3. **`stuck_timeout` default of 15m** — is any legitimate job (network discovery? a slow webhook?)
   expected to run longer? If so we raise the default or make it per-type (V2).

## Verification

- **Unit/integration (table-driven, `testify/require`, `t.Parallel()` per `server/CLAUDE.md`):**
  - Insert a job with `status=running`, `updated_at` older than timeout, `retry_count < cap` → after
    `ReapStuckJobs`, original is `retried` and a backoff clone exists with `retry_count+1`.
  - Same but `retry_count == cap` → original is `failed` with `output.reason == "stuck_timeout"`.
  - A job `running` but `updated_at` *within* timeout is **untouched**.
  - A `pending`/`success`/`failed` job is never reaped.
  - Anti-clobber: reaper marks a job `retried`, then the original worker calls the guarded completion
    → `RowsAffected == 0`, status stays `retried`, warning logged.
  - Run on both SQLite and Postgres (testcontainers) — the optimistic guard must behave on both.
- **Manual:** with the server running, `UPDATE jobs SET status='running', updated_at = now() - interval '30 minutes'`
  on a real pending job, wait one reaper interval, confirm it transitions and the
  `solidping_jobs_reaped_total` counter increments and a warning is logged.
- `make lint` and `make test` pass.

## Files referenced

- `server/internal/jobs/jobdef/types.go` — new job type constant
- `server/internal/jobs/jobtypes/registry.go` — register the type
- `server/internal/jobs/jobtypes/job_stuck_job_reaper.go` — **new** runner
- `server/internal/jobs/jobtypes/job_startup.go` — provision at startup
- `server/internal/jobs/jobtypes/job_snooze_sweep.go` — pattern to copy
- `server/internal/jobs/jobsvc/service.go` — `ReapStuckJobs`, guarded `UpdateJobStatus`, reuse `RetryJob`
- `server/internal/jobs/jobworker/worker.go` — `maxRetryCount` (share it), guarded terminal write in `handleResult`
- `server/internal/db/models/job.go` — status enum (no change for MVP)
- Config struct + `SP_` env reader — `jobs.stuck_timeout`, `jobs.reaper_interval`
</content>
</invoke>
