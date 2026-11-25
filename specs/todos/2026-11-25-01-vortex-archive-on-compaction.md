---
model: opus
effort: high
pr: batch
automerge: false
---

# Optionally archive compacted results as Vortex files in the file storage

## Problem
The aggregation job compacts results tier by tier (raw to hour to day to month) and deletes the source rows
(`server/internal/jobs/jobtypes/job_aggregation.go:359`, `CompactResults` at `server/internal/db/service.go:717`).
Once deleted, the fine-grained data is gone. Operators who want long-term history (audit, ML, ad-hoc analytics
in DuckDB/Spark) have no way to keep it cheaply. Request: an optional archive that writes the rows being
compacted as Vortex files (`github.com/fclairamb/vortex-go`, pure Go, no cgo), one per check and hour and one
per check and day.

Everything is optional: off by default, no behaviour change when unset.

## Proposal
**Storage: the generic file storage, not a dedicated S3 client** (Florent, 2026-11-25). Archives go through
`filestorage.FileStorage` (`server/internal/handlers/filestorage/filestorage.go:58`), the seam already used for
screenshots, reports and logos, with its `local` and `s3` backends configured by `filestorage.*` /
`SP_FILESTORAGE_*` (`server/internal/config/config.go:1349`). Consequences:

- No bucket, endpoint, region or credential setting for archives. The only new setting is the on/off switch.
- Archives work on the local backend too (default), not only on S3.
- The key follows the storage convention `<orgUID>/<group>/<fileID>` (`BuildPath`, `filestorage.go:83`), so the
  layout is `<s3_prefix>/$orgUid/archives/$checkUid/$date/$hour.vortex` and
  `<s3_prefix>/$orgUid/archives/$checkUid/$date.vortex` (`$date` = `YYYY-MM-DD`, `$hour` = `HH`, UTC). This is
  `$orgUid/archives/...`, not the `archives/$orgUid/...` of the original request: do not special-case the path.
- New group `GroupTypeArchives GroupType = "archives"` next to the others (`filestorage.go:27`). Server-written,
  never served over HTTP.
- **No `files` table row per archive.** The key is deterministic and one row per check and hour would add
  about 25 rows per check per day forever. The blob is addressed by its key only.

Other decisions:

- **What goes in which file.** Archive exactly the rows a compaction deletes, so nothing is ever deleted unarchived:
  the raw to hour compaction writes `$date/$hour.vortex` (the raw rows of that hour); the hour to day compaction
  writes `$date.vortex` (the 24 hour rollups). No file for day to month (month rows stay in the DB, they are tiny).
  Regions are rows inside the file (a `region` column), not part of the path.
- **Durability order.** Write the file first, delete after. A failed write rolls the compaction transaction back,
  the bucket stays raw and the next run retries. No archive-less deletion when archiving is enabled.
- **Idempotent keys.** A bucket can be compacted again when a late row arrives (the rollup upsert already handles
  this): `ReadFile` the existing blob (`ErrFileNotFound` means none), merge by result `uid`, `WriteFile` the union.
  Never overwrite blindly (that would lose the first batch).
- **Write-only in v1.** No read path in SolidPing (no UI, no API). The files are for external tools.
- **Org deletion.** Deleting an organization must also delete its `archives` blobs (GDPR). With no `files` rows,
  whatever reaps blobs from those rows will not see them (see To verify).

Steps:
1. Config: `archive.enabled` (default false) in `server/internal/config/config.go`, env `SP_ARCHIVE_ENABLED`
   (single-word leaf, reachable by koanf's env loader). Nothing else. Document in `web/docs/` configuration,
   next to the file storage page.
2. `GroupTypeArchives` in `filestorage.go`.
3. New package `server/internal/archive`: `Archiver` interface
   (`Archive(ctx, orgUID, checkUID, tier, start, rows) error`), an implementation over `filestorage.FileStorage`
   (obtained with `filestorage.NewFileStorage`, as the other writers do), the fileID builder
   (`<checkUid>/<YYYY-MM-DD>/<HH>.vortex`, `<checkUid>/<YYYY-MM-DD>.vortex`), and the Vortex encoder (one struct
   per row, sorted by `period_start`, `uid` as the dedupe key). Columns: uid, region, period_type, period_start,
   period_end, status, duration, metrics, output and the rollup counters, taken from `models.Result`
   (`server/internal/db/models/result.go`). JSON fields stored as UTF-8 strings. MIME type
   `application/vnd.apache.vortex` (or `application/octet-stream` if there is no registered type).
4. Hook in `job_aggregation.go` inside the `CompactResults` aggregate closure (line ~359), after
   `measurableSourceUIDs` and before returning: call `Archiver.Archive` for the measurable sources only
   (lifecycle markers are not deleted, so not archived). An error aborts the transaction.
5. Metrics: archived rows, bytes, failures (use `internal/prommetrics`). One WARN log per failure with
   check and bucket, never the row contents.
6. Wiki note in `wiki/features/` on the layout, schema and the "archive before delete" invariant.

## Tests
- `archive/key_test.go`: fileID layout for hour and day, UTC boundaries (23:59:59.999 vs 00:00), month rollover,
  and the full key through `filestorage.BuildPath` / `ParsePath` round trip.
- `archive/encode_test.go`: round trip rows through vortex-go and back, with nullable fields and a JSON output.
- `archive/archiver_test.go` on the `localfs` backend in a temp dir: write, then a second write with a late row
  gives the union without duplicates.
- `job_aggregation_test.go`: archiving enabled + failing archiver, the bucket stays raw, no rollup row, next run
  succeeds (negative case). Archiving disabled: identical behaviour to today, no blob written.
- Marker-only bucket: nothing written.
- Org deletion removes the org's `archives` blobs, and leaves another org's untouched.

## To verify
- A fileID that contains `/` on both backends. `BuildPath`/`ParsePath` (`SplitN` on 3 parts) and `localfs`
  (`MkdirAll` on the parent, `localfs.go:55`) look fine; check `s3fs` (`s3fs.go:111`) and any fileID validation.
- How org deletion reaps blobs today. `FileStorage` has no list or prefix delete (`filestorage.go:58`). If the
  reaper walks `files` rows, archives need either a prefix delete added to the interface (both backends), or an
  enumeration of the org's checks and dates. Prefer the prefix delete.
- vortex-go writer API (`~/code/fclairamb/vortex-go/api.txt`, `docs/interface.md`) and whether a compression
  mode ("compact") is selectable; use it for the long-storage files. Check the module's Go version (1.26) against
  `server/go.mod`.
- Doing a storage write inside the DB transaction of `CompactResults` (SQLite holds the write lock). If it
  blocks other writers too long on S3, fetch and write before the transaction and re-check the source UIDs inside it.
- Exact set of `models.Result` columns and which are worth archiving.

## Open questions
1. Hour file = raw rows, day file = hour rollups (as proposed), or day file = everything of the day? Recommended:
   as proposed, because it matches what the DB deletes and keeps files small.
2. When the storage is down, stall compaction (fail closed, DB grows) or skip archiving and delete anyway?
   Recommended: fail closed with a metric, losing history silently is worse than a temporary DB growth.
3. Per-org opt-in later (entitlement in SaaS mode) or global only? Recommended: global only for v1 (YAGNI).
