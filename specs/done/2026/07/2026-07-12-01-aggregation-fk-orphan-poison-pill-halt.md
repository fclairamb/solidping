# Aggregation FK-orphan poison pill — orphaned results rows fail the rollup insert and permanently halt aggregation for the org

## Problem

Local dev (SQLite, org `default`): aggregation job
`0fd5f8ce-7165-4e41-b7e2-b6e99ba503ca` failed with

```
failed to create aggregated result: constraint failed: FOREIGN KEY constraint failed (787)
```

after a full retry chain (`33621387` → `0060efd3` → `0fd5f8ce`, 2 retries).
Since the final failure there are **zero pending aggregation jobs** for the
org — aggregation is dead, and raw rows accumulate unaggregated (~13k in the
first two hours).

The database contains **3,056 orphaned `results` rows across 20 distinct
`check_uid`s that no longer exist in `checks`** (raw + hour rows,
`period_start` 2026-06-28 → 2026-07-02). Every attempt deterministically
picks the same orphan (`check_uid=94076336-5d8d-4996-8158-87122d6bdd32`,
`period_start=2026-07-02T13:25:24Z` — the newest orphan raw row).

### Root cause — the chain

1. `findAggregatableResults`
   ([job_aggregation.go:210-246](../../server/internal/jobs/jobtypes/job_aggregation.go))
   selects source rows older than the retention boundary with no regard to
   whether their check still exists. `ListResults` orders `period_start DESC`
   ([sqlite.go:1643](../../server/internal/db/sqlite/sqlite.go)), so once the
   fresh backlog is worked through, the newest *orphan* row is always the
   next candidate — the poison pill is deterministic.
2. `buildAggregatedResult`
   ([job_aggregation.go:972](../../server/internal/jobs/jobtypes/job_aggregation.go))
   copies `results[0].CheckUID` into the rollup row.
3. `CreateResult` violates `results.check_uid → checks(uid)` — SQLite error
   787 (`SQLITE_CONSTRAINT_FOREIGNKEY`); on Postgres this would be `23503`.
4. **The halt**: `Run` returns the retryable error at
   [job_aggregation.go:89](../../server/internal/jobs/jobtypes/job_aggregation.go)
   *before* reaching the "schedule next run" `CreateJob` at
   [job_aggregation.go:109](../../server/internal/jobs/jobtypes/job_aggregation.go).
   When the retry chain exhausts, **no future aggregation job exists for the
   org**. Nothing re-seeds it until server restart
   ([job_startup.go:333](../../server/internal/jobs/jobtypes/job_startup.go)),
   after which the job churns through the new backlog at `delay=0`, hits the
   same orphan, and dies again — a restart-crash cycle, observable in `jobs`
   as a burst of `success` rows ~200 ms apart followed by the fatal retry
   chain.

### How the orphans got created

The application never hard-deletes checks — `DeleteCheck` is a soft delete
([sqlite.go:1367](../../server/internal/db/sqlite/sqlite.go),
[postgres.go:1465](../../server/internal/db/postgres/postgres.go)) — and no
SQLite migration rebuilds the `checks` table. So the 20 checks were
hard-deleted by a connection running **without foreign-key enforcement**,
which skipped the `ON DELETE CASCADE` on `results.check_uid`:

- SQLite FKs are **per-connection**. The code enables them with a one-shot
  `PRAGMA foreign_keys = ON` on the pool
  ([sqlite.go:130](../../server/internal/db/sqlite/sqlite.go)) instead of in
  the DSN. `SetMaxOpenConns(1)` makes recycling rare, but any connection
  re-opened by `database/sql` after an error runs with FKs **off** — no
  cascades, no validation — silently, until restart.
- External sessions (`sqlite3` CLI defaults FKs to off) have the same effect.

Postgres cannot produce orphans this way (FKs are schema-level), but chain
step 4 — permanent halt after a failed stage — is backend-independent: any
persistent `aggregatePeriod` error kills aggregation for the org on both
backends.

## Proposal

Related: [2026-07-11-16-aggregation-poison-pill-loop.md](2026-07-11-16-aggregation-poison-pill-loop.md)
is the *marker-only loop* sibling. None of its fixes (§1 marker exclusion,
§2 progress guard, §3 idempotent upsert, §4 retention params) covers this
variant — the fetched rows are real and the upsert would still hit the FK.
Its §2 and this spec's §1 both touch `Run`'s control flow; implement
together.

