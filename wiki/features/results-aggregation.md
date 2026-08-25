# Results Aggregation

How raw check executions become hour/day/month rollups: the per-organization
background job, the tier boundaries, the transactional compaction, the pure-Go
aggregate math, the retention knobs, and every consumer that reads across
tiers. Read this before touching `job_aggregation.go`, `CompactResults`, the
`uptimebar` package, or any endpoint that queries `results`.

For the `results` table schema itself see
[database-model/results-incidents.md](../database-model/results-incidents.md);
this page covers behavior, not columns.

## The tier model

All results — raw executions and rollups — live in the single `results` table,
one shape, discriminated by `period_type`: `raw` → `hour` → `day` → `month`
(`server/internal/db/models/result.go`). The aggregation job rolls each tier
into the next and **deletes the source rows in the same transaction**, which
yields the system's load-bearing invariant:

> **Tiers cover disjoint age bands.** A time range can be answered by one
> union query over `raw + hour + day` with no risk of double-counting, because
> a measurement exists in exactly one tier at any moment.

Consequences every consumer relies on:

- **The current open bucket never has a rollup row.** Only completed periods
  aggregate, so "this hour so far" is always raw (or `hour` rows for "this
  month so far", etc.). Consumers must request raw as a co-tier to fill the
  open bucket (spec `2026-05-18-01`).
- **`month` is terminal.** Nothing rolls it further and nothing deletes it;
  month rows accumulate for the life of the check.
- **Rollups are per-region** — one row per `(check, region, period)`; `region
  IS NULL` is its own group. Region granularity survives aggregation.
- **Availability is never stored.** It is derived at read time as
  `successful_checks / total_checks × 100`
  (`handlers/results/service.go`, spec `2026-07-24-02`).
- **Rollups carry no `output` blob** (raw failure text is gone once a bucket
  rolls up — only status, counts and duration stats survive) and no
  per-execution `worker_uid` unless the bucket had exactly one worker.

## The aggregation job

Type `aggregation` (`jobs/jobdef/types.go`), implemented in
`jobs/jobtypes/job_aggregation.go`, **one job per organization**.

- **Provisioning**: the startup job seeds one per org
  (`job_startup.go` → `ensureAggregationJobs`); the test-data endpoint also
  kicks one. There is no other creation site, so **an org created after boot
  gets its first aggregation job at the next server start**.
- **Self-rescheduling**: every run schedules its successor via `CreateJob`,
  which dedupes against an existing pending job of the same type/org rather
  than stacking duplicates (`jobs/jobsvc/service.go`,
  `findAndUpdateExistingJob`). Delay is `0` only when a rollup actually
  committed (drain the backlog), otherwise `+1h` — a poisoned bucket can never
  spin at delay=0.
- **Stages in strict priority**: raw→hour, then hour→day, then day→month.
  **One bucket per run** — the run stops after the first stage that did work,
  so progress is incremental and each run is short.
- **Never-halt guard** (spec `2026-07-12-01`): a stage error is captured, not
  returned early; the follow-up job is *always* scheduled first, then the
  error is surfaced as a `RetryableError`. A stuck bucket degrades to "this
  bucket is stuck", never "aggregation for the org is dead".

## Bucket selection and tier boundaries

`findAggregatableResults` resolves retention live, computes the boundary, and
asks for candidate source rows:

- **Retention semantics**: retention `N` keeps the current incomplete period
  plus `N-1` completed ones. `calculateAggregationBoundary` (all UTC,
  calendar-aligned):
  - raw→hour: `truncate(now, hour) − (rawHours−1)h`
  - hour→day: `startOfTodayUTC − (hourDays−1)d`
  - day→month: `startOfMonthUTC − (dayMonths−1)mo`
- **Discovery filter**: source `period_type`, `period_start <` boundary,
  `ExcludeStatuses = {created, running}` (lifecycle markers must not make a
  bucket look aggregatable on their own), `RequireCheckExists: true` (skip
  FK-orphan rows), limit 100. The first row's `(check, region, period_start)`
  picks the bucket — ordering is `period_start DESC`, so the newest eligible
  bucket wins.
- **Target bucket**: `calculatePeriodBoundaries` gives `[start,
  start + span − 1ms]` (the stored, inclusive-display `period_end`). The
  **source fetch window is `period_end + 1ms`** — the exclusive next-bucket
  start. Fetching with the inclusive end excluded a row landing exactly on
  `HH:59:59.999`, which was then re-discovered forever and never compacted
  (spec `2026-07-22-03` §B, observed live on a production deployment).

## Transactional compaction

`aggregatePeriod` delegates to `DBService.CompactResults`
(`db/service.go`; implementations mirrored line-for-line in
`db/postgres/postgres.go` and `db/sqlite/sqlite.go` per the
`/sync-pg-to-sqlite` convention). **Fetch → aggregate → upsert → delete is one
transaction**:

1. Read source rows with `applyResultsFilter` — the *same* helper `ListResults`
   uses, so the transaction sees exactly what a plain list query would.
2. Call the pure-Go `AggregateResultsFunc` (no DB access inside).
3. If the aggregate returns no rollup or no source UIDs (marker-only bucket),
   write nothing and report `Compacted=false`.
4. Upsert the rollup via `upsertAggregatedResultTx`: **delete-then-insert on
   the bucket key** with `COALESCE(region,'')` so NULL regions can't slip past
   the unique index. Chosen over `ON CONFLICT` on an expression index (awkward
   through Bun); `results_aggregated_unique_idx` — rebuilt NULL-proof in
   migration `006_v0_5_0` — remains the backstop. Re-aggregating a bucket is
   idempotent: exactly one row, always.
5. Delete the source rows **by explicit UID list** (never by re-running the
   filter), skipping lifecycle markers.

Any error rolls the whole thing back: the bucket stays fully raw and a later
run retries cleanly — never a rollup+raw hybrid, never data deleted without
its rollup committed. A `compactFailpoint` test hook between upsert and delete
exists purely to prove the rollback in tests.

**Lifecycle markers** (`created`, `running`) are never deleted and never
counted: they exist so result permalinks stay stable (spec `2026-07-08-04`).
`measurableSourceUIDs` filters them out of the delete set; a bucket containing
*only* markers is logged and skipped without writing anything — this progress
guard plus the discovery-time exclusion is what killed the poison-pill loop
(spec `2026-07-11-16`).

## The aggregate math (pure Go)

All computation lives in `job_aggregation.go` and runs inside the transaction
as a pure function — an explicit architecture decision from the original spec
(`2025-12-19-aggregation`): single source of truth, unit-testable, no
`date_trunc`/`strftime` divergence between Postgres and SQLite. Tier detection:
`results[0].DurationMin == nil` → raw sources.

**Availability counting**

- Raw path: markers skipped entirely; `total_checks` counts every non-marker
  row; `successful_checks` counts rows where `Status.CountsAsUp()` — **up and
  warning both count as up**.
- Rollup path: sums children's `TotalChecks`/`SuccessfulChecks`; status counts
  are weighted by the checks they represent.

**Dominant status** (`calculateDominantStatus`), three rules in order:

1. A dominating hard failure wins — most frequent, ties broken by
   `checkerdef.Status.Severity()` (down/timeout/error > degraded >
   up/warning > running).
2. Otherwise, a window containing a raw `warning` (or an already-promoted
   `degraded` child) promotes to **`degraded`** — so a rollup can show
   `degraded` with 100% availability. That is intentional: availability and
   health are different signals.
3. Otherwise the dominant non-failing status.

**Durations**

- `duration_avg` is the careful value: raw mean for raw sources,
  `total_checks`-weighted mean of children's `DurationAvg` for rollups, `nil`
  when nothing contributed. Consumers that care (uptimebar) read `DurationAvg`.
- `duration` on a rollup is a plain unweighted mean of children's `Duration`;
  `duration_p95` for rollups is the mean of children's p95s — documented
  approximations.
- min-of-mins / max-of-maxes for the bounds.

**Custom metrics** aggregate by name suffix (`determineMetricAggregation`):
`_min`, `_max`, `_avg`, `_pct`, `_rte`, `_sum`, `_cnt`, `_val` (string values
merge into a value→count map); un-suffixed names fall back by type
(string→`_val`, int→`_cnt`, float→`_avg`).

**What does not survive**: there is no error sampling. Once a bucket rolls up,
per-failure detail (output text, individual timings) is gone; only the raw
tier has it.

## Retention configuration

Defaults **24 raw hours / 7 hourly days / 2 daily months**
(`job_aggregation.go`; the old aggressive `1/1/1` fallback is gone, and 30→7 /
12→2 were tightened by spec `2026-07-14-02`).

Resolution happens **on every job run** (`retentionFromConfig` →
`resolveRetentionTier`), so edits apply without a restart. Precedence per tier:

1. Env: `SP_PERFORMANCE_AGGREGATION_RETENTION_RAW_HOURS` / `_HOUR_DAYS` /
   `_DAY_MONTHS`
2. Global DB parameter (`organization_uid IS NULL`):
   `performance.aggregation_retention_raw_hours` / `_hour_days` / `_day_months`
   (`systemconfig/systemconfig.go`)
3. Legacy koanf `aggregation.retention_*` (deprecated, warns on use)
4. The hardcoded defaults

Values must be whole numbers ≥ 1 — enforced at write time
(`ValidateAggregationRetentionParameter`, surfaced as 422) and floored in the
super-admin UI (server → **Aggregation** tab,
`web/dash0/src/routes/orgs/$org/server.aggregation.tsx`).

Two operator rules from spec `2026-07-14-02`:

- **Changing retention never triggers re-aggregation.** Lowering makes more
  buckets eligible for normal forward rollup on the next runs.
- **Raising retention does not restore data** already rolled up — the raw rows
  were deleted. Raise *before* you need the history.

There is no retention for the `month` tier; it grows forever.

## Consumers

**`internal/uptimebar`** — the shared engine for per-bucket availability
(status pages, badges). `BucketAvailability` issues **one query** over
`raw + hour + day`, truncates each row to its display bucket, and dispatches
raw rows vs rollups to two accumulators (`accumulateRaw` is the single place
the warning-counts-as-up rule applies to the raw tier; rollups already encode
it in `successful_checks`). The rollup-lag race — a completed bucket whose raw
rows haven't rolled up yet — is handled *by construction*: those raw rows
match the same query and fill the bucket immediately. A safety row cap derived
from the actual retention config (padded, deliberately wide) guards against a
stalled job or misconfigured retention piling up raw rows: it logs a warning
and returns partial data instead of erroring (`bucketing.go`).
`WindowAvailability` (`window.go`) is the same union folded to one number per
check.

**Status pages** (`handlers/statuspages/service.go`) — hourly view builds 24
buckets (newest = the in-progress hour, filled from raw); daily view and group
components merge member checks per bucket.

**Badges** (`handlers/badges/service.go`) — same engine for the availability
bars; separate raw-only queries for latest-status and response-time parts.

**Availability endpoint** (`handlers/availability/service.go`) — window math
over the union. `WindowAvailability` is the one uptimebar entry point that
**includes the `month` tier**: a whole-window fold only sums counts, so the
terminal month rows are safe to add and make any lookback answerable
regardless of retention tuning. The endpoint's lookback cap is therefore a
pure input-sanity bound (`maxLookbackYears = 10`), not a data horizon. One
edge: a rollup row counts iff its `period_start` falls inside the window, so a
duration window (`365d`) may miss up to a month at its oldest edge; calendar
windows (`mtd`/`ytd`) start on month boundaries and are exact.

`BucketAvailability` (status pages, badges) deliberately stays raw+hour+day: a
month rollup spans many ticks and cannot be honestly attributed to one, so
ticks older than the day-tier horizon (~2 months by default) render "no data"
rather than fabricated per-day values.

**Results API** (`handlers/results/`) — `periodType` is a comma-separated
filter. `GetResult` survives rollups: a UID that was compacted away is
resolved by parsing the **UUIDv7 embedded timestamp** and walking
hour → day → month for the covering rollup (`findCoveringAggregation`),
returned with `fallbackInfo.reason = "rolled_up_to_<tier>"` so permalinks
degrade gracefully instead of 404ing.

**MCP** (`internal/mcp/tools_results.go`) — defaults to `hour`, picks a
default time window matched to the finest requested tier, and echoes the
effective filter back so silent defaulting is visible to the model.

**Dashboard response-time chart**
(`web/dash0/src/components/checks/response-time-chart.tsx`) — requests raw as
a **co-tier** (`day → "raw,hour"`, `month → "raw,hour,day"`, …): raw covers
the current open bucket, disjointness guarantees no double-count, and its gap
detector deliberately ignores tier transitions.

## Failure-mode history

Three production/dev incidents shaped the current design; the specs are the
forensic record and worth reading in full:

| Incident | Failure | Fix | Spec |
|---|---|---|---|
| Poison-pill loop | A marker-only bucket compacted to nothing, stayed discoverable, and the delay=0 reschedule re-ran it forever — 8.55M duplicate hour rows (one bucket × 1.66M), 4.1 GB dev DB; NULL regions defeated the unique index | Marker exclusion at discovery, progress guard, idempotent NULL-proof upsert, `performance.*` retention | `specs/done/2026/07/2026-07-11-16-*` |
| FK-orphan halt | Rows for a hard-deleted check (possible on SQLite: `PRAGMA foreign_keys` is per-connection) made the rollup INSERT fail every run — org aggregation dead | `RequireCheckExists` at discovery + always-schedule-the-follow-up | `2026-07-12-01-*` |
| Boundary millisecond | Row at `HH:59:59.999` excluded from its own bucket's fetch → re-discovered forever; plus the old fetch→upsert→delete ran as three separate calls with two inconsistent-state windows | Fetch to `period_end + 1ms`; single-transaction `CompactResults` | `2026-07-22-03-*` |

Design-rationale specs: `2025-12-19-aggregation` (pure-Go architecture,
per-org job, one-bucket-per-run, delete-by-UID), `2026-07-14-02` (retention
config & UI, defaults tightening), `2026-07-24-02` (dropping `output` /
`availability_pct` / `last_for_status` from rollups), `2026-07-08-04`
(marker preservation for permalinks).

## Known gaps

- **Per-bucket strips go "no data" past the day tier**: `BucketAvailability`
  excludes the month tier by design (a month rollup can't be attributed to a
  single tick), so a 90d status-page/badge strip on default retention renders
  its oldest ~30 days as "no data". Honest, but a product-visible consequence
  of the 2-month default.
- **Orgs created after boot** have no aggregation job until the next server
  restart re-runs `ensureAggregationJobs`.
- **`duration_min` can seed to 0**: aggregation state seeds min/max from
  `results[0]` (newest-first ordering); if that row has a nil duration the min
  comparison never fires. `duration_avg` is nil-safe and unaffected.
- **No user-facing docs**: `web/docs/` says nothing about retention or
  rollups — a self-hoster can't learn that raising retention won't restore
  compacted history.

## Tests

- `jobs/jobtypes/job_aggregation_test.go` — boundary/period math, raw and
  weighted rollup aggregation, metric suffixes, marker retention.
- `job_aggregation_boundary_test.go` — the final-millisecond row compacts.
- `job_aggregation_poison_pill_test.go` — marker-only bucket inert, idempotent
  upsert, +1h vs delay=0 rescheduling, retention resolution.
- `job_aggregation_fk_orphan_test.go` — orphan bucket inert, orphan doesn't
  halt the org, stage error still schedules the follow-up (incl. Postgres
  variant).
- `job_aggregation_warning_test.go` — warning→degraded promotion, severity
  tie-breaks.
- `db/sqlite/compaction_test.go` + `db/postgres/compaction_postgres_test.go` —
  transactional compaction happy path, rollback via `compactFailpoint`,
  no-source-UIDs no-op.
- `db/sqlite/aggregation_dedupe_migration_test.go` — runs the real 006 dedupe
  SQL against seeded duplicates.
- `uptimebar/bucketing_test.go` — raw+rollup no double-count, safety cap
  engages and warns, multi-check fairness.
