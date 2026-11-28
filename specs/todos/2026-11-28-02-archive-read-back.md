---
model: sonnet
effort: high
pr: batch
automerge: false
---

# Read archived result details back in the API and the dashboard

## Problem

The result archive (spec `2026-11-25-01`, `wiki/features/result-archive.md`) keeps every raw
result a compaction deletes, as Vortex files in the file storage. Nothing reads them back: the
wiki says "Write-only" (`wiki/features/result-archive.md:74`), the package comment too
(`server/internal/archive/archiver.go:8`), and the operator doc says "SolidPing never reads them
back" (`web/docs/docs/configuration/result-archive.md:13`).

So once a raw row is compacted, the dashboard cannot show it any more:

- `GetResult` (`server/internal/handlers/results/service.go:649`) falls back to the covering
  hour, day or month rollup. A rollup has no `output`, so "why did it fail at 03:12 three weeks
  ago" has no answer.
- `GET /results?periodType=raw` is clamped to the raw retention band by `clampRawWindow`
  (`server/internal/handlers/results/service.go:186`). A past window returns nothing.
- The chart falls back to hourly rollups for a zoomed window older than the raw band
  (`web/dash0/src/lib/chart-window.ts:87`), only because the raw rows are gone.

The archive holds exactly the missing rows. This spec reads them back, server side. The frontend
never downloads or decodes a `.vortex` file.

Line numbers below are from `origin/main` at `ab4f42348`.

## Proposal

### 1. `archived` flag on rollup rows

1. Add a nullable boolean column `archived` to `results`, no default, in a new migration for both
   dialects (`server/internal/db/postgres/migrations/`, `server/internal/db/sqlite/migrations/`;
   see `## To verify` for the number). Add `Archived *bool` to `models.Result`
   (`server/internal/db/models/result.go`, next to `TotalChecks` at line 261).
2. Meaning, on a rollup row (`hour`, `day`, `month`). Raw rows leave it `null`.

   | Value | Meaning | Reader does |
   |---|---|---|
   | `true` | detail was archived under this bucket | reads the files |
   | `false` | compacted with archiving off | no storage call |
   | `null` | compacted before the column existed | probes the storage |

   It means "there is archived detail under this bucket", not "this row has a file": a row is per
   region and a file is per check, hour files are merged into the day file, and day files outlive
   the day rows. No file name is stored: the key is deterministic (`server/internal/archive/key.go`).
3. Set it in the compaction closure (`server/internal/jobs/jobtypes/job_aggregation.go:440`,
   archiver call at line 452), so it commits in the same `CompactResults` transaction as the
   rollup:
   - raw to hour: `true` when the archiver is non-nil and `Archive` returned nil for a non-empty
     batch, `false` when the archiver is nil.
   - hour to day and day to month: `true` if any source row is `true`, else `null` if any source
     row is `null`, else `false`.
4. Never regress. `upsertAggregatedResultTx` (`server/internal/db/postgres/postgres.go:2936`,
   `server/internal/db/sqlite/sqlite.go:2903`) keeps an existing `true` when a bucket is
   compacted again after a late row, even if archiving was switched off in between. Keep the two
   dialects mirrored (`/sync-pg-to-sqlite` convention).
5. Expose it as `archived` on `ResultResponse`
   (`server/internal/handlers/results/service.go:103`), a base field, omitted when `null`.
   Document it in `server/internal/app/openapi/openapi.yaml`.
6. The flag is a hint. A `true` row whose file is missing (deleted by an operator or a bucket
   lifecycle rule) is handled like "no detail", never a 500.

### 2. Archive reader

