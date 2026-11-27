---
sidebar_position: 11.5
title: Result Archive
---

# Result Archive

The aggregation job rolls old results up into hourly, daily and monthly
summaries and deletes the finer rows (see [Data Retention](/configuration/data-retention)).
The result archive keeps those finer rows anyway: before a compaction deletes
them, SolidPing writes them as [Vortex](https://vortex.dev) files in the
[file storage](/configuration/file-storage). The files are meant for external
tools (DuckDB, Spark, a notebook): SolidPing never reads them back, and they
are not shown in the dashboard or the API.

The archive is **off by default**. When it is off, nothing changes.

## Enabling it

| Variable | Config key | Default | Description |
|---|---|---|---|
| `SP_ARCHIVE_ENABLED` | `archive.enabled` | `false` | Archive the rows a compaction deletes |

There is no other setting: the archive goes to the backend configured by
`SP_FILESTORAGE_*`, local directory or S3 bucket, under the same root or
prefix as the other blobs.

## Layout

| Compaction | File | Contents |
|---|---|---|
| raw to hour | `<orgUid>/archives/<checkUid>/<YYYY-MM-DD>/<HH>.vortex` | The raw results of that hour (temporary) |
| hour to day | `<orgUid>/archives/<checkUid>/<YYYY-MM-DD>.vortex` | Every raw result of that day (long-term) |
| day to month | none | Monthly summaries stay in the database |

The hour files are temporary. When the day is compacted, SolidPing merges the
day's hour files into the day file and deletes them: once a day is compacted,
only `<YYYY-MM-DD>.vortex` remains for it.

The archive only ever holds individual results, never a summary. An hour
compacted before the archive was switched on is absent from the day file, and
a day compacted before it gets no day file at all.

Dates and hours are in UTC. On S3 the keys start with `SP_FILESTORAGE_S3_PREFIX`
when it is set. All the regions of a check share one file: the region is a
column. A file that is written again after a late result keeps its first
rows: it is read back and merged by `uid`.

## Columns

`uid`, `check_uid`, `region`, `period_type`, `period_start`, `worker_uid`,
`status`, `duration`, `metrics`, `output`, `maintenance`, `created_at`.

`period_type` is always `raw`. Timestamps are in microseconds, UTC. `metrics`
and `output` are JSON text. Rows are sorted by `period_start`.

Files written by v0.55 and v0.56 also have the summary columns (`period_end`,
`total_checks`, `successful_checks`, `maintenance_checks`,
`maintenance_successful_checks`, `duration_min`, `duration_max`,
`duration_p95`, `duration_p50`, `duration_avg`), always empty on a result.
Their day files can also hold hourly summary rows (`period_type` = `hour`):
filter on `period_type = 'raw'` when reading them. A later merge into such a
day file drops those rows.

## When the storage fails

The archive fails closed: if a file cannot be written, read back for the merge,
or (for a day) the hour files cannot be deleted, the compaction of that
bucket is rolled back and its rows stay in the database until a later run
succeeds. Nothing is deleted without being archived. The results table grows
in the meantime, so watch these metrics:

| Metric | Labels | Meaning |
|---|---|---|
| `solidping_archive_rows_total` | `tier` | Rows archived |
| `solidping_archive_bytes_total` | `tier` | Bytes of archive files written |
| `solidping_archive_failures_total` | `tier` | Failed archives (compaction kept the rows) |

## Deleting an organization

Deleting an organization deletes its archive files too.
