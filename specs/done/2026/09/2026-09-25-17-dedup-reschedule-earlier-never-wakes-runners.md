---
model: sonnet
effort: medium
---

# Pulling an already-queued job earlier through CreateJob's dedup path never wakes a sleeping runner

## Problem

`CreateJob` (`server/internal/jobs/jobsvc/service.go:199`) first calls
`findAndUpdateExistingJob` (`service.go:254`). When a PENDING job with the same
org + type + config already exists, it overwrites that row's `scheduled_at` in
place and returns `found=true`. `CreateJob` then only calls `publishJobsHint`
(`service.go:219`), a dashboard realtime hint. It never calls
`s.notifier.Notify(ctx, eventTypeJobCreated, "{}")`.

`createNewJob` (`service.go:298`) does notify, when the new job is due within 15
minutes (`service.go:321`).

`GetJobWait` (`service.go:613`) sleeps until the earliest still-future pending
job (`nextPendingWait`, `service.go:659`, from spec 2026-09-25-07) or until a
`job.created` wake-up. The wait is computed once per loop iteration. So when a
runner is asleep on a timer set for job A (due in 4 minutes) and a caller pulls
job B from "in 5 minutes" to "now" through the dedup path, nothing re-enters the
loop. B runs about 4 minutes late, once A's timer fires.

This was confirmed while writing the tests for 2026-09-25-07. A test that hit
the dedup path (same type, empty config) saw `GetJobWait` stay asleep until its
existing timer expired. The regression test had to use a distinct config to go
through the insert path instead.

### Who actually hits the dedup path

No production caller passes `BounceDelay`. It only appears in `service.go`
itself (`:38`, `:234`). The path is still reached often, mostly through `nil`
options, which means "schedule now":

- `job_startup.go` enqueues every self-rescheduling sweep with `nil` options
  (`:229`, `:250`, `:272`, `:297`, `:613`, `:636`, `:660`, `:685`, `:707`,
  `:732`, `:756`, `:781`). If the previous run already queued the next one for
  later, startup pulls it to now, silently.
- `handlers/jobs/handler.go:70` (API-triggered job) and
  `handlers/discovery/service.go:127` use `nil`. A user re-triggering a job that
  is already queued for later gets the same silent pull-forward.
- Callers with an explicit `ScheduledAt` (`self_rescheduling_sweep.go:111`,
  `job_aggregation.go:130`, `job_uptime_report.go:214`, `job_agent_gc.go:255`,
  `job_demo_cleanup.go:271`, `job_custom_domain_verify.go:241`,
  `job_incident_publish.go:147`, `job_escalation_step.go:1672`,
  `unack_notice.go:195`, `testapi/generate_data.go:112`) can move a row either
  way, depending on what is already queued.

## Proposal

Emit the same `job.created` notify from the dedup path, only when the update
makes the job due sooner than it was **and** it lands inside the 15-minute
window `createNewJob` already uses.

1. In `findAndUpdateExistingJob`, keep the row's previous `scheduled_at` before
   overwriting it, and return it (or a boolean `movedEarlier`) to `CreateJob`.
2. In `CreateJob`'s `found` branch, notify when all of these hold:
   - `newScheduledAt.Before(oldScheduledAt)`. A bounce that pushes the job out,
     or leaves it where it was, fires nothing. This is the job-storm guard.
   - `oldScheduledAt.After(now)`. If the job was already due, it is already
     claimable and a sleeping runner would not be sleeping on it. Notifying
     adds nothing.
   - `time.Until(newScheduledAt) <= 15*time.Minute`, the same gate as
     `createNewJob`.
3. Put the 15-minute threshold in one named constant and use it in both places,
   so the two paths cannot drift.
4. Keep `publishJobsHint` exactly as it is.

### Debounce semantics

Adding a notify does not change which job runs or when. It only makes a
sleeping runner re-run `claimNextJob` + `nextPendingWait`, which is idempotent
(the `<-wakeup` branch at `service.go:640` already treats a wake for a later job
as harmless). No caller relies on "the rescheduled job must *not* be picked up
before the runner's current timer". The implementer should still read each
`ScheduledAt:` caller listed above and confirm that in the PR description.

### Out of scope (note, don't fix)

- The dedup path moves `scheduled_at` **later** too. A caller with a later
  `ScheduledAt` postpones an already-queued earlier job. That is the current
  "bounce" contract (`service.go:38`) and it is not changed here. If it looks
  wrong for a specific caller, file a separate spec.
- SELECT-then-UPDATE in `findAndUpdateExistingJob` is not atomic, so two
  concurrent `CreateJob` calls can both miss and insert twice. This is
  pre-existing and unrelated to the wake-up.

## Tests

Add them next to the existing wake tests (`getjobwait_wake_test.go` for SQLite,
and `getjobwait_wake_postgres_test.go` if the Postgres notifier path differs).

1. **Dedup pull-forward wakes the runner.** Queue job A (type X, config `{}`)
   due in ~10s. Queue job B (type Y, config `{}`) due in ~60s. Start
   `GetJobWait`. It must be asleep on A's timer. Then call
   `CreateJob(type Y, config {}, nil)`. This hits the dedup path and pulls B to
   now. Assert `GetJobWait` returns B well before A is due (for example within
   2s). Assert that it went through the dedup path: same UID as the original B,
   and one pending row of type Y. Without that check, the test can pass through
   the insert path and prove nothing.
2. **Pushing later does not notify.** Use a counting/fake `EventNotifier`, or
   the `claimAttempted` hook (`service.go:174`). Queue a job due in 30s, then
   `CreateJob` it again with `ScheduledAt` = now + 5min. Assert that no
   `job.created` was emitted and no extra claim attempt happened.
3. **Positive control for test 2.** The same setup with `ScheduledAt` = now +
   5s must emit exactly one notify. This proves the counter can see a notify at
   all.
4. **Beyond the window does not notify.** Pull a job from now + 2h to now +
   30min. It moves earlier but lands outside the 15-minute gate, so there is no
   notify.
