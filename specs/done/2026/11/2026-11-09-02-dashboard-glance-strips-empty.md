---
model: sonnet
effort: medium
---

# Dashboard "Checks at a glance" 24h strips are always grey

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/58

## Source

In-app report on the org dashboard (v0.44.3): every cell of every 24h uptime strip
in the "Checks at a glance" card is grey (no data), even for checks that have been
up all day. The reporter wants the strips filled, and filled by a second request so
the initial page load is not delayed.

The second half is already true: the strips ride their own query
(`hourlyResultsQuery`), which is not part of `isInitialLoading`. The bug is that this
query returns nothing.

## Problem

`web/dash0/src/components/dashboard/dashboard-page.tsx:241` fetches
`/results?periodType=hour&periodStartAfter=<now-24h>` and
`groupResultsByCheck` (`:135`) slots those rows into 24 hourly cells. But an `hour`
row only exists once the raw rows it summarizes age past `retention_raw` (default
24 h, `web/docs/docs/configuration/data-retention.md:50`), and the rollup deletes the
raw rows it folded. So the trailing 24 hours live almost entirely in **raw** rows,
the `hour` query for that window is empty, and every cell renders `noData`. The
comment above the query (`:229`) already says it for the availability KPI, which was
moved to the server-computed `stats.availability24h` for that reason; the strips were
left on the broken query.

Pulling raw rows fleet-wide from the browser is not an option (`GLANCE_PAGE_LIMIT` is
100 checks, `:87`; one check every minute is 1 440 rows a day each).

The server already has the right engine: `uptimebar.BucketAvailabilityInRegions`
(`server/internal/uptimebar/bucketing.go:588`) reads raw + hour + day tiers, takes a
**list** of check UIDs, and is what the per-check
`GET /orgs/:org/checks/:check/availability/buckets` endpoint uses
(`server/internal/handlers/availability/buckets.go:239`). Only the per-check route is
exposed.

## Why a spec

- A new API endpoint (org-level bucketed availability) and its OpenAPI entry.

## Proposal

1. **Backend**: `GET /api/v1/orgs/:org/availability/buckets?from=&to=&bucket=3600&checks=<uid,uid,…>`
   (or `limit`/same ordering as the glance list if `checks` is omitted). Reuses
   `planBuckets` + `uptimebar.BucketAvailabilityInRegions` with the given UIDs in one
   call. Response: `{ data: { <checkUid>: { buckets: Bucket[], latestDurationMs? } }, bucketSeconds, windowStart, windowEnd }`,
   same `Bucket` DTO (`hasData`, `availabilityPct`, `status`, counts) as the per-check route.
   Cap the number of checks per request (100, matching the glance clamp) and reject
   past it. Org membership check like the other org routes; checks of other orgs are
   ignored.
2. **Latency**: the glance row shows the latest response time from the newest bucket.
   Include `avgDurationMs` per bucket (from `BucketStats.AvgDuration`) so the client
   keeps that column.
3. **Frontend**: replace `hourlyResultsQuery` with a hook on the new endpoint, keyed by
   the glance check UIDs, enabled once `checksQuery` has data (so it stays a second
   request, never on the critical path). Keep the strip skeleton/empty-track rendering
   while it loads. Drop `groupResultsByCheck` or reduce it to the DTO mapping.
4. **Docs**: OpenAPI + `wiki/api-specification/` entry.
5. **Tests**:
   - Go: an org with two checks that only have raw rows in the last 24 h returns
     non-empty buckets for both (fails on today's hour-only path); a check of another
     org in `checks=` is ignored; >100 UIDs → 400.
   - Playwright: on the test org, after a check has run, the dashboard glance row of
     that check has at least one non-`noData` cell (`data-testid` on the cells).

## Closing the issue

This spec closes fclairamb/solidping-business#58. The implementing PR body **must**
carry `Closes fclairamb/solidping-business#58`. When the spec is archived to
`specs/done/`, verify the issue is closed and, if the merge did not close it, close
it by hand:

    gh issue close 58 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
