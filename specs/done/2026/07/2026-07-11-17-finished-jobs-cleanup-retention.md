# `jobs` table grows unbounded — add a daily `jobs_cleanup` job (soft-delete finished at 48h, hard-delete soft-deleted at +24h)

## Problem

Nothing ever hard-deletes terminal `jobs` rows. `DeleteJob`
([postgres.go:2060](../../server/internal/db/postgres/postgres.go)) and the
jobsvc cancel paths are all *soft* deletes (`deleted_at`), and no
age-based purge exists anywhere — so the table grows monotonically forever,
even in perfectly healthy operation:

- `snooze_sweep` and `stuck_job_reaper` each self-reschedule **every
  minute** ([job_snooze_sweep.go:18](../../server/internal/jobs/jobtypes/job_snooze_sweep.go),
  [job_stuck_job_reaper.go:18](../../server/internal/jobs/jobtypes/job_stuck_job_reaper.go))
  → ~2,880 terminal rows/day with zero checks configured.
- `aggregation` adds one row per rollup step per org
  ([job_aggregation.go:109](../../server/internal/jobs/jobtypes/job_aggregation.go)).
- Every notification/email/webhook adds a row.

Dev evidence (see sibling spec
[2026-07-11-16-aggregation-poison-pill-loop.md](2026-07-11-16-aggregation-poison-pill-loop.md)):
`jobs` reached **2.2 GB / 8.5M rows**, all but 13 of them `success`. That
case was pathological, but the steady-state trend is the same — only slower.
The queue index (`idx_jobs_queue`, partial on `pending`) keeps scheduling
fast, but `idx_jobs_organization` (partial on `deleted_at IS NULL`) and
every jobs-admin listing scan an ever-growing set.

Additionally, the retention knobs that do exist (aggregation) live only in
koanf config ([config.go:364-366](../../server/internal/config/config.go)) —
changing them requires a restart, and there is no single place where an
operator can see and tune the platform's data-retention behavior.

## Proposal

### 1. New `jobs_cleanup` maintenance job

Follow the `state_cleanup` pattern exactly
([job_state_cleanup.go](../../server/internal/jobs/jobtypes/job_state_cleanup.go)):
a global job (`organization_uid` NULL, created with org `""`), registered in
the jobtypes registry, self-rescheduling with `delay = 24h`, bootstrapped
from the startup job via a new `ensureJobsCleanupJob` alongside
`ensureStateCleanupJob`
([job_startup.go:345](../../server/internal/jobs/jobtypes/job_startup.go)).
Its own terminal rows flow through the same two-stage lifecycle —
self-cleaning.

### 2. Two-stage lifecycle

Each run performs two passes:

**Stage 1 — soft-delete finished jobs done for > 48h.**
Rows with `status IN ('success', 'retried', 'failed')`, `deleted_at IS
NULL`, and `updated_at < now() - 48h` (updated_at ≈ terminal-transition
time) get `deleted_at = now()`. This immediately drops them out of the
`deleted_at IS NULL` partial indexes — notably `idx_jobs_organization`
([001_v0_1_0.up.sql:497](../../server/internal/db/postgres/migrations/001_v0_1_0.up.sql)),
the index behind org job listings — so the hot working set stays small
while the rows remain recoverable/inspectable for one more day.
(`idx_jobs_queue` already excludes them by `status = 'pending'`.)

**Stage 2 — hard-delete jobs soft-deleted for > 24h.**
Rows with `deleted_at IS NOT NULL AND deleted_at < now() - 24h` are
physically deleted, subject to the FK guard (§4). This covers both rows
aged out by stage 1 and user-cancelled jobs (which arrive with
`deleted_at` already set) — anything soft-deleted disappears for good 24h
later. Anchor on `deleted_at`, not `updated_at`: we set it ourselves in
stage 1, so stage 2's timing is deterministic and immune to incidental
`updated_at` touches.

Total lifetime of a finished job: **48h visible + 24h soft-deleted = 72h.**

Never touch `pending`/`running` rows (without `deleted_at`) regardless of
age — recovering those is the stuck-job reaper's mandate, not cleanup's.

Both windows resolve from the new `performance.*` global parameters (§3)
at each run — no restart needed to change them.

### 3. Global `performance.*` parameter group for all retention knobs

Introduce a `performance` group of **global parameters** resolved through
the existing `systemconfig` mechanism
([systemconfig.go](../../server/internal/systemconfig/systemconfig.go)):
precedence env `SP_*` → DB parameter row with `organization_uid IS NULL` →
hardcoded default. This makes every retention/performance knob tunable at
runtime from one namespace, without a restart. New `ParameterKey`
constants (unit suffix in the name so values are unambiguous integers):

| Key | Meaning | Default |
|---|---|---|
| `performance.aggregation_retention_raw_hours` | hours of raw results kept before the hourly rollup | 24 |
| `performance.aggregation_retention_hour_days` | days of hourly rollups kept before the daily rollup | 30 |
| `performance.aggregation_retention_day_months` | months of daily rollups kept before the monthly rollup | 12 |
| `performance.jobs_soft_delete_hours` | hours after a job finishes before it is soft-deleted (stage 1) | 48 |
| `performance.jobs_hard_delete_hours` | hours after soft-deletion before a job is hard-deleted (stage 2) | 24 |

