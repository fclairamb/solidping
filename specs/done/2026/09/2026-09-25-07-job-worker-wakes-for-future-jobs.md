---
model: sonnet
effort: medium
---

# Jobs scheduled for later start up to 5 minutes late, so every "per-minute" sweep runs about every 5 minutes

## Problem

`GetJobWait`
([jobsvc/service.go:585](server/internal/jobs/jobsvc/service.go#L585)) wakes a
runner in only two ways: a `job.created` notification, or a fixed 5-minute
fallback ticker.

A job created with a **future** `scheduled_at` sends its notification at insert
time, when it is not due yet. The claim finds nothing, and the runner goes back
to waiting. Nothing arms a timer for the moment the job becomes due, so it waits
for the next unrelated notification or the 5-minute ticker.

Every self-rescheduling sweep inserts its next run at `now + interval`, so it
hits this on every cycle.

Measured in prod over the 12 hours to 2026-09-24 22:40 UTC:

| Jobs | Intended | Actual |
|---|---|---|
| `slo_burn_eval`, `snooze_sweep`, `stuck_job_reaper`, `degraded_eval` | every minute | 3 to 4 runs per hour each, started **240 s late on average** (max 350 s) |
| `platform_watchdog`, `uptime_report` | hourly | p50 9 s late, **max 299 s** |
| `notification`, `email`, `incident_resolution_notice` (due immediately) | now | p50 0.3 s (fine) |

At 22:35-22:39 UTC this looked like a 4.5-minute stall of the whole queue. The
runner woke at 22:39:50.4, right after the next job it happened to know about
(a retry due at 22:39:50.2).

Consequences:
- SLO burn alerts and degraded detection run about 5× less often than designed.
- Delayed escalation steps and retries can fire up to 5 minutes late.
- The per-minute sweeps proposed in `2026-09-25-02` and `2026-09-25-03` would
  silently run every 5 minutes.

## Proposal

1. After a no-rows claim, `GetJobWait` reads the earliest pending
   `scheduled_at` (`status = pending AND deleted_at IS NULL AND
   scheduled_at > now()`). It waits on a timer until then, capped by the
   existing 5-minute fallback, and recomputes on every loop. A `job.created`
   wake-up still interrupts the wait, and the recomputed timer picks up a new
   earlier job.
2. Floor the timer, for example at 250 ms. If a due job is skipped because
   another runner holds it (`FOR UPDATE SKIP LOCKED`), the computed delay is
   ≤ 0, and without a floor the loop would spin.
3. Check that the `min(scheduled_at)` read uses an index on
   `(status, scheduled_at)` in both databases; add one if not. It runs once per
   idle wake-up per runner.
4. Publish `solidping_job_start_delay_seconds{type}` (histogram of
   `claimed_at - scheduled_at`) so a regression shows up in metrics instead of
   in a spec. The existing `Processing stats` log line reports
   `averageDelaySeconds≈250000` (about 3 days), which cannot be right. Find what
   it averages, and fix or drop it.

### Tests

- A job scheduled at `now + 2 s` is claimed within ~0.5 s of being due, on
  SQLite and Postgres. Today it waits for the 5-minute ticker. Use the
  package's existing time hooks or a short real delay; no 5-minute test.
- A self-rescheduling sweep with a 1-minute interval runs about 60 times per
  hour in an integration test with a shortened interval.
- A new job created with an earlier `scheduled_at` than the one the runner is
  waiting for is claimed at its own due time.
- A due job held by another runner does not make the loop spin (bounded number
  of claim attempts per second).
- `getjobwait_leak_test.go` still passes: no listener leak.
