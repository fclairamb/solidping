# Aggregation job poison-pill loop — marker-only buckets re-aggregate forever, duplicating hour rows and jobs unbounded

## Problem

`solidping_dev` (k8xp, CNPG `main-cluster-1`) grew to **4.1 GB** in a few
days — 20× every other database on the server combined — while holding only
153 checks. Two tables are the entire database:

| Table | Size | Rows | Created in the last hour |
|---|---|---|---|
| `results` (`period_type='hour'`) | 2.0 GB | 8.55M | **484,725** |
| `jobs` (`type='aggregation'`, all `success`) | 2.2 GB | 8.51M | **481,561** |

The `hour` rollups span only **9,891 distinct `(check, period_start)`
buckets** → an **860× duplication factor**. The worst single bucket
(`check 79f7b8d5-…`, hour `2026-07-10 14:00`) has **1,662,028 identical
copies**, and its *only* remaining raw row is one lifecycle-marker
(`status=1` "created") at `2026-07-10 14:19:32`. Aggregation jobs were being
created at **6,000–8,000/minute and accelerating**, in four organizations
independently. Dev had exactly **54** stranded old lifecycle-marker raw rows
(50 × `created`, 4 × `running`) — each one is a poison pill.

### Root cause — the loop

Each aggregation iteration for a bucket whose only raw rows are
lifecycle markers (`created`/`running`):

1. `findAggregatableResults`
   ([job_aggregation.go:210-246](../../server/internal/jobs/jobtypes/job_aggregation.go))
   selects the marker — its hour is past the retention boundary, so it is
   "aggregatable". Nothing excludes marker-only buckets.
2. `aggregateResults` → `processRawResult`
   ([job_aggregation.go:734](../../server/internal/jobs/jobtypes/job_aggregation.go))
   **skips** lifecycle markers, producing a degenerate rollup
   (`total_checks=0`, status `0`).
3. `CreateResult`
   ([job_aggregation.go:179](../../server/internal/jobs/jobtypes/job_aggregation.go))
   is a **plain INSERT** — a fresh duplicate `hour` row every iteration.
4. The delete step **skips lifecycle markers by design**
   ([job_aggregation.go:189-195](../../server/internal/jobs/jobtypes/job_aggregation.go),
   permalink preservation) → `deletedCount=0`, the marker survives.
5. `aggregatePeriod` still returns `true` (`len(results) > 0`), so `Run`
   sets `workDone=true`, `break`s at stage 1 (starving `hour→day`
   forever), and **reschedules itself with `delay=0`**
   ([job_aggregation.go:94-116](../../server/internal/jobs/jobtypes/job_aggregation.go)).

→ +1 duplicate `hour` row and +1 `jobs` row per iteration, as fast as the
job worker turns, forever. ~250 MB/hour in dev.

### Why the unique index didn't save us

