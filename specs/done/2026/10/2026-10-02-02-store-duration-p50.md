# Store a duration median (p50) on rollup rows and seam bins

## Problem

A user asked for "the average over the time range I'm looking at or like the
median", and explicitly said p95 is not what he wants for this. The stats strip
shows avg and p95 but no median, and a median cannot be derived from what is
stored.

`results` carries four duration aggregates and no p50:
[`server/internal/db/models/result.go:277`](server/internal/db/models/result.go:277)
— `duration_min`, `duration_max`, `duration_p95`, `duration_avg`. The
aggregation job fills them in
[`server/internal/jobs/jobtypes/job_aggregation.go:1160`](server/internal/jobs/jobtypes/job_aggregation.go:1160),
computing avg and p95 only
([`calculateRawMetrics`](server/internal/jobs/jobtypes/job_aggregation.go:1195)).
The status page's seam aggregate has the same four and no p50:
[`server/internal/db/models/response_time_bin.go:94`](server/internal/db/models/response_time_bin.go:94).

So the UI can only show a median inside the raw tier, by sorting the raw points
it already holds. The moment the window reaches back past raw retention the
number would silently change meaning or disappear, which is worse than not
having it.

## Proposal

Add `duration_p50` alongside the existing four, end to end:

1. **Schema** — a nullable float column `duration_p50` on `results`, in a new
   per-release migration file for both engines
   (`server/internal/db/postgres/migrations/`,
   `server/internal/db/sqlite/migrations/`; latest is `025_v0_34_0`). Per
   [`wiki/conventions/migrations.md`](wiki/conventions/migrations.md) the
   released files are frozen, so this is a new `NNN_vX_Y_Z` pair, with a
   `comment on column` on the Postgres side. NULL means "this row predates the
   column" and must stay distinguishable from zero, exactly as the other
   duration pointers are nil-not-zero today.
2. **Aggregation job** — `calculateRawMetrics` returns a third value: the
   nearest-rank p50 over the same sorted `durations` slice it already builds for
   p95, so one sort serves both. `calculateAggregatedMetrics` combines child
   p50s the same way it combines p95s (unweighted mean of the children's own
   p50s), and skips children whose `DurationP50` is nil instead of treating them
   as zero. Reuse the shared nearest-rank index helper rather than writing a
   second rounding rule — the existing p95 index is pinned by a test precisely
   because a second rule would drift.
3. **Seam aggregate** — `ResponseTimeBin.DurationP50 *float32`, computed in both
   dialects with the same nearest-rank index the job uses, mirroring what
   `ResponseTimeBinP95Index` does for p95.
4. **Read path and UI** — carry it through the results API alongside
   `durationP95Ms`, and add a median to `computeDurationStats`
   ([`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:319`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:319))
   and to the strip. Raw rows contribute their own duration as a degenerate
   single-sample median, as they already do for p95.
5. **Old rows have no median.** Render the median only when every contributing
   row carried one, or mark it `~` the way `isEstimate` already marks combined
   avg/p95. Do not backfill: the raw rows those rollups came from are gone.

## Open questions

- Is a median worth a column, or should the strip show the p50 only for windows
  that are entirely raw and omit it otherwise? The column is the honest answer
  and costs 4 bytes per rollup row; the cheap answer ships in an afternoon and
  makes the number appear and disappear depending on the range. Pick the column
  unless the row-count math in `job_aggregation_rowcount.go` says otherwise.
- `calculateAggregatedMetrics` averaging children's percentiles is already a
  known approximation for p95. The same approximation on p50 is tighter but
  still wrong in principle. Decide whether that is acceptable or whether this
  wants a t-digest, and write the decision down next to the code — it is the
  question that will be asked again.

## Resolved open questions

- Store a real p50 column on rollup rows and seam bins (the honest answer), so the median shows for every range. Check the row-count math in `job_aggregation_rowcount.go` first and note the result in the commit body.
- Averaging children's p50 is an accepted approximation, same as p95. No t-digest. Write this decision in a comment next to `calculateAggregatedMetrics`.

## Not in scope

Any other percentile, and the two sibling items from the same report:
`2026-10-02-01-duration-stats-strip-hidden-without-region.md` and
`2026-10-02-03-response-time-trend-vs-previous-window.md`.