7. Add a reader to `server/internal/archive/` (new file `reader.go`), over the same
   `filestorage.FileStorage` as `StorageArchiver`, reusing `read`
   (`server/internal/archive/archiver.go:227`), `Decode` (`server/internal/archive/encode.go:91`)
   and `Merge`:
   - `Rows(ctx, orgUID, checkUID, from, to) ([]Row, error)`: for each UTC day the window touches,
     the day file (`DayFileID`) and the hour files (`HourFileID`) of the hours inside the window.
     `ErrFileNotFound` means "no rows", not an error. Reads run in parallel with a small bound
     (8). The result is merged by `uid`, filtered to `[from, to)`, sorted by `period_start` then
     `uid`.
   - `Row(ctx, orgUID, checkUID, uid) (*Row, error)`: the timestamp of the UUIDv7 gives the day
     file and the hour file; return the row with that `uid`, or nil.
   - A `Row` to `models.Result` conversion, the inverse of `RowFromResult`.
8. No cache and no range read in this spec: a day of a 10 s check on 3 regions is a 42 KB file.
9. The reader is nil when `archive.enabled` is false. Every caller below then behaves exactly as
   today.

### 3. `GetResult` by UID

10. In `GetResult` (`server/internal/handlers/results/service.go:649`), after the direct DB lookup
    misses and before the `findCoveringAggregation` loop, look the UID up with the reader. On a
    hit, return it as a normal raw `ResultResponse` with `output` and `metrics`, no `fallback`.
    The check and the org come from `resolveResultScope`, so a UID of another check or org is
    never served: the file read is keyed by the resolved `org.UID` and `check.UID`.
11. `previousUid` / `nextUid` for an archived row: the neighbours inside the same archive read
    (the same day and hour files), within the region scope. Omitted at the edge of the files read.
12. On a miss or a storage error, fall through to the existing rollup fallback. Log the storage
    error, do not fail the request.

### 4. Raw list from the archive

13. In the list path (`clampRawWindow` call at `server/internal/handlers/results/service.go:290`):
    a `periodType=raw` request reads the archive instead of being clamped when all of these hold:
    the reader is non-nil, exactly one `checkUid` is given, and `periodStartAfter` is older than
    the raw band. Otherwise nothing changes.
14. Limits, checked before any storage access:
    - span (`periodEndBefore` or now, minus `periodStartAfter`) over 31 days:
      `400 VALIDATION_ERROR`, detail naming the 31 day limit.
    - more than 10,000 results in the window: `400` with a new code `TOO_MANY_RESULTS`, detail
      giving the count and the limit. The count is the sum of `total_checks` of the rollup rows of
      the check in the window, one DB query, no storage call.
    Both limits are constants next to the reader.
15. The rows are the archive rows of the window merged by `uid` with the DB raw rows of the same
    window (the window can straddle the retention edge, and a late row can sit in both). Region
    and status filters apply in memory. Same response shape, same ordering and same cursor
    pagination as today: one page per response.
16. `window` in the response (`WindowResponse`, around line 139) reports the window actually
    served, `clamped: false`, and a new `source` field: `"archive"` when the archive was read,
    omitted otherwise.
17. Archive rows are only ever served to a raw request. They are never mixed with rollups in a
    count: the "tiers cover disjoint age bands" invariant of `wiki/features/results-aggregation.md`
    stays true for the database.

### 5. dash0

18. `chartRollupTier` (`web/dash0/src/lib/chart-window.ts:70`): the fallback to `"hour"` for a
    past zoom window (line 87) no longer applies when the rollup rows of the window are
    `archived`. The rest of the rule is unchanged: raw is drawn only for a window of 1 hour or
    less, or 24 hours or less for a check slower than 5 minutes. `useChartWindowResults`
    (`web/dash0/src/api/hooks.ts:2591`) carries the flag from pass 1.