### 1. Never halt: always schedule the follow-up job

Restructure `Run`
([job_aggregation.go:65-117](../../server/internal/jobs/jobtypes/job_aggregation.go))
so the "schedule next aggregation job" `CreateJob` executes even when an
aggregation stage returns an error (schedule with the +1h delay on the error
path, then return the stage error so the job itself still retries/fails
visibly). A poisoned bucket must degrade to "this bucket is stuck", never
"aggregation for the org is dead". `CreateJob`'s duplicate handling already
makes this safe against double-scheduling with the retry chain.

### 2. Exclude orphaned buckets from discovery

`findAggregatableResults` must only select rows whose check still exists.
Add a filter flag to `ListResultsFilter` (e.g. `RequireCheckExists bool` →
`check_uid IN (SELECT uid FROM checks)`) applied in both backends
(`sqlite.go` / `postgres.go`, keep parity per `/sync-pg-to-sqlite`).
Soft-deleted checks still satisfy the FK and keep their history —
**only rows whose check row is truly absent are excluded**. On Postgres the
subquery is a no-op by construction (FK guarantees existence); implement it
anyway for parity and defense in depth.

### 3. Purge existing orphans (migration) — self-heal

Next consolidated migration (`006_vX_Y_Z.up.sql`, SQLite **and** Postgres for
symmetry even though Postgres can't hold orphans):

```sql
delete from results where check_uid not in (select uid from checks);
```

Idempotent, no-op on clean databases. This removes the 3,056 dev orphans.
With §2 in place any future externally-created orphans are merely inert dead
rows instead of poison pills; no periodic sweep needed.

### 4. Make SQLite FK enforcement connection-safe

Move `foreign_keys` into the connection string in `New`
([sqlite.go:112-132](../../server/internal/db/sqlite/sqlite.go)) so every
connection — including ones recycled after errors — enforces FKs. The DSN
parameter depends on which driver `sqliteshim` resolves (mattn with CGO:
`_foreign_keys=on`; modernc otherwise: `_pragma=foreign_keys(1)`). Verify at
startup by reading `PRAGMA foreign_keys` back on a fresh connection and
fail fast if it is not `1`; keep the existing one-shot `ExecContext` as
belt-and-braces for the `:memory:` path.

### 5. Tests

- **Orphan bucket is skipped**: seed a raw row whose check does not exist
  (SQLite: insert via a connection with `PRAGMA foreign_keys=OFF`);
  discovery skips it, `Run` completes, and a follow-up job is scheduled.
- **Stage error still schedules the follow-up**: force `aggregatePeriod` to
  error (both backends); assert the next aggregation job exists with the
  +1h delay and the job error is still surfaced.
- **Migration purge**: seed orphans, migrate, assert they are gone and
  non-orphan rows survive.
- **FK enforcement**: open the SQLite service, read
  `PRAGMA foreign_keys` on a fresh pool connection, assert `1`.
- Table-driven, `testify/require`, `t.Parallel()` per repo convention.

## Cleanup runbook — local dev (server/solidping.db)

Handled automatically by the §3 migration on deploy. Manual emergency
unblock before the fix ships:

1. `DELETE FROM results WHERE check_uid NOT IN (SELECT uid FROM checks);`
   (3,056 rows; run via `sqlite3` — FKs off there is fine, we're deleting
   the orphans themselves.)
2. Restart the server so `job_startup` re-seeds the per-org aggregation job
   ([job_startup.go:333](../../server/internal/jobs/jobtypes/job_startup.go)).
3. Confirm recovery: `SELECT count(*) FROM jobs WHERE type='aggregation' AND
   status IN ('pending','running');` returns ≥1, and raw rows older than the
   retention boundary start draining.

## Out of scope / follow-ups

- Terminal `jobs` rows retention —
  [2026-07-11-17-finished-jobs-cleanup-retention.md](2026-07-11-17-finished-jobs-cleanup-retention.md).
- Marker-only loop fixes —
  [2026-07-11-16-aggregation-poison-pill-loop.md](2026-07-11-16-aggregation-poison-pill-loop.md).
- Orphan sweeps for other tables that reference `checks` without strict
  enforcement guarantees (none known today).

## Implementation Plan

Reconciled with the already-landed sibling spec **2026-07-11-16** (marker-only
loop), which shipped on this branch: `ExcludeStatuses` in `ListResultsFilter`,
the idempotent `UpsertAggregatedResult` write path, the progress guard in
`aggregatePeriod`, per-tier retention defaults, and migration `006_v0_5_0`
(dedupe + NULL-proof unique index). None of those touch the FK-orphan halt, so
every item below is new work; §1 restructures the same `Run` the -16 spec's §2
guard fed into, and I integrate on top of it.

### §1 — Never halt: always schedule the follow-up job (NOT covered by -16)
Restructure `AggregationJobRun.Run` (`job_aggregation.go`) so the "schedule next
aggregation job" `CreateJob` runs even when an `aggregatePeriod` stage returns an
error. On a stage error: capture it, stop trying further stages, schedule the
follow-up with the **+1h** delay (never delay=0), then return
`jobdef.NewRetryableError(stageErr)` so the job still retries/fails visibly.
`CreateJob`'s duplicate handling (dedupe on type+config+pending+org) keeps this
safe against the retry chain. Success/no-work paths keep their existing delay
(0 on work done, 1h on no work).

### §2 — Exclude orphaned buckets from discovery (NOT covered by -16)
Add `RequireCheckExists bool` to `models.ListResultsFilter`. When set, both
backends' `ListResults` add `check_uid IN (SELECT uid FROM checks)` (no
`deleted_at` filter — soft-deleted checks keep their history and satisfy the FK;
only truly-absent check rows are excluded). Set `RequireCheckExists: true` in
`findAggregatableResults`'s discovery filter. Postgres: a no-op by FK
construction, implemented for parity + defense in depth. Keep sqlite/postgres in
lockstep (`/sync-pg-to-sqlite`).

