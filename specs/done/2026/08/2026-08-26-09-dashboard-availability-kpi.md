---
model: sonnet
effort: high
---

# The dashboard "24h Availability" KPI is fabricated — it has never displayed real data

## Problem

The org dashboard's "24h Availability" tile (and the all-green banner's
availability figure) is hardcoded theater, for every org, all the time:

1. **The data query is structurally dead.** The KPI is computed by
   `weightedAvailability(results)`
   ([dashboard-page.tsx:218](../../web/dash0/src/components/dashboard/dashboard-page.tsx),
   used at line 338) over results fetched with `periodType: "day"` and
   `periodStartAfter: now-24h` (line 276). Day-aggregate rows have
   `period_start` at a UTC midnight, so the only day bucket that can start
   within the trailing 24 h is *today's* — and today's day row never exists,
   because the aggregation job only compacts hour→day for days older than the
   hour-retention window
   ([job_aggregation.go:487-500](../../server/internal/jobs/jobtypes/job_aggregation.go),
   `retention_hour` default 7 days,
   [config.go:777-778](../../server/internal/config/config.go)). The query
   therefore returns zero rows for every org, always.

2. **Null is rendered as a perfect score.** With zero rows,
   `weightedAvailability` returns `null`, and the tile renders the literal
   fallback `availabilityPct === null ? "100%" : …`
   (dashboard-page.tsx:459-462). The `OverallStatusBanner` does the same with
   `"100"` (lines ~704-707).

3. **The status chrome is unconditional.** The tile's "Operational" badge and
   emerald trend icon (lines ~458-470) and the banner's "24h SLA Operational"
   pill are hardcoded — they would read "Operational" with the entire fleet
   down.

Verified on production (`solidping_prod`): the `public` org has ~18.5k `raw`
rows and 307 `hour` rows but **zero `day` rows**, and zero rows match the
tile's query — the "100% / Operational" it displays corresponds to nothing.

A subtlety that rules out the "obvious" client-side fix: the page already
fetches hourly results for the uptime strips (`hourlyResultsQuery`, line
~284), but hour rows only exist once raw rows age past `retention_raw`
(24 h default), so the newest hour row typically lags ~24 h behind. The
trailing day lives almost entirely in **raw** rows. Recomputing the KPI from
the hourly query would still show stale/empty data, and pulling raw rows
fleet-wide client-side is exactly what the aggregates exist to avoid. The
number must come from the server.

## Proposal

### Backend — add `availability24h` to the org stats aggregate

Extend `GET /api/v1/orgs/{org}/checks/stats`
([handler.go:129-151](../../server/internal/handlers/checks/handler.go),
service + 1-minute TTL cache in
[stats.go](../../server/internal/handlers/checks/stats.go) /
[service.go:386-388](../../server/internal/handlers/checks/service.go)) with:

```jsonc
{
  // existing counters…
  "availability24h": 99.97   // number | null — null when no measurable data
}
```

Computed in one pass over `results` for the org in the `[now-24h, now]`
window, combining both period types that hold that window's data:

- **`hour` rows** (`period_start >= now-24h`): contribute
  Σ`successful_checks` / Σ`total_checks` (the maintenance-split columns stay
  out of it — availability remains `successful/total`, per
  [result.go:224](../../server/internal/db/models/result.go)).
- **`raw` rows** (`period_start >= now-24h`): contribute counts by status
  using the existing single-source-of-truth semantics — denominator excludes
  `ExcludedFromAvailability` statuses (lifecycle markers + abandoned,
  [result.go:148](../../server/internal/db/models/result.go)), numerator is
  `CountsAsUp` (up + warning, result.go:137). Mirror `RawAvailability`
  (result.go:155) in SQL rather than loading rows into Go.

`availability24h = 100 * (Σ success) / (Σ total)`, `null` when the combined
denominator is 0 (empty or brand-new org). Double-counting is not possible:
a raw row that has been rolled up is deleted by the compaction, so raw and
hour rows in the window are disjoint by construction.

The SQL must work on both dialects (PostgreSQL + SQLite — keep it to portable
aggregates; see `sync-pg-to-sqlite` expectations), ride the existing stats
cache/TTL, and be added to the OpenAPI spec
(`server/internal/app/openapi/openapi.yaml`) plus the generated client and
the frontend `CheckStats` type (`web/dash0/src/api/hooks.ts`).

### Frontend — honest tile and banner

In [dashboard-page.tsx](../../web/dash0/src/components/dashboard/dashboard-page.tsx):

- The tile's value comes from `stats.availability24h`. When `null`, render
  "—" with a "No data yet" sub-label — **never a fabricated 100%**.
- Derive the badge from data instead of hardcoding it, e.g.:
  `availability24h == null` → neutral "No data"; `>= 99.9` → emerald
  "Operational"; `>= 99` → amber "Degraded"; else red "Down". Tint the icon
  consistently. (Thresholds are a starting point — keep them as named
  constants.)
- Apply the same treatment to `OverallStatusBanner`'s availability figure and
  its "24h SLA Operational" pill (suppress or neutralize the pill when there
  is no data; the pill only renders on the all-green branch, so a red state
  needs no new banner variant).
- Delete the now-dead `periodType: "day"` results query (line 276) and
  `weightedAvailability` once nothing consumes them. The hourly query stays —
  the glance-card uptime strips use it.
- New user-facing strings go through the locale files (all locales, so
  `bun run test:unit` stays green).

### Testing

