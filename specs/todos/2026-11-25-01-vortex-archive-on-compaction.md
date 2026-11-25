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

Scope of this stage (Florent, 2026-11-25): optional, disabled by default, global (one switch for the whole
instance, no per-org setting), and write only. SolidPing never serves or queries the archives. No behaviour
change when the switch is off.

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

- **What goes in which file** (Florent, 2026-11-25). The hour file is temporary, the day file is the long-term one
  and holds everything:
  - raw to hour compaction writes `$date/$hour.vortex` with the raw rows of that hour;
  - hour to day compaction writes `$date.vortex` with all the raw rows of the day (the union of the day's hour
    files), then deletes those hour files. After it, `$date/` is empty and only `$date.vortex` remains;
  - an hour that has no raw row in the archive (compacted before archiving was switched on) contributes its hour
    rollup row instead, so the day is not silently missing. The `period_type` column tells the two apart;
  - no file for day to month (month rows stay in the DB, they are tiny).
  Regions are rows inside the file (a `region` column), not part of the path.
- **All or nothing** (Florent, 2026-11-25). When archiving is on, a bucket is compacted only if its archive was
  written. Any storage error (write, read of the file to merge, delete of the hour files) aborts the compaction
  transaction: nothing is archived, nothing is compacted, the rows stay in the DB and the next run retries. There
  is no "skip the archive and delete anyway" path, and no partial state to repair. The DB grows while the storage
  is unavailable: expose it with a metric and a WARN log.
- **Order inside one compaction.** Write the archive, (day step) delete the hour files, then let the transaction
  delete the DB rows. The day file already holds everything when the hour files go, so a failure after that point
  loses nothing and the retry is a plain merge.
- **Idempotent keys.** A bucket can be compacted again when a late row arrives (the rollup upsert already handles
  this): `ReadFile` the existing blob (`ErrFileNotFound` means none), merge, `WriteFile` the union. Raw rows are
  deduplicated by result `uid`, rollup rows by (region, period_type, period_start), last one wins. Never overwrite
  blindly (that would lose the first batch).
- **Write only.** No read path in SolidPing (no UI, no API, no query falls back to the archive). The files are for
  external tools. The only reads are the archiver reading back its own files to merge them.
- **Global only.** One instance-wide switch. No per-org opt-in, no entitlement.
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
   - target `hour`: write the hour file from the raw sources.
   - target `day`: read the existing day file and the day's hour files, merge, add the rollup row of each hour
     that has no raw row, write the day file, delete the hour files.
   - target `month`: nothing.
5. Metrics: archived rows, bytes, failures (use `internal/prommetrics`). One WARN log per failure with
   check and bucket, never the row contents.
6. Wiki note in `wiki/features/` on the layout, schema and the "archive before delete" invariant.

## Tests
- `archive/key_test.go`: fileID layout for hour and day, UTC boundaries (23:59:59.999 vs 00:00), month rollover,
  and the full key through `filestorage.BuildPath` / `ParsePath` round trip.
- `archive/encode_test.go`: round trip rows through vortex-go and back, with nullable fields and a JSON output.
- `archive/archiver_test.go` on the `localfs` backend in a temp dir:
  - hour write, then a second write with a late row gives the union without duplicates;
  - day step: 24 hour files become one day file with every raw row, and the hour files are gone;
  - day step with an hour that has no hour file: its rollup row is in the day file;
  - day step run twice (late row): same day file plus the late row, no duplicate.
- `job_aggregation_test.go`, all or nothing, with a storage that fails on write, on read and on delete in turn:
  the bucket stays as it was (source rows present, no rollup row, hour files still there), and the next run with a
  working storage compacts and archives it (negative cases).
- `job_aggregation_test.go`: archiving disabled gives identical behaviour to today and writes no blob.
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
- Size of a day file held in memory during the merge (a 10 s check on 3 regions is about 26,000 raw rows a day).
  If a busy check makes it too large, stream hour file by hour file into the writer instead of loading all rows.
- vortex-go reads back what it wrote (needed for the merge), including with the compact mode.
