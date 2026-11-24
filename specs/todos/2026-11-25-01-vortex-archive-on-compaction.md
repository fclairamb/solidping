---
model: opus
effort: high
pr: batch
automerge: false
---

# Optionally archive compacted results to S3 as Vortex files

## Problem
The aggregation job compacts results tier by tier (raw to hour to day to month) and deletes the source rows
(`server/internal/jobs/jobtypes/job_aggregation.go:359`, `CompactResults` at `server/internal/db/service.go:717`).
Once deleted, the fine-grained data is gone. Operators who want long-term history (audit, ML, ad-hoc analytics
in DuckDB/Spark) have no way to keep it cheaply. Request: an optional archive that writes the rows being
compacted to S3 as Vortex files (`github.com/fclairamb/vortex-go`, pure Go, no cgo), at:

- `s3://$bucket/archives/$orgUid/$checkUid/$date/$hour.vortex`
- `s3://$bucket/archives/$orgUid/$checkUid/$date.vortex` (`$date` = `YYYY-MM-DD`)

Everything is optional: off by default, no behaviour change when unset.

## Proposal
Adjustments to the request (each is a decision taken here, see Open questions for the ones to confirm):

- **What goes in which file.** Archive exactly the rows a compaction deletes, so nothing is ever deleted unarchived:
  the raw to hour compaction writes `$date/$hour.vortex` (the raw rows of that hour); the hour to day compaction
  writes `$date.vortex` (the 24 hour rollups). No file for day to month (month rows stay in the DB, they are tiny).
  Regions are rows inside the file (a `region` column), not part of the path.
- **Durability order.** Upload first, delete after. A failed upload rolls the compaction transaction back, the
  bucket stays raw and the next run retries. No archive-less deletion when archiving is enabled.
- **Idempotent keys.** The key is deterministic, so a retry overwrites. A bucket can be compacted again when a
  late row arrives (the rollup upsert already handles this): if the object exists, read it, merge by result `uid`,
  rewrite. Never overwrite blindly (that would lose the first batch).
- **Write-only in v1.** No read path in SolidPing (no UI, no API). The files are for external tools.
  Marking this explicit avoids a second storage backend to maintain.
- **Org deletion.** Because `$orgUid` is the first path segment, deleting an organization must also delete
  `archives/$orgUid/` (GDPR). Include it in the org deletion path when archiving is on.

Steps:
1. Config: `archive.enabled` (default false), `archive.bucket`, `archive.prefix` (default `archives`),
   `archive.endpoint` (MinIO / S3-compatible), `archive.region`, credentials via the standard AWS chain or
   `SP_ARCHIVE_*`. Validation: enabled requires a bucket. File: `server/internal/config/config.go` (follow
   the `koanf` + `SP_` env pattern near line 1392). Document in `web/docs/` configuration.
2. New package `server/internal/archive`: `Archiver` interface (`Archive(ctx, orgUID, checkUID, tier, start, rows) error`),
   an S3 implementation, key builder (`archives/<org>/<check>/<YYYY-MM-DD>/<HH>.vortex` and `<YYYY-MM-DD>.vortex`,
   UTC), and the Vortex encoder (one struct per row, sorted by `period_start`, `uid` as the dedupe key).
   Columns: uid, region, period_type, period_start, period_end, status, duration, metrics, output and the rollup
   counters, taken from `models.Result` (`server/internal/db/models/result.go`). JSON fields stored as UTF-8 strings.
3. Hook in `job_aggregation.go` inside the `CompactResults` aggregate closure (line ~359), after
   `measurableSourceUIDs` and before returning: call `Archiver.Archive` for the measurable sources only
   (lifecycle markers are not deleted, so not archived). An error aborts the transaction.
4. Metrics: archived rows, bytes, failures (use `internal/prommetrics`). One WARN log per failure with
   check and bucket, never the row contents.
5. Wiki note in `wiki/features/` on the layout, schema and the "archive before delete" invariant.

## Tests
- `archive/key_test.go`: key layout for hour and day, UTC boundaries (23:59:59.999 vs 00:00), month rollover.
- `archive/encode_test.go`: round trip rows through vortex-go and back, with nullable fields and a JSON output.
- `archive/s3_test.go` (in-memory S3 fake or testcontainers MinIO under the slow tag): write, rewrite with a late
  row gives the union without duplicates.
- `job_aggregation_test.go`: archiving enabled + failing archiver, the bucket stays raw, no rollup row, next run
  succeeds (negative case). Archiving disabled: identical behaviour to today.
- Marker-only bucket: nothing uploaded.
- Config: enabled without bucket is rejected.

## To verify
- Whether the repo already has an S3 client (feedback screenshots, `FeedbackRetentionDays`, `config.go:1392`)
  to reuse. The AWS SDK v2 is only an indirect dependency in `server/go.mod`, so a new direct dependency may be
  needed; prefer the one already used, else the smallest S3 client that supports custom endpoints.
- vortex-go writer API (`~/code/fclairamb/vortex-go/api.txt`, `docs/interface.md`) and whether a compression
  mode ("compact") is selectable; use it for the long-storage files. Check the module's Go version (1.26) against
  `server/go.mod`.
- Doing a network upload inside the DB transaction of `CompactResults` (SQLite holds the write lock). If it
  blocks other writers too long, fetch and upload before the transaction and re-check the source UIDs inside it.
- Exact set of `models.Result` columns and which are worth archiving.

## Open questions
1. Hour file = raw rows, day file = hour rollups (as proposed), or day file = everything of the day? Recommended:
   as proposed, because it matches what the DB deletes and keeps files small.
2. When S3 is down, stall compaction (fail closed, DB grows) or skip archiving and delete anyway? Recommended: fail
   closed with a metric, losing history silently is worse than a temporary DB growth.
3. Per-org opt-in later (entitlement in SaaS mode) or global only? Recommended: global only for v1 (YAGNI).