19. Check page (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`), for a zoomed past
    window. The count is the sum of `totalChecks` of the rollup rows already loaded:
    - under the limit and archived: a button "Show the N individual results"
      (`data-testid="show-archived-results"`), which loads the raw list of the window into the
      existing results table.
    - over the limit: "N results in this range. Zoom in to under 10,000 to see them one by one."
      (`data-testid="archived-results-too-many"`).
    - not archived (`false`, or `null` and the list came back empty): "No detail archived for
      this period." (`data-testid="archived-results-none"`).
20. The result detail page needs no change: an archived UID now returns a raw row.
21. Reuse the primitives of the design reference page. No new one is expected.

### 6. Docs

22. `wiki/features/result-archive.md`: replace the "Write-only" invariant with the read path
    (reader, limits, the `archived` flag and its three values), and list the new tests.
23. `web/docs/docs/configuration/result-archive.md`: the dashboard and the API now read the
    files. Explain the 10,000 results and 31 day limits and the "No detail archived" case.
24. `wiki/features/results-aggregation.md`: the `archived` column and how it propagates.
25. `server/internal/archive/archiver.go:8`: update the package comment.
26. `CHANGELOG.md` entry (`wiki/conventions/changelog.md`).

### Out of scope

- A failures-only search across windows over the limits.
- Retention or pruning of the archive files.
- Per-org switch or entitlement.
- Status pages (status0) and the public API of status pages.
- Caching decoded files, range reads on S3.

## Tests

- `server/internal/archive/reader_test.go` (on `localfs`):
  - a window inside one day file;
  - a window across two UTC days;
  - a day with hour files only, and a day with a day file plus a late hour file (merged by `uid`,
    no duplicate);
  - no file at all returns no rows and no error;
  - a corrupt file returns an error;
  - `Row` by UID: found, not found, and a UID of another check not found.
- `server/internal/jobs/jobtypes/job_aggregation_archive_test.go`:
  - raw to hour sets `archived` true with the archiver, false without;
  - hour to day and day to month: true / null / false propagation;
  - a recompaction after a late row with archiving off keeps `true`;
  - a failing archiver leaves no rollup row (existing behaviour, still passing).
- Migration tests for both dialects: the column exists and is `null` on existing rows.
- `server/internal/handlers/results/` service tests:
  - `GetResult` of a compacted UID returns the raw row with `output`, no `fallback`;
  - archiving off, or the file missing: the rollup `fallback` as today;
  - a UID that exists in another org's archive: not found;
  - raw list on a past window: rows from the archive, `window.source = "archive"`, pagination
    across two pages returns each row once;
  - a window straddling the retention edge: DB and archive rows, no duplicate;
  - 32 day span: `400 VALIDATION_ERROR`; 10,001 results: `400 TOO_MANY_RESULTS`, and in both
    cases the storage was not read (a storage that fails the test when called);
  - two `checkUid` values, or archiving off: clamped as today (negative case).
- `web/dash0/src/lib/chart-window.test.ts` (or the existing chart-window test file):
  `chartRollupTier` on a past window, archived and not archived.
- Playwright, `web/dash0/e2e/`: on a check with archived history, zoom on a past window, click
  `show-archived-results`, open a result and see its output. A window over the limit shows
  `archived-results-too-many` and no button (the positive control is the first case).

## To verify

- The migration number. `043_v0_57_0` is the newest migration and the v0.57.0 release PR is open.
  Run `git tag --contains $(git log --diff-filter=A --format=%h -1 -- <file>)` on it: if a tag
  contains it, add `044`; otherwise extend `043` (AGENTS.md, step 4b).
- How `CompactResults` passes the rollup to `upsertAggregatedResultTx`, and where the closure can
  set `Archived` from the source rows (`server/internal/db/postgres/postgres.go:3278`,
  `server/internal/db/sqlite/sqlite.go:3243`).
- Whether a test-mode fixture with archive files exists for the Playwright case. If not, the
  side-car server needs `SP_ARCHIVE_ENABLED=true` and a seed that compacts a past bucket.
- The exact cursor format of the list endpoint, so archive rows page with the same cursor.
- That a `Row` decodes back to every field the result detail page shows (the scan-only check
  info and the transient diagnostics are not in `archive.Row`).
