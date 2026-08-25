---
model: sonnet
effort: high
---

# Ghost regions are invisible — one API call must surface every region reference nothing serves

## Problem

During the 2026-08-24 region-rename incident, answering the single most
important operational question — *"which region slugs are referenced somewhere
but served by nobody?"* — required hand-written SQL against three tables. The
API has no surface for it:

- `GET /api/v1/regions` returns the **declared** list (the `regions` system
  parameter, [server.go:1025](server/internal/app/server.go#L1025)) — it said
  `gravelines, paris, …` while 419 jobs sat under `default`/`eu-2`/`us-1`.
- `GET /orgs/:org/regions` is org-scoped and also declaration-based.
- Worker liveness (`workers.last_active_at`,
  [worker.go:19](server/internal/db/models/worker.go#L19)) is not exposed
  per-region anywhere.

Three distinct reference sources can each hold a slug independently, and each
was desynchronized tonight in its own way:

| Source | Tonight's state |
|---|---|
| `checks.regions` arrays | already renamed (`gravelines`) |
| `check_jobs.region` rows | stale (`default`, `eu-2`, `us-1`, plus a truncated `@s3ns-paris` from an earlier rename) |
| live workers' announced regions | new slugs only |

A **ghost region** is any slug that appears in checks or jobs but has no live
worker whose announced region claims it under the scheduler's own matching rule
— which is a *prefix* match, `worker_region LIKE job_region || '%'`
([service.go:372](server/internal/checkworker/checkjobsvc/service.go#L372)).
The detection must reuse that exact predicate: `us` would be servable by a
`us-1` worker even though no worker is literally named `us`, and a naive
equality check would report false ghosts.

## Proposal

One super-admin, read-only endpoint on the `/system` actions group
([server.go:1463](server/internal/app/server.go#L1463)):

```
GET /api/v1/system/regions/health
```

Response — every region slug seen anywhere, one row each, so the caller gets
ghosts *and* the healthy baseline in one call:

```json
{
  "regions": [
    {
      "slug": "gravelines",
      "declared": true,
      "liveWorkers": 1,
      "lastWorkerSeenAt": "2026-08-24T03:10:20Z",
      "checksReferencing": 370,
      "jobs": 312,
      "jobsOverdue": 0,
      "oldestOverdueAt": null,
      "ghost": false
    },
    {
      "slug": "default",
      "declared": false,
      "liveWorkers": 0,
      "lastWorkerSeenAt": "2026-08-24T00:13:01Z",
      "checksReferencing": 0,
      "jobs": 59,
      "jobsOverdue": 51,
      "oldestOverdueAt": "2026-08-24T00:13:25Z",
      "ghost": true
    }
  ],
  "ghostCount": 4,
  "generatedAt": "..."
}
```

Definitions (each independently testable):

1. **Universe of slugs** = union of: declared regions (`regions.ParamRegions`),
   distinct `checks.regions` elements, distinct `check_jobs.region`, and
   distinct regions of non-deleted workers. `region IS NULL` jobs (any-region)
   are excluded — they are claimable by every cloud worker by construction
   ([service.go:361](server/internal/checkworker/checkjobsvc/service.go#L361)).
2. **liveWorkers** = non-deleted workers whose `last_active_at` is within the
   liveness window (reuse the constant the workers listing/agent GC already
   uses — do not invent a second threshold) and whose announced region matches
   the slug under the claim's prefix rule.
3. **ghost** = `(jobs > 0 OR checksReferencing > 0) AND liveWorkers == 0`.
   A declared region with zero live workers and zero references is *dark but
   unused* — reported (`liveWorkers: 0`) but not a ghost; the alarm condition
   is work assigned to nobody.
4. **lastWorkerSeenAt** = max `last_active_at` across *all* matching workers
   including soft-deleted ones — it dates when the region went dark, which is
   the first thing an operator wants during triage.
5. Private (`@…`) regions are included: tonight's data had a stranded
   `@s3ns-paris` (truncation of `@s3ns-paris-prod`/`-nonprod`) that this
   endpoint must catch. For private regions "workers" are the org agents
   serving that scope.

Performance: one request must stay cheap on the production dataset
(~600 checks / ~600 jobs / dozens of workers) — a handful of grouped queries,
no per-check loop. No pagination; the region cardinality is intrinsically
small.

This endpoint is the read-side companion of the migration API
(spec `2026-08-24-08`): its output tells the operator exactly which
`from` slugs to migrate, and the hourly watchdog (spec `2026-08-24-10`)
consumes the same service function — implement the computation as a service
method the handler and the job both call, not as handler-inline SQL.

### Tests

- Fixture with: healthy declared region, declared-but-dark unused region,
  ghost with overdue jobs, undeclared slug referenced only by `checks.regions`,
  prefix-servable slug (`us` job vs `us-1` worker → **not** a ghost), private
  ghost (`@x` with no agent), and NULL-region jobs (never counted).
- Liveness boundary: a worker exactly at the window edge.
- Auth: 403 for org admin, 200 for super-admin.

Out of scope: any mutation (migration is spec `2026-08-24-08`), alerting on
the result (spec `2026-08-24-10`), and dashboard UI — though the response is
deliberately shaped so a future `/system` page can render it as-is.

## Implementation Plan

Builds directly on spec `2026-08-24-08` (region migration), which just landed
on this branch: same `checks` package, same `systemActions` route group, same
`regions.WorkerLivenessWindow` constant, same prefix-match convention.

### Architecture decision: raw bounded scans + Go aggregation, not per-slug DB calls

`ListChecksReferencingRegion` / `ListCheckJobsByRegion` (added by 08) each take
**one** region string and are the right shape for the migration report (which
only ever inspects `from`/`to`). They are the wrong shape here: computing a
row **per slug in the whole universe** by calling them once per slug would be
one query per slug, not "a handful of grouped queries".

Instead follow the precedent already in this codebase for exactly this shape
of report — `system.Service.LaneLoad`
([service.go:612](server/internal/handlers/system/service.go#L612)): it scans
`workers` and a narrow `check_jobs` projection with `s.db.DB()` (the raw
`*bun.DB` escape hatch on `db.Service`, no new interface method), and
aggregates in Go, including the exact region prefix match
(`strings.HasPrefix(workerRegion, *job.Region)`) this spec must reuse. Doing
the same avoids new `db.Service` interface methods entirely (so no new
`mockDBService` stubs), avoids duplicating dialect-specific SQL for something
that doesn't need it (`checks.regions` and `check_jobs.region` decode through
the existing `models.Check`/`models.CheckJob` bun tags regardless of dialect —
only the *query shape*, not the Go type, ever needed a Postgres/SQLite split,
and a narrow column projection doesn't touch the array/JSON column at all
differently from the full-model reads those tags already handle), and keeps
the whole computation to 3 bounded scans (checks, check_jobs, workers) +
1 param read (declared regions), independent of region cardinality.

### Steps

1. **`server/internal/handlers/checks/region_health.go`** (new file):
   - `RegionHealthRow` / `RegionHealthReport` types (camelCase JSON per the
     spec's example shape).
   - `Service.RegionHealth(ctx) (*RegionHealthReport, error)`:
     a. `s.regions.GetGlobalRegions` → declared slug set.
     b. One scan of `checks` (`uid` unneeded — just `regions`, non-deleted
        only) → per-slug `checksReferencing` counts, de-duplicating a slug
        that (through API misuse) repeats within one check's array so it
        never double-counts.
     c. One scan of `check_jobs` (`region, scheduled_at`, `region IS NOT
        NULL` — NULL/any-region jobs excluded by construction) → per-slug
        `jobs`, `jobsOverdue` (`scheduledAt` before `now`), `oldestOverdueAt`.
     d. One scan of **all** `workers`, deliberately with no `deleted_at`
        filter (unlike `ListWorkers`/`ListLiveWorkers`, neither of which
        would surface a dark region's last heartbeat) → used twice per slug:
        `liveWorkers` (non-deleted, `last_active_at >= now -
        WorkerLivenessWindow`, prefix match) and `lastWorkerSeenAt` (max
        `last_active_at` over every matching worker, deleted included).
     e. Union the universe: declared ∪ checksReferencing keys ∪ jobStats keys
        ∪ non-deleted workers' regions (deleted-only workers do not extend
        the universe — matches the spec's source list exactly).
     f. Per slug: `ghost = (jobs>0 || checksReferencing>0) && liveWorkers==0`.
   - Liveness cutoff computed once (`time.Now().Add(-regions.WorkerLivenessWindow)`),
     boundary inclusive (`>=`), mirroring `ListLiveWorkers`/`knownRegionSlugs`.
2. **`server/internal/handlers/checks/region_health_handler.go`** (new file):
   `Handler.RegionHealth` → `h.svc.RegionHealth(ctx)` → `h.WriteJSON(200, report)`.
   No request body, no error branches beyond internal (read-only, no
   validation surface).
3. **`server/internal/app/server.go`**: register
   `systemActions.GET("/regions/health", checksHandler.RegionHealth)` next to
   the existing `POST /regions/migrate` registration — same group, so
   `RequireAuth` + `RequireSuperAdmin` apply unchanged.
4. **`server/internal/app/openapi/openapi.yaml`**: document
   `GET /api/v1/system/regions/health`, `RegionHealthRow` and
   `RegionHealthReport` schemas, mirroring the migration endpoint's doc style.
5. **Tests** — `server/internal/handlers/checks/region_health_test.go`
   (SQLite, following `region_migration_test.go`'s fixture-building helpers):
   - One fixture organization/service covering every case in the spec's test
     list in a single `RegionHealth` call: healthy declared region (live
     worker + checks + jobs), declared-but-dark unused region (`ghost:
     false`), ghost with overdue jobs, undeclared slug referenced only via
     `checks.regions`, prefix-servable slug (`us` job vs `us-1` worker — not
     a ghost), private ghost (`@x`, no agent/worker), NULL-region job
     (create a check with no declared regions, confirm its any-region job
     never appears in any row's counts).
   - Liveness boundary sub-test: a worker whose `last_active_at` is set to
     exactly `now - WorkerLivenessWindow` counts as live (`liveWorkers: 1`,
     not a ghost) — set via direct DB update since `UpdateWorkerHeartbeat`
     always stamps "now".
   - `lastWorkerSeenAt` sub-test: a soft-deleted worker's timestamp still
     surfaces (create, heartbeat, then `DeleteWorker`).
   - Handler auth-matrix test (mirrors
     `agents/handler_test.go:TestListAllAgentsHandlerAuthMatrix`): 403 org
     admin / regular user, 200 super-admin.
6. `make fmt`, targeted package build + `golangci-lint run
   ./server/internal/handlers/checks/... ./server/internal/app/...`, then the
   full gate once.