- The three aggregation keys **replace** the koanf-only
  `aggregation.retention_raw/hour/day` fields
  ([config.go:364-366](../../server/internal/config/config.go));
  `retentionFromConfig`
  ([job_aggregation.go:251-273](../../server/internal/jobs/jobtypes/job_aggregation.go))
  switches to reading systemconfig at job-run time (see spec -16, which
  touches the same function). Keep the old koanf fields working for one
  release as a fallback layered between the DB parameter and the hardcoded
  default, with a deprecation log line when they're the resolved source.
- Env overrides follow the standard mapping
  (`SP_PERFORMANCE_AGGREGATION_RETENTION_RAW_HOURS`, …). These are
  multi-word koanf keys — wire them into the manual `SP_*` env reader,
  which koanf doesn't map automatically.
- Values are validated like the existing retention validation
  (`>= 1`, [config.go:78-79](../../server/internal/config/config.go));
  invalid DB parameter values log a warning and fall through to the
  default rather than breaking the job.
- Future candidates for the same group (not in this spec's scope, listed
  so the namespace is designed for them): cleanup batch size, aggregation
  discovery limit, maintenance-job intervals, and per-org retention
  overrides via same-key org parameter rows (the
  `auth.session_max_duration` pattern) — a natural fit for SaaS
  entitlement tiers later.

### 4. Respect the `previous_job_uid` FK on hard delete — no migration needed

`previous_job_uid uuid references jobs(uid)` has **no `ON DELETE` action**
([001_v0_1_0.up.sql:490](../../server/internal/db/postgres/migrations/001_v0_1_0.up.sql)),
so hard-deleting a `retried` job still pointed at by its (newer) successor
violates the FK. Rather than altering the constraint to `ON DELETE SET
NULL` (a full table rebuild on SQLite), stage 2 excludes referenced rows:

```sql
AND uid NOT IN (SELECT previous_job_uid FROM jobs WHERE previous_job_uid IS NOT NULL)
```

Chains then drain tail-first across consecutive daily runs: the successor
is hard-deleted one run, the predecessor becomes unreferenced and goes the
next. Stage 1 needs no guard — soft deletes don't touch the FK.

### 5. DB layer

Two new methods in both backends
([postgres.go](../../server/internal/db/postgres/postgres.go),
[sqlite.go](../../server/internal/db/sqlite/sqlite.go) — keep parity per
`/sync-pg-to-sqlite`):

- `SoftDeleteFinishedJobs(ctx, before time.Time, limit int) (int64, error)`
  — stage 1 UPDATE.
- `DeleteSoftDeletedJobs(ctx, before time.Time, limit int) (int64, error)`
  — stage 2 DELETE with the FK-exclusion subquery.

Run both in batches (e.g. `LIMIT 10_000` via `uid IN (SELECT … LIMIT n)`
loop until short batch) so a large backlog never holds a long transaction.
Log per-run counts for both stages (`Soft-deleted finished jobs count=N`,
`Deleted soft-deleted jobs count=M`), matching state_cleanup's logging.

### 6. Tests

Table-driven, both backends:

- Stage 1: soft-deletes old `success`/`retried`/`failed`; keeps recent
  terminal rows and `pending`/`running` of any age; already-soft-deleted
  rows untouched by stage 1.
- Stage 2: hard-deletes rows soft-deleted > 24h ago (including
  user-cancelled ones); keeps freshly soft-deleted rows.
- Full lifecycle: a finished job survives at 47h, is soft-deleted after
  48h, still queryable (with `deleted_at`) until 72h, gone after.
- Retry chain: successor referenced predecessor — predecessor's hard
  delete is deferred until the successor is gone (drains over two runs).
- Batching: backlog larger than one batch fully drains in one run.
- Parameters: a `performance.jobs_soft_delete_hours` DB parameter row
  (org NULL) changes the window without restart; `SP_*` env wins over the
  DB row; invalid value falls back to default with a warning.
- Startup bootstrap: `ensureJobsCleanupJob` doesn't stack duplicates
  (same dedupe behavior as the other `ensure*` jobs).

## Notes / open questions

- **Disk is not reclaimed by DELETE** — plain autovacuum reuses the space
  for new rows, which is fine in steady state. The existing 8.5M-row dev
  backlog is faster to clear via the manual purge + `VACUUM FULL` runbook
  in spec -16; this job's purpose is preventing recurrence.
- **Retention defaults**: is 48h right for `failed` jobs? Keeping failures
  visible longer (e.g. 7 days) would help debugging — worth a decision;
  with §3 in place a per-status split would just be additional
  `performance.*` keys later.
- Jobs-admin surfaces (`sp jobs-admin`, handlers/jobsadmin) lose history
  past 48h (visible) / 72h (at all) — acceptable for a task queue, but
  worth stating in the CLI help. If jobs-admin filters `deleted_at IS
  NULL`, consider an `--include-deleted` flag to inspect the grace window.

## Out of scope

