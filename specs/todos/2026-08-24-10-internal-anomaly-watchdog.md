---
model: opus
effort: high
---

# The monitoring system cannot tell anyone it is broken — hourly internal watchdog reporting to designated operators

## Problem

On 2026-08-24, 526 of 627 enabled checks silently stopped executing for ~3
hours (region rename stranded their `check_jobs` — see spec `2026-08-24-08`).
During that window:

- **345 jobs were overdue by up to 3 hours** and nothing noticed. The job
  queue has no concept of "this row is unclaimable" — a job nobody can claim
  looks identical to a job whose turn hasn't come.
- **61 incidents stayed frozen in `active`** — including ~50 whose targets had
  recovered — because a check that stops being executed keeps its last state
  forever. Escalations had already fired at humans for outages that were over.
- The only alerting solidping has is *check-level* (incidents → escalation
  policies). There is **no channel for the platform to report on itself**, so
  the worst failure mode of a monitoring product — going blind — is exactly
  the one that produces zero signal. A user watching the dashboard sees green
  checks with quietly aging "last result" timestamps.

The platform already runs self-scheduled internal jobs (snooze sweep, stuck-job
reaper, agent GC, abandoned-result reaper — ensured at startup,
[job_startup.go:565](server/internal/jobs/jobtypes/job_startup.go#L565)) and
already knows how to deliver to a person through their configured contacts
(`user_contacts` / `user_notification_routes`,
[user_contact.go:96](server/internal/db/models/user_contact.go#L96), with
email / Slack DM / Telegram / webpush delivery job types under
`server/internal/jobs/jobtypes/`). What is missing is a job that checks the
platform's own vitals and a wiring from "big anomaly" to "named humans".

## Proposal

### 1. An hourly `platform-watchdog` internal job

New job type following the snooze-sweep self-rescheduling pattern
([job_snooze_sweep.go](server/internal/jobs/jobtypes/job_snooze_sweep.go)),
ensured at startup like its siblings, interval 1h (configurable). Each run
evaluates a small set of **detectors**, each returning zero or more anomalies:

1. **Dark region with assigned work** — the headline case from tonight.
   Reuses the ghost-region service function from spec `2026-08-24-09` (same
   computation, not a copy): any region where `jobs > 0 && liveWorkers == 0`,
   or where `jobsOverdue` exceeds a threshold with the oldest overdue older
   than N× its period. Severity scales with blast radius: 419 stranded jobs
   is a page-worthy anomaly, 1 job 90 seconds late is not — set the initial
   bar at ≥5 jobs overdue by ≥10 minutes, tunable via config.
2. **Fleet execution collapse** — results produced in the last completed hour
   vs. the hour one day before, per deployment. Tonight the rate fell from
   ~5,700/10min to ~1,300/10min instantly; a drop of more than 50% with the
   earlier baseline above a floor (say ≥100 results/h) is an anomaly even if
   no single region is technically dark. This detector is the catch-all for
   stranding causes this spec hasn't imagined.
3. **Stale active incidents** — active incidents whose check has produced no
   result in > max(3× period, 15 min): the "frozen incident" symptom, reported
   with count and the three oldest.

Detectors must be independent: one failing (query error) logs and does not
suppress the others. The run must be cheap — grouped queries only, and it
reuses the `2026-08-24-09` service so there is exactly one definition of
"dark".

### 2. Delivery to a designated operator list, via existing mediums

A system parameter (editable through the existing super-admin
`/system/parameters` CRUD,
[server.go:1447-1454](server/internal/app/server.go#L1447)):

```json
platform_watchdog: {
  "enabled": true,
  "recipients": ["<user-uid>", "..."],
  "minSeverity": "warning"
}
```

- Recipients are **user UIDs**; delivery goes through each user's own enabled
  `user_notification_routes` in position order — exactly what incident
  notifications do. No new medium, no hardcoded webhook: the operators
  already maintain their contact preferences and the watchdog inherits them.
  A recipient with no enabled routes is reported in the run log as
  undeliverable (this is an alerting path; silent drops are the bug this spec
  exists to kill).
- Empty/absent recipients with `enabled: true` → the job still runs and logs
  anomalies (observability via logs/metrics) but delivers nothing; log a WARN
  so the misconfiguration is visible.
- The message is a compact digest, one per run, not one per anomaly:
  detector name, severity, headline numbers, and — for dark regions — the
  ready-to-run remediation (`POST /system/regions/migrate` from spec
  `2026-08-24-08`, or the ghost listing from `2026-08-24-09`).

### 3. Anti-flood: report state transitions, not state

An anomaly that persists must not re-notify every hour. Persist a fingerprint
per anomaly (detector + subject, e.g. `dark-region:default`) with
first-seen/last-seen, using the existing `state_entries`/system-parameter
storage — whichever the implementer judges cleaner:

- **New** anomaly → notify.
- **Ongoing** → re-notify at most every 24h ("still broken since …").
- **Resolved** (condition gone this run) → notify recovery once, clear the
  fingerprint. Recovery notice matters: tonight the fix was applied at 03:30
  and confirming "watchdog sees 0 stranded jobs" is the operator's exit
  criterion.

### 4. Metrics

Export per-detector gauges (anomaly count, e.g. stranded-jobs total) through
the existing prommetrics registry so an external Prometheus can alert
independently of the in-band path — the watchdog notifying through the same
process it monitors is better than nothing but must not be the only signal.

### Tests

- Each detector against fixtures (including tonight's exact shape: jobs under
  a slug no live worker prefix-matches; frozen active incident with recovered
  target; halved result rate).
- Transition logic: new → notify, ongoing < 24h → silent, ongoing > 24h →
  re-notify, resolved → recovery notice exactly once.
- Delivery: recipients resolved to routes; user with no routes → run succeeds,
  undeliverable logged; `enabled: false` → no run side effects.
- A detector throwing does not block the others.

Out of scope: a dashboard surface for watchdog state (the digest + metrics
cover v1), per-org self-monitoring for customers (this is the *server
operator's* tool; org-facing "your checks look stale" is a separate product
decision), and any new delivery medium.
