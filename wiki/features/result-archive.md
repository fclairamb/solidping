# Result Archive (Vortex)

Optional archive of the result rows a compaction deletes, written as Vortex
files (`github.com/fclairamb/vortex-go`, pure Go) through the generic file
storage. Off by default (`archive.enabled`, `SP_ARCHIVE_ENABLED`); with it off,
nothing is written and compaction is unchanged. Spec
`2026-11-25-01-vortex-archive-on-compaction`. Operator doc:
`web/docs/docs/configuration/result-archive.md`.

## Where the code is

| Piece | File |
|---|---|
| Switch | `server/internal/config/config.go` (`ArchiveConfig`) |
| Group, prefix delete | `server/internal/handlers/filestorage/` (`GroupTypeArchives`, `DeletePrefix` on `localfs` and `s3fs`) |
| Archiver, keys, encoder | `server/internal/archive/` |
| Hook | `server/internal/jobs/jobtypes/job_aggregation.go`, inside the `CompactResults` closure |
| Org deletion | `server/internal/handlers/auth/org_delete.go` |
| Metrics | `server/internal/prommetrics/` (`solidping_archive_{rows,bytes,failures}_total{tier}`) |

## Layout

The key follows the storage convention `<orgUID>/<group>/<fileID>`, so on S3
it sits under `filestorage.s3_prefix`:

- raw to hour: `<orgUID>/archives/<checkUID>/<YYYY-MM-DD>/<HH>.vortex`, the raw rows of the hour;
- hour to day: `<orgUID>/archives/<checkUID>/<YYYY-MM-DD>.vortex`, the hour rollups of the day;
- day to month: nothing (month rows stay in the database).

UTC. Regions are a column, not part of the path: the per-region buckets of a
check land in the same file. **No `files` row** per archive: the key is
deterministic, so the blob is addressed by key only. Consequence: anything that
reaps blobs from `files` rows does not see archives; org deletion removes them
with `FileStorage.DeletePrefix(org, archives, "")`.

## Schema

`archive.Row`: one row per result, the data columns of `models.Result`
(`uid`, `check_uid`, `region`, `period_type`, `period_start`, `period_end`,
`worker_uid`, `status`, `duration`, `metrics`, `output`, `maintenance`, the
rollup counters and duration stats, `created_at`). Nullable columns are
nullable in the file; `metrics` and `output` are JSON text; timestamps are
microseconds UTC. Rows are sorted by `period_start`, then `uid`. Written with
`vortex.Compact` compression, newest edition.

## Invariants

- **Archive before delete.** The closure archives exactly the measurable source
  rows (`measurableSources`: lifecycle markers are not deleted, so not
  archived) and an archive error aborts the `CompactResults` transaction: the
  bucket stays raw, no rollup is committed, the next run retries. Fail closed:
  a storage outage stalls compaction (the DB grows, `archive_failures_total`
  climbs) rather than losing history.
- **Merge, never overwrite.** A bucket can be compacted again after a late row,
  and two regions share a file: `Archive` reads the existing file
  (`ErrFileNotFound` means none), merges by `uid` (the new row wins) and writes
  the union. An existing file that cannot be decoded is an error, not an
  overwrite.
- **Write-only.** Nothing in SolidPing reads the files except that merge.

## Known trade-offs

- The storage read and write happen inside the compaction transaction. On
  SQLite that holds the write lock for the round trip; fine on local disk, a
  latency cost on a slow S3. If it hurts, move the archive write before the
  transaction and re-check the source UIDs inside it.
- Global switch only, no per-org opt-in (v1).

## Tests

- `server/internal/archive/*_test.go`: keys (UTC boundaries, month rollover,
  `BuildPath`/`ParsePath` round trip), encode/decode round trip with nulls and
  JSON, merge, archiver on `localfs` (late row union, corrupt file fails,
  no-ops), org reap.
- `server/internal/jobs/jobtypes/job_aggregation_archive_test.go`: failing
  archiver keeps the bucket raw then the next run succeeds; disabled writes
  nothing; enabled through config for both tiers; marker-only bucket.
- `server/internal/handlers/auth/org_delete_test.go`
  (`TestDeleteOrgReapsItsResultArchives`).
- `localfs` / `s3fs` `DeletePrefix` tests.