- `events` and `incident_notifications` are also append-only with no
  retention — same follow-up family, separate spec (their windows should
  land in the `performance.*` group too).

## Implementation Plan

Scope note: the `performance.*` parameter foundation (keys, env→DB→default
resolver `resolveRetentionTier`, deprecated-koanf fallback) already landed with
spec -16 for the three aggregation keys. This spec reuses that resolver as-is and
adds only the two jobs-cleanup knobs plus the job itself. **No migration** — the
`jobs` table already has `deleted_at`, `updated_at`, `status`, `previous_job_uid`
and `idx_jobs_previous` (§4 "no migration needed").

1. **systemconfig keys (§3).** Add `KeyPerfJobsSoftDeleteHours =
   "performance.jobs_soft_delete_hours"` and `KeyPerfJobsHardDeleteHours =
   "performance.jobs_hard_delete_hours"` next to the existing perf keys. These
   resolve at job-run time via the shared `resolveRetentionTier` (env
   `SP_PERFORMANCE_JOBS_SOFT_DELETE_HOURS` / `..._HARD_DELETE_HOURS` → global DB
   parameter → hardcoded default), same as the aggregation perf keys — read
   directly with `os.Getenv` at run time, not wired into config.go's koanf
   reader (there is no koanf field for them). Invalid (< 1) env/DB values warn
   and fall through to the default. Defaults 48 / 24.

2. **models/job.go.** Add `FinishedJobStatuses()` returning
   `[]JobStatus{JobStatusSuccess, JobStatusRetried, JobStatusFailed}` — shared by
   both backends so the terminal set is defined once. `pending`/`running` never
   included.

3. **DB layer (§5), both backends + interface parity (/sync-pg-to-sqlite).**
   - `SoftDeleteFinishedJobs(ctx, before time.Time, limit int) (int64, error)` —
     stage 1. Select up to `limit` uids where `status IN (finished) AND deleted_at
     IS NULL AND updated_at < before`, then `UPDATE ... SET deleted_at = now()` on
     that uid set. Returns rows affected.
   - `DeleteSoftDeletedJobs(ctx, before time.Time, limit int) (int64, error)` —
     stage 2. Select up to `limit` uids where `deleted_at IS NOT NULL AND
     deleted_at < before AND uid NOT IN (SELECT previous_job_uid FROM jobs WHERE
     previous_job_uid IS NOT NULL)` (static-SQL subquery — FK guard, §4), then
     physically `DELETE` that uid set. Returns rows affected.
   Both take an explicit `limit` so the caller batches; a large backlog never
   holds a long transaction.

4. **jobdef/types.go.** Add `JobTypeJobsCleanup JobType = "jobs_cleanup"`.

5. **jobtypes/registry.go.** Register `JobsCleanupJobDefinition`.

6. **jobtypes/job_jobs_cleanup.go (§1, §2).** Global job (org `""`,
   `organization_uid` NULL), self-rescheduling with `delay = 24h`, following the
   `state_cleanup` pattern. Each Run: resolve the two windows via
   `resolveRetentionTier`; compute `softBefore = now - softHours`, `hardBefore =
   now - hardHours`; run stage 1 then stage 2, each looping
   `SoftDeleteFinishedJobs`/`DeleteSoftDeletedJobs` in batches of
   `jobsCleanupBatchSize (10_000)` until a short batch (backlog fully drains in
   one run); log per-run counts (`Soft-deleted finished jobs count=N`,
   `Deleted soft-deleted jobs count=M`); reschedule self 24h out (skipped when
   services unavailable, like the other maintenance jobs). Its own terminal rows
   flow through the same two-stage lifecycle (self-cleaning). `pending`/`running`
   never touched regardless of age.

7. **job_startup.go.** Add `ensureJobsCleanupJob` and call it from `Run` next to
   `ensureStateCleanupJob`; relies on `CreateJob` dedupe (type+config+org+pending)
   so it doesn't stack duplicates.

8. **Tests (§6).**
   - DB-layer parity sub-test `testJobsCleanupRetention` added to `testService`
     (runs on Postgres + SQLite): stage 1 soft-deletes old
     success/retried/failed and leaves recent terminal + pending/running of any
     age + already-soft-deleted untouched; stage 2 hard-deletes rows soft-deleted
     > window (incl. user-cancelled = arrived with deleted_at) and keeps freshly
     soft-deleted; full lifecycle (survives at 47h, soft-deleted at 48h, still
     queryable until 72h, gone after); retry chain (referenced predecessor
     survives until successor gone, drains over two runs); batching (backlog >
     one batch drains in one run via the runner loop); idempotency (second run
     no-ops). All assertions target seeded UIDs so the shared harness DB can't
     pollute them.
   - Runner tests in the jobtypes package (SQLite in-memory, like the reaper
     test): a `performance.jobs_soft_delete_hours` global DB parameter changes
     the window without restart; `SP_*` env wins over the DB row; invalid value
     falls back to default with a warning; the run reschedules exactly one
     pending `jobs_cleanup` follow-up; `ensureJobsCleanupJob` doesn't stack
     duplicates; registry resolves `jobs_cleanup`.
