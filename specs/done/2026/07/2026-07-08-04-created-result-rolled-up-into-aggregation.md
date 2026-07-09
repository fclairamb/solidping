# `created` lifecycle results get swallowed by aggregation and render as a wrong aggregated row

## Problem

Observed on
`https://solidping.k8xp.com/dash0/orgs/webingenia/checks/4e52ef3c-cd9f-49d2-92ab-7139af22cc46/results/019f3480-e1cc-7645-9380-2497e7d983ed`:
the result detail page shows an **aggregated** row, but the underlying result
was the check's `CREATED` lifecycle marker. It should display as a `created`
result and should not be presented as (or absorbed into) an aggregation.

Mechanism:

1. The aggregation job **excludes** lifecycle-marker rows (`created`,
   `running`) from the rollup stats
   (`server/internal/jobs/jobtypes/job_aggregation.go:725-726`) — correct,
   they are non-measurement states
   (`server/internal/db/models/result.go:66-70`).
2. But the deletion step then deletes **every** source row in the window,
   including those excluded lifecycle markers
   (`server/internal/jobs/jobtypes/job_aggregation.go:183-193` — UIDs are
   collected from the full `ListResults` source set, not the aggregated
   subset).
3. When the deleted `created` result's URL is visited, `GetResult` falls back
   to the smallest covering aggregation via the UUIDv7 embedded timestamp
   (`server/internal/handlers/results/service.go:456-498`) and serves the
   hour/day/month rollup with a `rolled_up_to_*` fallback banner.

Net effect: the one raw row that records "this check was created here" is
destroyed by rollup even though it contributed nothing to the rollup, and its
permalink silently morphs into an aggregation page — which reads as a wrong
aggregation.

## Proposal

Preserve lifecycle-marker raw rows across aggregation:

- In the aggregation job, skip lifecycle-marker rows (`created`, `running`)
  when collecting the source UIDs to delete
  (`job_aggregation.go:183-187`), reusing
  `models.ResultStatus.IsLifecycleMarker()`. They are already excluded from
  the aggregate's stats, so keeping them changes no rollup numbers.
- These rows are rare (typically one `created` per check lifetime; `running`
  rows are transient and normally overwritten by the final status), so the
  storage cost of keeping them as `period_type = raw` forever is negligible.
- Verify the result detail page then resolves the UID directly again
  (`results/service.go:442-454`) and renders the `CREATED` status with no
  fallback banner.
- Tests: aggregation-job test asserting that a window containing
  `created`/`running` rows rolls up the measurable rows, deletes them, and
  **keeps** the lifecycle rows; results-service test asserting `GetResult` on
  a kept lifecycle row returns it raw (no `fallback` field).

### Open questions

- Should `running` rows be kept too, or only `created`? The skip-from-stats
  logic treats them identically, so symmetric handling (keep both) is the
  simplest and is what the proposal above does.
- Existing data: rows already deleted by past rollups are unrecoverable; the
  fallback banner remains the best-effort behavior for those. No backfill
  proposed.
- Neighbor navigation (`previousUid`/`nextUid`) mixes period types per series;
  confirm a kept raw lifecycle row doesn't confuse raw-series navigation once
  everything around it is rolled up (it becomes the only raw row in old time
  ranges).

## Implementation Plan

1. **Aggregation job** (`server/internal/jobs/jobtypes/job_aggregation.go`,
   UID-collection step ~183-193): change the UID-collection loop to skip rows
   where `models.ResultStatus(*result.Status).IsLifecycleMarker()` is true,
   reusing the same helper already used in `processRawResult` (~line 726) to
   exclude lifecycle rows from stats. Preallocate the slice with capacity
   `len(resultsResp.Results)` per repo lint conventions but build it via
   `append` since the final length is now variable. `DeleteResults` already
   no-ops on an empty UID slice (checked in both `postgres.go` and
   `sqlite.go`), so a window made entirely of lifecycle markers is safe.
2. **Verify `GetResult` fallback removal**: no code change needed —
   `results/service.go`'s `GetResult` (~442-454) already resolves any UID it
   finds via `s.db.GetResult` directly, with no `Fallback` field set, before
   ever reaching the UUIDv7 covering-aggregation fallback path (~456-498).
   Once the lifecycle row is no longer deleted, this pre-existing direct path
   naturally takes over. Add a service-level test to lock this in.
3. **Open questions resolution**: keep both `created` and `running` rows
   symmetrically (matches the existing skip-from-stats predicate, simplest,
   matches the spec's stated default). No backfill for already-deleted rows.
   Neighbor navigation is unaffected by this change: `GetResultNeighbors` is
   called with `resp.PeriodType` from the resolved row, i.e. `raw`, and it
   already tolerates sparse raw series — this is out of scope for this fix
   beyond confirming no regression via the new aggregation-job test.
4. **Tests**:
   - Aggregation-job test in
     `server/internal/jobs/jobtypes/job_aggregation_test.go`: seed a raw
     window with a `created` row, a `running` row, and one or more measurable
     rows (e.g. `up`/`down`); run the aggregation job; assert the measurable
     rows are deleted and rolled into the new aggregated row, and assert the
     `created`/`running` rows still exist in the DB (e.g. via `GetResult` or
     a direct list query) after the job runs.
   - Results-service test in
     `server/internal/handlers/results/service_test.go`: create a `created`
     lifecycle row (not deleted), call `GetResult` on its UID, assert the
     response's `Fallback` field is nil and the status/period type match the
     raw row.
5. Run `make fmt`, then `make build-backend lint-back test`, fixing any
   failures, with explicit-path commits per fix.
