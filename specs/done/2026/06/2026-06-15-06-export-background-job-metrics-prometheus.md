# Export background-job queue metrics to Prometheus

## Context

SolidPing has rich Prometheus coverage for **check execution** — counters,
duration/scheduling-delay histograms, per-stage timings, worker gauges, claim
outcomes (`server/internal/prommetrics/metrics.go`). It has **nothing** for the
**background-job queue** (the `jobs` table: email, webhook, notification,
aggregation, escalation_step, snooze_sweep, network_discovery, state_cleanup,
…). Today the only way to know whether that queue is healthy is to query the DB
directly.

This is the alerting-grade complement to the in-app admin Jobs page
([2026-06-15-05-admin-jobs-observability-page.md](2026-06-15-05-admin-jobs-observability-page.md)):
the UI is for a human poking around; metrics are for Grafana/alerting ("queue
depth climbing", "email jobs failing", "jobs running late"). They are
independent — this spec ships value with or without the page.

The instrumentation point already exists and is clean. `JobWorker.processNext`
(`server/internal/jobs/jobworker/worker.go:139`) is the single funnel every job
passes through. It already computes everything we need and even feeds an
internal stats struct:

```go
startTime := time.Now()
delay := startTime.Sub(job.ScheduledAt)        // scheduling delay (clamped ≥ 0)
...
jobErr := w.executeWithRecovery(jobExecCtx, jobRun, jctx)
w.stats.AddMetric(jobErr == nil, time.Since(startTime), delay)   // <- right here
return w.handleResult(jobExecCtx, logger, job, jobErr)            // success | retried | failed
```

So the per-job counters/histograms are a few lines next to an existing call
site. Queue **depth** is a point-in-time gauge and needs a small periodic
sampler (none exists today — the gauge setters in `prommetrics/recording.go`
such as `SetChecksConfigured`/`SetWorkersActive` are defined but not currently
wired to a loop, so a sampler is genuinely new work).

## Goal

Export Prometheus metrics for the background-job queue so operators can alert on
throughput, failure rate, latency, and backlog — following the exact naming and
helper conventions already used for check metrics.

## Behaviour / Metrics

All metrics prefixed `solidping_`, registered in `prommetrics.Register`'s
`allCollectors`, recorded via thin helpers in
`server/internal/prommetrics/recording.go` (matching the existing
`RecordExecution` / `Set*` style). Label cardinality kept low — **`job_type` is
bounded** by the `jobdef` enum; **never** use `job_uid`, `organization`, or
error text as labels.

### 1. `solidping_jobs_processed_total` (CounterVec) — REQUIRED
- Labels: `job_type`, `outcome` (`success` | `retried` | `failed`).
- Incremented once per processed job in `processNext` (covering the early
  `failed` exits for unknown-type / bad-config too, which currently call
  `w.stats.AddMetric(false, …)`).
- Helper: `RecordJobProcessed(jobType, outcome string)`.

### 2. `solidping_job_duration_seconds` (HistogramVec) — REQUIRED
- Labels: `job_type`, `outcome`.
- Observes `time.Since(startTime)` in seconds. Buckets tuned for job work (jobs
  span fast webhooks to slow discovery fan-outs), e.g.
  `{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}`.
- Helper: `RecordJobDuration(jobType, outcome string, seconds float64)`.

### 3. `solidping_job_scheduling_delay_seconds` (HistogramVec) — REQUIRED
- Label: `job_type`.
- Observes the existing `delay` (`startTime - job.ScheduledAt`, clamped ≥ 0) in
  seconds — the queue-lateness signal (mirrors
  `solidping_check_scheduling_delay_seconds`). Buckets
  `{0.1, 0.5, 1, 2, 5, 10, 30, 60, 300}`.
- Helper: `RecordJobSchedulingDelay(jobType string, seconds float64)`.

### 4. `solidping_jobs_queue_depth` (GaugeVec) — REQUIRED
- Label: `status` (`pending` | `running`).
- Backlog gauge. Set by a **periodic sampler** (default every 30s) running one
  cheap query — `SELECT status, COUNT(*) FROM jobs WHERE deleted_at IS NULL AND
  status IN ('pending','running') GROUP BY status` — and calling the setter for
  each status (explicitly zeroing absent statuses so the series drops to 0, not
  stale).
- Helper: `SetJobsQueueDepth(status string, count float64)`.
- Sampler lifecycle: a small `time.NewTicker(30s)` goroutine started where the
  job worker / app services start, ctx-cancelled on shutdown. Interval can be a
  constant for v1 (no new config key needed).

### Recording wiring
- `processNext` / `handleResult` (`jobworker/worker.go`): map the result to an
  outcome and call `RecordJobProcessed` + `RecordJobDuration` (both labelled by
  `job.Type` and outcome) and `RecordJobSchedulingDelay(job.Type, delay)`.
  Outcome must match the terminal status actually written by `UpdateJobStatus`
  (success / retried / failed) — derive it in `handleResult` so the metric and
  the DB row never disagree. The two early failure exits record outcome
  `failed`.
- The existing `w.stats.AddMetric(...)` internal accounting stays; this is
  additive.

## Out of scope
- The admin Jobs **UI** — separate spec (05). This spec is backend-only.
- Per-organization labels (cardinality risk on multi-tenant instances). Org-level
  breakdowns belong in the UI, not metrics.
- `check_jobs` lease/scheduler metrics — check execution is already covered by
  `solidping_check_*`; any check-schedule gauges (due-now / stalled) can be a
  later addition.
- Grafana dashboards / alert rules (can be a follow-up; the metrics are the
  contract).
- Gating: `/metrics` remains controlled by `SP_PROMETHEUS_ENABLED` as today — no
  change.

## Testing
Per `server/CLAUDE.md` (table-driven, `testify/require`, `t.Parallel()`):
- **Counter/histogram**: drive `processNext` (or a thin extracted recorder) for
  a success, a retryable failure (→ `retried`), a terminal failure (→ `failed`),
  and an unknown-type job; assert the right `(job_type, outcome)` series
  increment. Use `prometheus/client_golang/prometheus/testutil`
  (`CollectAndCount` / `ToFloat64`).
- **Scheduling delay**: a job with `ScheduledAt` in the past observes a positive
  delay; a future/just-now `ScheduledAt` clamps to 0.
- **Queue-depth sampler**: seed pending/running/success/soft-deleted rows, run
  one sample tick, assert `pending`/`running` gauges match counts and that a
  status that drops to zero is reset to 0 (not left stale). Soft-deleted and
  terminal rows are excluded.
- **Registration**: the new collectors are in `allCollectors` and
  `Register` stays panic-free (duplicate-registration guard).

## Implementation Plan
1. **`prommetrics/metrics.go`**: add the four collectors + `job_type` / `outcome`
   label consts; append to `allCollectors`.
2. **`prommetrics/recording.go`**: add `RecordJobProcessed`,
   `RecordJobDuration`, `RecordJobSchedulingDelay`, `SetJobsQueueDepth`.
3. **`jobworker/worker.go`**: derive outcome in `handleResult`; record
   processed/duration/scheduling-delay in `processNext` (incl. the early-exit
   failures).
4. **Queue-depth sampler**: a `COUNT(*) … GROUP BY status` query method on the
   job service + a 30s ticker goroutine wired into worker/app startup with
   ctx-cancel on shutdown; zero-fill absent statuses.
5. **Tests** per the Testing section; `make test`, `make lint`.