- Backend: table-driven service tests over a seeded window — raw-only org
  (young org), hour+raw mix, statuses that must be excluded (created/running/
  abandoned) and warning-counts-as-up, multi-region rows, empty org → `null`.
  Prove the negative: an org whose only rows are excluded statuses returns
  `null`, not 100.
- Frontend: unit/E2E coverage that the tile shows "—" when
  `availability24h` is null and the badge tier matches the value; assert the
  hardcoded "100%" fallback is gone.

### Out of scope (follow-up candidates)

- **Coverage/skip honesty**: an executed-runs ratio still ignores skipped
  runs (no row ⇒ counts as nothing), so scheduler gaps silently inflate
  availability. Surfacing expected-vs-actual execution coverage is a separate
  spec.
- Making the availability window configurable (7d/30d variants).

## Implementation Plan

### Backend

1. `models.AvailabilityCounts{Success, Total int64}` + `Pct() (float64, bool)`
   in `server/internal/db/models/result.go`, next to `RawAvailability` —
   `ok=false` when `Total == 0` so "no data" can never collapse into a
   fabricated percentage.
2. `db.Service.GetOrgAvailability24h(ctx, orgUID string, since, now time.Time)
   (models.AvailabilityCounts, error)` added to the interface
   (`server/internal/db/service.go`) and implemented identically on both
   dialects (`db/postgres/postgres.go`, `db/sqlite/sqlite.go`): TWO tier-aligned
   SQL aggregates (mirroring `uptimebar/window.go`'s split, for the same
   partial-index reason) —
   - `hour`: `COALESCE(SUM(successful_checks),0)` / `COALESCE(SUM(total_checks),0)`.
   - `raw`: success = `COUNT` where `status IN (up, warning)`; total = `COUNT`
     where `status NOT IN (created, running, abandoned)` — mirrors
     `RawAvailability`'s predicate in SQL.
   - Both filtered to `period_start >= since AND period_start < now`; summed
     in Go. day/month tiers are not queried (they never carry data for a
     "today" window — see the doc comment).
   - `mockDBService` in `server/internal/notifications/slack_test.go` gets a
     matching stub (it implements `db.Service` for an unrelated test).
3. `checks.CheckStatsResponse.Availability24h *float64` (`server/internal/handlers/checks/stats.go`),
   populated in `GetCheckStats` from `GetOrgAvailability24h(now-24h, now)` via
   `Pct()`; `now` reuses the service's existing injectable `s.now` field
   (shared with `RegionHealth`, override via `SetRegionHealthNowForTest` in
   tests). Rides the existing 1-minute stats cache as-is.
4. OpenAPI: add `availability24h: number | null` to `CheckStats` in
   `server/internal/app/openapi/openapi.yaml`; regenerate
   `server/pkg/client/client_generated.go` via `go generate ./pkg/client/...`.
5. Tests: `server/internal/db/models/result_test.go` (`AvailabilityCounts.Pct`
   boundary), a new `server/internal/handlers/checks/availability24h_test.go`
   (SQLite) + `availability24h_postgres_test.go` (embedded-Postgres twin,
   shared case table) driving `svc.GetCheckStats` over seeded raw/hour rows:
   empty org, raw-only mix, hour+raw mix, excluded-statuses-only → null (the
   headline negative), warning-counts-as-up, multi-region, window-boundary
   exclusion, day-rows-ignored.

### Frontend (`web/dash0/src/components/dashboard/dashboard-page.tsx`)

6. `CheckStats.availability24h: number | null` in `web/dash0/src/api/hooks.ts`.
7. Delete the `periodType: "day"` `resultsQuery` and `weightedAvailability`;
   read `stats.availability24h` directly. Keep `hourlyResultsQuery` (glance
   uptime strips) untouched. Remove `resultsQuery` from
   `isInitialLoading`/`refreshAll`/`isRefetching`/`latestUpdate`.
2. Named threshold constants (`AVAILABILITY_OPERATIONAL_PCT = 99.9`,
   `AVAILABILITY_DEGRADED_PCT = 99`) driving a small `availabilityTier`
   pure function (null → `noData`, else operational/degraded/down) — unit
   tested directly. Tier maps to badge text (new locale keys) + badge/icon
   Tailwind classes matching the existing emerald/amber/destructive/muted
   conventions used by the other KPI tiles.
8. Tile: value `"—"` + "No data yet" sub when null, else `pct.toFixed(2)+"%"`;
   badge/icon driven by the tier, never hardcoded "Operational".
9. `OverallStatusBanner`: all-green branch reads `stats.availability24h`
   directly (no `"100"` fallback) and suppresses the "24h SLA Operational"
   pill when null (new `allGreenSubNoData` locale key for the sub-line); the
   hard-down/timeout-only branches are untouched (spec: "a red state needs no
   new banner variant").
10. New locale keys in all four `web/dash0/src/locales/{en,fr,de,es}/dashboard.json`.
11. `web/dash0/e2e/dashboard.spec.ts`: drop the dead `periodType=day` mock
    branch, add `availability24h` to `MockCheckStats`, and add dedicated tests
    for the "—"/No data state, the tier badges, and the suppressed banner
    pill — asserting the fabricated "100%"/"Operational" text is gone.
12. New `dashboard-page.test.ts` unit test for the exported `availabilityTier`
    boundary values.

### QA gate

`make build-backend lint-back test`; `make build-dash0` then
`cd web/dash0 && bun run lint`; `cd web/dash0 && bun run test:unit`. E2E file
run standalone if the local stack allows it.