[`results_aggregated_unique_idx`](../../server/internal/db/postgres/migrations/001_v0_1_0.up.sql#L457)
on `(organization_uid, check_uid, region, period_type, period_start) WHERE
period_type != 'raw'` exists and is present in dev — but **8,552,616 of the
8.55M duplicate rows have `region IS NULL`**, and Postgres (and SQLite)
unique indexes treat NULLs as distinct, so the constraint never fires. The
degenerate rollup inherits `region` from the marker row
(`results[0].Region` in `buildAggregatedResult`), which is NULL.

### Aggravating factors

- `retentionFromConfig`
  ([job_aggregation.go:251-273](../../server/internal/jobs/jobtypes/job_aggregation.go))
  falls back to `1/1/1` when unset, so a marker becomes "aggregatable"
  within the hour it was written — the loop starts almost immediately.
- `jobs` has **no terminal-row retention at all** (only soft deletes), so
  the 8.5M `success` rows accumulated with nothing to stop them
  (follow-up, see Out of scope).

## Proposal

All changes in
[server/internal/jobs/jobtypes/job_aggregation.go](../../server/internal/jobs/jobtypes/job_aggregation.go)
plus the DB layer (`postgres.go` / `sqlite.go` — keep parity per
`/sync-pg-to-sqlite`).

### 1. Exclude lifecycle markers from work discovery

`findAggregatableResults` must never select a bucket on the strength of
lifecycle-marker rows. Add a status-exclusion to `ListResultsFilter` (e.g.
`ExcludeStatuses []int` or a dedicated `ExcludeLifecycleMarkers bool`) and
apply it in the discovery query only — the aggregation fetch (step 3) keeps
reading all rows so markers still contribute their skip/keep behavior.
Marker-only buckets then simply stop being found, and the intentionally
preserved markers become inert.

### 2. Progress guard — "work done" requires actual progress

`aggregatePeriod` currently reports success whenever it *fetched* rows.
Change the contract to: return `aggregated=true` **only when at least one
non-marker source row was aggregated** (equivalently: skip the insert and
return `false` when every fetched row is a lifecycle marker /
`total_checks==0`). This makes the `delay=0` immediate reschedule
([job_aggregation.go:102-108](../../server/internal/jobs/jobtypes/job_aggregation.go))
conditional on real progress even if discovery regresses — defense in
depth against any future poison-pill variant. Log a warning if an
iteration fetches rows but deletes none.

### 3. Make aggregated-result writes idempotent (fix the NULL hole)

- **Migration** (next consolidated `NNN_vX_Y_Z.up.sql`, Postgres + SQLite):
  replace `results_aggregated_unique_idx` with a NULL-proof expression
  index:
  ```sql
  create unique index results_aggregated_unique_idx
    on results (organization_uid, check_uid, coalesce(region, ''), period_type, period_start)
    where period_type != 'raw';
  ```
  The migration **must dedupe existing rows first** or index creation fails
  on polluted DBs: per `(organization_uid, check_uid, coalesce(region,''),
  period_type, period_start)` keep the best row — highest `total_checks`,
  tie-break earliest `created_at` — and delete the rest. Idempotent, no-op
  on clean databases. This is also how the 8.5M dev duplicates get removed
  (see Cleanup below).
- **Code**: the aggregation write becomes an UPSERT (`ON CONFLICT … DO
  UPDATE` on the expression key — supported by both Postgres and SQLite),
  either as an option on `CreateResult`
  ([postgres.go:1695](../../server/internal/db/postgres/postgres.go),
  [sqlite.go:1596](../../server/internal/db/sqlite/sqlite.go)) or a
  dedicated `UpsertAggregatedResult`. If expression conflict targets prove
  awkward through Bun, an acceptable fallback is delete-then-insert for the
  bucket key inside one transaction (the unique index stays as backstop).
  Mind the `last_for_status` side-effects in `CreateResult` when choosing.

### 4. Resolve retention from global `performance.*` parameters

`retentionFromConfig`
([job_aggregation.go:251-273](../../server/internal/jobs/jobtypes/job_aggregation.go))
currently reads koanf-only `AppConfig.Aggregation.Retention*` values and
silently falls back to `1/1/1` — restart-bound and invisible to operators.
Switch it to the `performance.*` global parameters defined in
[2026-07-11-17-finished-jobs-cleanup-retention.md §3](2026-07-11-17-finished-jobs-cleanup-retention.md)
(`performance.aggregation_retention_raw_hours` / `_hour_days` /
`_day_months`, defaults 24/30/12), resolved via `systemconfig` at job-run
time (env `SP_*` → global DB parameter → default). The `1/1/1` fallback
disappears — absent configuration means the real defaults, not the
most aggressive possible schedule.

### 5. Tests

- Marker-only bucket: discovery skips it; a forced `aggregatePeriod` on it
  inserts nothing, returns `false`; `Run` schedules the next job at +1h,
  not `delay=0` — i.e. the loop terminates.
- Mixed bucket (real rows + marker): one rollup row, real rows deleted,
  marker kept, `aggregated=true`.
- Re-aggregating the same bucket twice yields **one** row (NULL region and
  set region both covered).
- Migration dedupe: seed duplicate hour rows (NULL region), migrate, assert
  one survivor with the highest `total_checks`.
- Both backends (table-driven, per repo convention).

## Cleanup runbook — ⚠️ NOT executed yet, run only after the fix is deployed

Deploying the code fix first is mandatory — cleaning before fixing just
lets the loop re-pollute. Then, on `solidping_dev`
(`kubectl --context k8xp -n postgres exec main-cluster-1 -- psql -U postgres -d solidping_dev`):

1. **Dedupe `hour` rows** — happens automatically via the §3 migration on
   deploy. Verify: `SELECT count(*) FROM results WHERE period_type='hour';`
   should drop from ~8.5M to ~10k.
2. **Purge the garbage jobs** (no code path deletes terminal jobs today):
   ```sql
   DELETE FROM jobs WHERE type='aggregation' AND status='success';
   ```
   (~8.5M rows; batch by `created_at` if lock time matters.)
3. **Reclaim disk** — row deletes don't shrink files:
   ```sql
   VACUUM FULL results; VACUUM FULL jobs;
   ```
   `VACUUM FULL` takes an exclusive lock — fine for dev; use `pg_repack`
   if this ever has to run on a production instance.
4. **Leave the 54 stranded lifecycle markers alone** — they are kept by
   design (permalink preservation) and are inert once §1 lands. Deleting
   them (`DELETE FROM results WHERE period_type='raw' AND status IN (1,2)
   AND period_start < date_trunc('hour', now())`) is only the emergency
   stop if the loop must be halted *before* the fix ships.
5. Confirm recovery: aggregation job creation rate back to ~1 per org per
   hour when idle (`SELECT count(*) FROM jobs WHERE type='aggregation' AND
   created_at > now()-interval '1 hour';`), and DB size flat on the
   Grafana "Database size by database" panel.

## Implementation Plan

All five Proposal items are implemented; nothing descoped.

### §1 — Exclude lifecycle markers from work discovery
- Add `ExcludeStatuses []int` to `models.ListResultsFilter`
  (`internal/db/models/result.go`).
- Apply it in both backends' `ListResults`
  (`postgres.go` / `sqlite.go`) as `(status IS NULL OR status NOT IN (?))`
  — NULL-status rows are never markers, so they stay discoverable.
- `findAggregatableResults` sets
  `ExcludeStatuses: []int{ResultStatusCreated, ResultStatusRunning}` on the
  discovery filter only. The step-3 aggregation fetch keeps reading all rows
  (no `ExcludeStatuses`), so markers still contribute their skip/keep behavior.

### §2 — Progress guard
- In `aggregatePeriod`, count non-marker rows among the fetched bucket. If
  **zero**, log a warning, skip the insert, and return `aggregated=false`
  (no `delay=0` reschedule → loop terminates). Only build+write the rollup and
  delete when at least one non-marker row exists. Warn when rows were fetched
  but `deletedCount==0`.

### §3 — Idempotent aggregated writes (NULL hole)
- **Migration** `006_v0_5_0.up.sql` (Postgres + SQLite, parity):
  dedupe existing non-raw rows per
  `(organization_uid, check_uid, coalesce(region,''), period_type, period_start)`
  keeping highest `total_checks`, tie-break earliest `created_at` (then `uid`),
  via `ROW_NUMBER()`; then `DROP INDEX results_aggregated_unique_idx` and
  recreate it NULL-proof on `coalesce(region,'')`. `.down.sql` restores the
  region-based index. Idempotent / no-op on clean DBs.
- **Code**: new `UpsertAggregatedResult(ctx, *models.Result)` on `db.Service`
  and both backends — delete-then-insert for the bucket key inside one tx (the
  spec-blessed fallback; robust across PG+SQLite, unique index stays as
  backstop). `aggregatePeriod` calls it instead of `CreateResult`.
- Add the stub to the `mockDBService` in `notifications/slack_test.go`.

### §4 — Retention from global `performance.*` parameters
- Add `KeyPerfAggRetentionRawHours` /
  `KeyPerfAggRetentionHourDays` / `KeyPerfAggRetentionDayMonths`
  (`performance.aggregation_retention_raw_hours` / `_hour_days` /
  `_day_months`) to `systemconfig`.
- Rework `retentionFromConfig` → `retentionFromConfig(ctx, jctx)` resolving each
  tier via env `SP_PERFORMANCE_*` → global DB parameter → legacy koanf
  `AppConfig.Aggregation.Retention*` (back-compat, deprecation warn) → hardcoded
  default **24 / 30 / 12**. The `1/1/1` fallback is gone; invalid DB values warn
  and fall through.

### §5 — Tests (sqlite in-memory, PG parity self-skipping under `-short`)
- Marker-only bucket: discovery skips it; forced `aggregatePeriod` inserts
  nothing, returns false; `Run` reschedules at +1h.
- Mixed bucket: one rollup, real rows deleted, markers kept, `aggregated=true`
  (existing test, updated for the 24h default).
- Re-aggregate same bucket twice → one row (NULL region and set region).
- Migration dedupe: seed duplicate hour rows (NULL region) under the old index,
  run the real `006` up SQL, assert one survivor with the highest
  `total_checks` and that the NULL-proof index builds.

The Cleanup runbook is **not executed** (data-safety) — the §3 migration is the
scripted dedupe tooling it references.

## Out of scope / follow-ups

- **`jobs` terminal-row retention**: nothing ever hard-deletes
  `success`/`failed`/`retried` job rows, so the table grows monotonically
  even in healthy operation (snooze_sweep + stuck_job_reaper alone add
  ~2,900 rows/day/org). Deserves its own spec (age-based purge job).
- `events` and `incident_notifications` are also append-only with no
  retention — same follow-up family, much slower growth.