### §3 — Purge existing orphans (migration) — self-heal (NOT covered by -16)
Add migration `007_v0_5_0.up.sql` (+ `.down.sql`) for **both** backends (006 is
taken by -16; use the next number 007). Up:
`delete from results where check_uid not in (select uid from checks);` —
idempotent, no-op on clean DBs, no-op on Postgres by FK construction. Down is a
teardown-only no-op (deleted rows are unrecoverable).

### §4 — Make SQLite FK enforcement connection-safe (NOT covered by -16)
In `sqlite.New`, add `foreign_keys` to the connection DSN so every connection —
including ones `database/sql` re-opens after an error — enforces FKs. Pick the
param by driver via `sqliteshim.DriverName()`: modernc (`sqlite`) →
`_pragma=foreign_keys(1)` (applied per-connection in modernc's
`applyQueryParams`); mattn (`sqlite3`) → `_foreign_keys=on`. Only for file-based
DBs (the `:memory:` DSN has no query string); keep the existing one-shot
`PRAGMA foreign_keys = ON` `ExecContext` as belt-and-braces (covers `:memory:`).
Verify at startup by reading `PRAGMA foreign_keys` back and failing fast if not
`1`.

### §5 — Tests
- **Orphan bucket skipped** (sqlite): insert a raw row for a non-existent check
  via a `foreign_keys=OFF` connection; assert `findAggregatableResults` /
  `aggregatePeriod` skip it and `Run` schedules a follow-up.
- **Stage error still schedules the follow-up**: make `aggregatePeriod` error
  (a `CreateJob`-recording jobsvc + a DB stub / forced FK insert) and assert the
  next aggregation job exists at ~+1h and `Run` still surfaces the error.
- **Migration purge**: seed orphans (FK off), run `007` up, assert orphans gone
  and non-orphan rows survive.
- **FK enforcement**: open the sqlite service, read `PRAGMA foreign_keys` on a
  genuinely fresh pool connection (force via `SetMaxIdleConns(0)`), assert `1`.
- `RequireCheckExists` filter unit coverage in both backends where feasible
  (sqlite in-memory; postgres behind the existing testcontainer gate).
- Table-driven, `testify/require`, `t.Parallel()` per repo convention.
