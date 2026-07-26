---
model: opus
effort: high
---

# Results table tier-1 storage trim: drop dead columns and duplicated payloads

## Problem

The `results` table is the largest and hottest table in the system, and a
significant share of its bytes and write amplification is pure waste. The
analysis in `specs/ideas/2026-07-22-results-table-storage-optimization.md`
(Tier 1) identified free wins that need no restructuring:

1. **`last_for_status` is write-only.** Every result insert runs a companion
   UPDATE clearing the predecessor's flag
   ([postgres.go:1859-1871](server/internal/db/postgres/postgres.go:1859),
   [sqlite.go:1815-1827](server/internal/db/sqlite/sqlite.go:1815)), which
   costs one dead tuple + index churn + WAL per result — roughly **half the
   write-side bloat** of the raw tier. Nothing reads the column: no SELECT
   filters on it (the dashboard's "latest per check" uses `DISTINCT ON
   (check_uid)` instead), the only `WHERE last_for_status` is the maintenance
   UPDATE itself. It also drags along a useless partial index
   `idx_results_last_for_status`
   ([pg 001:458](server/internal/db/postgres/migrations/001_v0_1_0.up.sql:458),
   [sqlite 001:361](server/internal/db/sqlite/migrations/001_v0_1_0.up.sql:361)).

2. **Hourly rollups copy the last raw `output` blob**
   ([job_aggregation.go:976](server/internal/jobs/jobtypes/job_aggregation.go:976),
   written into the row at
   [job_aggregation.go:1195](server/internal/jobs/jobtypes/job_aggregation.go:1195)).
   Hour rows live 7× longer than raw rows, so the blob costs 7× more there
   than in the raw tier. Day/month rollups already empty it.

3. **The HTTP checker duplicates `duration` into `metrics`.** Every result
   path in [checkhttp/checker.go](server/internal/checkers/checkhttp/checker.go:385)
   (~13 sites, lines 385–549) builds
   `Metrics: {duration_ms: X}` — byte-for-byte the same information as the
   `duration` column, ~35 B/row of waste on the most common check type.

4. **`availability_pct` is derivable.** It is
   `successful_checks / total_checks` stored redundantly on every rollup row
   ([models/result.go:125](server/internal/db/models/result.go:125)) — 8 B/row
   and one more way for the two to disagree. The availability and badges
   handlers already compute it at read time from counts
   ([availability/service.go:204](server/internal/handlers/availability/service.go:204),
   [badges/service.go:311](server/internal/handlers/badges/service.go:311)).

5. **`ListResults` always selects both JSON blobs**
   ([postgres.go:1876](server/internal/db/postgres/postgres.go:1876)) even for
   consumers that only need status/counts (uptime bars, status pages, badges —
   see `server/internal/handlers/uptimebar/`), inflating I/O per request.

Combined, Tier 1 halves write amplification and trims ~40 B/row without any
schema restructuring or new dependency.

## Proposal

### 1. Delete `last_for_status` end to end

- Remove the clearing UPDATE from `SaveResultWithStatusTracking` in both
  backends so the save path is a **single INSERT**
  ([postgres.go:1859-1871](server/internal/db/postgres/postgres.go:1859),
  [sqlite.go:1815-1827](server/internal/db/sqlite/sqlite.go:1815)); simplify
  the method name/doc in [service.go:259](server/internal/db/service.go:259)
  accordingly.
- Remove `LastForStatus` from the model
  ([models/result.go:120](server/internal/db/models/result.go:120)) and from
  the rollup row builder
  ([job_aggregation.go:1197](server/internal/jobs/jobtypes/job_aggregation.go:1197)).
- New migration `009` (both backends): `DROP INDEX
  idx_results_last_for_status; ALTER TABLE results DROP COLUMN
  last_for_status;` with a down migration restoring column + partial index.
- Update `worker_test.go` and any other tests referencing the field.
- **Guard:** before deleting, re-verify with a repo-wide grep that no reader
  appeared since the analysis (2026-07-22).

### 2. Stop copying raw `output` into hourly rollups

- In the aggregation job, stop tracking/storing `lastOutput` for the hour
  stage (drop the tracking at
  [job_aggregation.go:976](server/internal/jobs/jobtypes/job_aggregation.go:976)
  and the `Output: state.lastOutput` assignment at
  [job_aggregation.go:1195](server/internal/jobs/jobtypes/job_aggregation.go:1195));
  hour rows get NULL output, matching day/month behavior.
- **Verify first** that no reader (dashboard, status pages, API consumers)
  renders `output` from hour-period rows — recent-detail views read raw rows.
  If something does want "last error of the hour", that's out of scope here;
  note it, don't build it.

### 3. Stop writing `metrics={duration_ms}` for HTTP

- Drop the single-key `Metrics` maps in
  [checkhttp/checker.go](server/internal/checkers/checkhttp/checker.go:385)
  (all ~13 sites) — leave `Metrics` nil. `duration` remains in its dedicated
  column. Other checkers (ssl, domain) carry `duration_ms` alongside real
  metrics keys; leave them untouched.
- **Verify** dash0 does not read `metrics.duration_ms` for HTTP response-time
  charts (they should use the `duration` field). If any chart goes blank, fix
  the reader to use `duration` rather than keeping the duplicate write.

### 4. Drop `availability_pct`, derive at read time

- Remove the column from the model
  ([models/result.go:125](server/internal/db/models/result.go:125)) and stop
  computing/storing it in the aggregation job
  ([job_aggregation.go:1194](server/internal/jobs/jobtypes/job_aggregation.go:1194)).
- The results API keeps its shape: `with=availabilityPct`
  ([results/service.go:333](server/internal/handlers/results/service.go:333))
  now computes `successfulChecks / totalChecks × 100` at serialization time
  (null when `totalChecks == 0`, matching the existing convention in
  [availability/service.go:78](server/internal/handlers/availability/service.go:78)).
  Reuse/mirror the existing `AvailabilityPct()` helper pattern used by
  availability and badges.
- Include the drop in the same migration `009` (both backends, with down
  migration). No backfill needed on down: the column can be repopulated from
  counts if ever restored.
- Update `openapi.yaml` field descriptions if they mention storage semantics
  ([openapi.yaml:6883](server/internal/app/openapi/openapi.yaml:6883),
  [openapi.yaml:6976](server/internal/app/openapi/openapi.yaml:6976)) — the
  JSON contract itself must not change.

### 5. Column projection for count-only readers

- Add a projection option to the results listing path (e.g. an option on the
  store call that skips `metrics`/`output`) and use it from the consumers
  that only need status/counts: uptime bars, status pages, badges
  ([postgres.go:1876](server/internal/db/postgres/postgres.go:1876), consumers
  in `server/internal/handlers/uptimebar/`). Both backends. Keep the default
  behavior (full row) for everything else.

### Constraints

- **SQLite parity is mandatory** — every schema/query change lands in both
  `server/internal/db/postgres/` and `server/internal/db/sqlite/` (SQLite
  ≥3.35 supports `ALTER TABLE … DROP COLUMN`).
- API responses keep their exact JSON shape; only storage changes.
- Comprehensive tests: single-INSERT save path (no UPDATE), hour rollups with
  NULL output, HTTP results with nil metrics, `availabilityPct` derived
  correctly incl. the `totalChecks == 0` → null case, projection returns no
  blobs, and migration 009 up/down on both backends.

### Out of scope

Tier 2 (table split, region FK, SQLite binary encodings), Tier 3 (output
dedup), partitioning, and the `month→year` constraint cleanup — all tracked
in the idea doc.

## Implementation Plan

### Verification guards (run before deleting)

- **`last_for_status` readers:** repo-wide grep found no SELECT/WHERE reader
  outside the maintenance UPDATE itself. Every `LastForStatus` reference is a
  *writer* setting `&lastForStatus` on a fresh `models.Result` before insert
  (`checkworker/backend/direct.go`, `db/postgres/postgres.go`,
  `db/sqlite/sqlite.go`, `jobs/jobtypes/job_aggregation.go`,
  `handlers/heartbeat/service.go`, `handlers/emailcheck/handler.go`,
  `handlers/workers/service.go`) plus `worker_test.go`. Spec claim holds — safe
  to delete.
- **hour-rollup `output` readers:** uptime bars / badges / status pages read
  raw-row `output` only via the recent-detail path; none render `output` from a
  `period_type='hour'` row. Day/month rollups already NULL it. Safe.
- **`metrics.duration_ms` (HTTP) readers:** dash0 response-time charts read the
  `durationMs`/`duration` field, never `metrics.duration_ms`. Backend badges use
  the `duration` column. Safe to drop the single-key HTTP metrics maps.
- **`availability_pct` model-field readers:** only two —
  `jobs/jobtypes/job_aggregation.go` (rollup averaging) and
  `handlers/results/service.go` (`with=availabilityPct` serialization). Both are
  reworked below; everything else uses the `BucketStats.AvailabilityPct()`
  method or the API response DTO (unchanged).

### Migration number

Highest existing migration is `008_v0_7_0` (both backends; v0.7.0 is the current
untagged dev release — latest git tag is v0.6.2). Each migration file maps to a
unique release version, so the new one is **`009_v0_8_0`** covering BOTH column
drops. A fresh migration (not an append to 008) avoids the silent-skip of an
appended block on already-migrated dev DBs.

### Steps (each keeps the tree buildable)

1. **Drop `last_for_status` end to end** — remove the clearing UPDATE from
   `SaveResultWithStatusTracking` (both backends → single INSERT), the model
   field, all 7 writer assignments + their `lastForStatus` locals, the rollup
   builder assignment, and the `service.go` doc. Add migration `009` (both
   backends) dropping the index + column, down restoring both. Delete
   `TestLastForStatus` and strip the field from the two other worker_test rows.
2. **Stop copying raw `output` into hourly rollups** — drop `lastOutput`
   tracking in `processRawResult`; hour rows get an empty/NULL output like
   day/month.
3. **Stop writing `metrics={duration_ms}` for HTTP** — drop the 9 single-key
   `Metrics` maps in `checkhttp/checker.go`; leave `Metrics` nil.
4. **Drop `availability_pct`, derive at read time** — remove the model field,
   remove the aggregation compute path (`availabilitySum` state, the read in
   `processAggregatedResult`, the availability return from the metric helpers,
   the builder assignment), and compute `successfulChecks/totalChecks×100` in
   `handlers/results/service.go applyAggregationFields` (nil when
   `totalChecks==0`, preserving the `omitempty` shape). Same migration `009`.
   Update aggregation tests to assert counts instead of the removed field.
5. **Column projection for count-only readers** — add `SkipBlobs bool` to
   `ListResultsFilter`; when set, `ListResults` (both backends) excludes
   `metrics`+`output`. Opt in from uptimebar (`WindowAvailability`,
   `BucketAvailability`), badges, and status-page recent queries. Default stays
   full-row.

Tests: single-INSERT save path (two same-status saves both persist), hour
rollup NULL output, HTTP nil metrics, `availabilityPct` derivation incl.
`totalChecks==0`, projection returns no blobs, migration 009 up/down both
backends.
