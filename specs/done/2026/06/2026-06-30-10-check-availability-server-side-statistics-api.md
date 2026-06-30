# Server-side availability-statistics API — real per-period measurements (replace the client-side estimate)

## Context

On the check detail page (`/dash0/orgs/$org/checks/<uid>`), the **Availability**
card shows one row per period — *Today*, *Last 7 days*, *Last 30 days*,
*Last 365 days* — with columns *Availability*, *Downtime*, *Incidents*,
*Longest incident*, *Avg. incident*.

The Availability and Downtime columns are **not measured — they are estimated on
the client**, and the estimate is misleading:

1. **Downtime is a formula, not a measurement.**
   `web/dash0/src/components/checks/availability-table.tsx:215` computes
   ```ts
   const downtime = (1 - availability / 100) * period.durationMs;
   ```
   i.e. `downtime = (1 − availability) × calendar-period-length`. So the three
   long rows in a real screenshot read **1h 29m / 6h 23m / 3d 5h** — which is just
   the *same* 0.89% applied to 7 d, 30 d, and 365 d. Nothing about actual outages
   is involved. For a check younger than a year, "3 d 5 h of downtime over the last
   365 days" is fiction: the check did not exist for most of that window.

2. **Availability repeats across periods** because the same finite set of result
   rows lands in every window (the check simply has no year of distinct history),
   and there is a silent `size: 1000` row cap that truncates older data anyway. So
   7 d, 30 d and 365 d all show an identical `99.11%`.

3. **The columns contradict each other.** The screenshot shows **0 incidents** but
   **3 d 5 h of downtime**. Under any honest definition those cannot both be true —
   downtime with no incident is downtime that never happened. The contradiction is
   a direct symptom of the formula in (1).

The fix the user asked for is correct: compute **actual** statistics **server-side**
and expose them through a dedicated endpoint, e.g.
`GET …/availability?periods=24h,7d,30d,365d`.

### Why this belongs on the server (and why it is cheap)

The hard part is already built. `server/internal/uptimebar/bucketing.go` is the
**single shared availability engine** already used by both the **badge** uptime
bar (`handlers/badges/service.go`) and the **public status page**
(`handlers/statuspages/service.go`). It runs **one** `{raw, hour, day}` union query
over a window and accumulates per-bucket success/total with the canonical rules
(`models.RawAvailability` / `ResultStatus.CountsAsUp` / `IsLifecycleMarker`;
`uptimebar.BucketStats.AvailabilityPct()` returns `ok=false` for *no data*, so
"100%" and "no data" are distinguishable).

Crucially, the retention tiers line up so this engine already covers **all four**
requested periods with no new aggregation:

| Tier | Retention (default) | Covers |
|---|---|---|
| `raw` | `retention_raw = 24` h | last ~24 h |
| `hour` | `retention_hour = 30` d | ~24 h → 30 d |
| `day` | `retention_day = 12` mo | ~30 d → 365 d |

(`server/internal/config/config.go:313-315` / `:563-565`.) Because aggregation
deletes source rows after each rollup, the tiers are **disjoint** — unioning them
never double-counts. So `{raw, hour, day}` covers `0 → 365 d` exactly.

The current table is the **last surface still computing availability in the
browser**. The same conceptual hole has already been patched, surface by surface,
in `specs/done/2026/06/2026-06-30-02` (status-page lifecycle exclusion), `-03`
(24 h history period), `-05` (status-page/badge data-source parity), and the
in-flight client band-aid `specs/todos/2026-06-30-08-…`. A server endpoint backed
by the shared engine ends that whack-a-mole: one number, computed one way, reused
everywhere.

## The endpoint

```
GET /api/v1/orgs/{org}/checks/{uid}/availability?periods=24h,7d,30d,365d&tz=UTC
```

- **`{uid}`** accepts a check UID **or** slug, like the other `…/checks/{uid}/…`
  routes.
- **Auth / scope:** org-scoped, `RequireAuth` — register beside the existing
  `/orgs/:org/checks/:check/results` group in `server/internal/app/server.go`
  (results group at `:716-724`).
- **`periods`** (required): comma-separated tokens. Each is either a **trailing
  duration** — `24h`, `7d`, `30d`, `90d`, `365d`, `1y` (suffixes `h`/`d`/`w`/`y`;
  `time.ParseDuration` does **not** handle `d`/`y`, so add a small token parser) —
  or a **calendar token** — `today`, `mtd`, `ytd` — resolved against `tz`.
  - Validate: reject unknown tokens with `VALIDATION_ERROR`; cap count (≤ 12) and
    max lookback (≤ day-tier retention, ~12 mo; anything longer would need the
    `month` tier added to the union).
- **`tz`** (optional, default `UTC`): IANA zone used to resolve the calendar
  tokens (`today` = since local midnight) and only those. Trailing durations are
  tz-independent.

### Response (wrap the list in `data`, per repo convention)

```json
{
  "data": [
    {
      "period": "7d",
      "windowStart": "2026-06-23T08:00:00Z",
      "windowEnd": "2026-06-30T08:00:00Z",
      "monitoredSeconds": 604800,
      "partial": false,
      "hasData": true,
      "totalChecks": 10080,
      "successfulChecks": 9990,
      "availabilityPct": 99.107,
      "downtimeSeconds": 5400,
      "incidents": {
        "count": 2,
        "longestSeconds": 3600,
        "averageSeconds": 2700,
        "totalDowntimeSeconds": 5400
      }
    }
  ]
}
```

- `availabilityPct` is **`null`** and `hasData=false` when `totalChecks == 0`
  (no data ≠ 100%). The UI renders `-`, not `100%`.
- `monitoredSeconds = min(windowLength, now − check.createdAt)`; `partial=true`
  when the check is younger than the window (lets the UI caveat e.g. the 365 d row
  of a week-old check as "7 d monitored").
- camelCase JSON; `*Seconds` (not `*Ms`) because windows span days; `omitempty`
  for the nullable incident fields.

## Semantics — the one real decision: what "downtime" means

The data supports **two** coherent definitions, and they legitimately differ.
This spec returns **both** so the product/UI can choose the display without a
backend change, and recommends which to lead with.

**A. Probe-ratio (recommended as the primary `availabilityPct` + `downtimeSeconds`).**
Availability is the share of probes that succeeded; downtime is the complementary
measured time:
- `availabilityPct = successfulChecks / totalChecks × 100` over the window
  (real per-bucket counts from the shared engine).
- `downtimeSeconds = (1 − availabilityPct/100) × monitoredSeconds`.
  *More precise variant (recommended in code):* sum per bucket
  `Σ (failedFraction_bucket × bucketDuration)`, which attributes downtime only to
  buckets that actually have data and so is robust to coverage gaps. The capped
  formula above is the intuition / fallback.

This is the definition the **rest of SolidPing already uses** — the stored
`results.availability_pct`, the status page, and the badge are all probe-ratio.
Leading the check-detail table with the same number keeps the surfaces
**consistent with each other**, which matters more than internal column symmetry.

**B. Incident wall-clock (returned in the `incidents` block).** Confirmed,
sustained outages only:
- `incidents.count`, `longestSeconds`, `averageSeconds` from incidents whose
  `started_at` falls in the window (`handlers/incidents` / `models.Incident`,
  which persists exact `started_at` / `resolved_at` / `state`).
- `incidents.totalDowntimeSeconds = Σ (min(resolvedAt, windowEnd) −
  max(startedAt, windowStart))`, clamping each incident to the window; an
  unresolved incident runs to `now`.

**Why return both, and why probe-ratio leads.** `downtimeSeconds` (probe-time)
will generally be **≥** `incidents.totalDowntimeSeconds` (wall-clock), because a
check can fail isolated probes that never reach the confirmation threshold to open
an incident. That is **not** a contradiction — it is two sensitivities:
"time the check was failing probes" vs "confirmed outages we paged on". The
screenshot's "0 incidents / 3 d downtime" was a *bug* (calendar fabrication);
with real measurement the probe-downtime becomes small and truthful, and any
residual gap to the incident count is explainable. The UI should label them so
the distinction is legible (e.g. *Downtime* = failing-probe time; the *Incidents*
columns = confirmed outages — a tooltip suffices).

> Alternative not recommended as primary: make `availabilityPct` itself
> incident-based (`1 − incidentDowntime/monitored`). It would make the table's own
> columns tie out, but **diverge** the check-detail number from the status page,
> badge, and stored `availability_pct` — a worse, more visible inconsistency.
> Returning both numbers keeps that option open as a pure display choice later.

### Exclusions (honesty refinements — see Out of scope for phasing)

- **Maintenance windows:** planned downtime should not silently count against
  availability. At minimum report it separately (e.g. add
  `excludedMaintenanceSeconds`); ideally subtract it from both numerator basis and
  `monitoredSeconds`. SolidPing already has maintenance windows
  (`specs/done/2026/06/2026-06-28-05-maintenance-windows-…`).
- **Disabled / not-yet-created time:** time before `check.createdAt` or while the
  check was disabled is *not* downtime. The `monitoredSeconds` cap handles
  not-yet-created; disabled-interval exclusion is a follow-up.

## Implementation

1. **Shared engine — add a window aggregate.** `uptimebar.BucketAvailability`
   is tick-strip oriented (per-bucket map, `Limit = n × checks`). Add a sibling
   in `server/internal/uptimebar/` that runs **one** `{raw, hour, day}` query over
   `[windowStart, now)` and accumulates **all** rows into a single `BucketStats`
   per check, reusing `accumulateRaw` / `accumulateAgg` so the counting rules stay
   canonical:
   ```go
   func WindowAvailability(ctx, db ResultsLister, orgUID string,
       checkUIDs []string, start, end time.Time) (map[string]BucketStats, error)
   ```
   Use a generous/unbounded limit (the window itself bounds the rows: worst case a
   1-min check over 365 d ≈ raw≤1 440 + hourly≈720 + daily≈335 ≈ 2.5 k rows —
   fine server-side, and *not* shipped to the browser). For the smallest window the
   query touches almost only raw, so it stays cheap. Existing indexes
   (`results_raw_idx`, `results_aggregated_idx`) already cover it.

2. **New handler package** `server/internal/handlers/availability/`
   (`handler.go` + `service.go`, mirroring `handlers/results`):
   - `handler.go`: embed `base.HandlerBase`; parse `periods`/`tz`; per period
     resolve `[windowStart, now)`, call `uptimebar.WindowAvailability` for the one
     check, fetch incidents for the window, assemble the DTO; `h.WriteJSON`.
     Translate not-found → `CHECK_NOT_FOUND` / `ORGANIZATION_NOT_FOUND`, bad tokens
     → `VALIDATION_ERROR`.
   - `service.go`: business logic + DTOs (`AvailabilityPeriod`,
     `PeriodIncidents`, `ListAvailabilityResponse{ Data []AvailabilityPeriod }`),
     resolve check uid/slug, compute `monitoredSeconds` from `check.createdAt`, do
     the incident clamping. No DB access in the handler.
3. **Route:** in `server/internal/app/server.go` near `:716`, add
   `orgChecksAvail := api.NewGroup("/orgs/:org/checks/:check/availability").Use(authMiddleware.RequireAuth)` →
   `orgChecksAvail.GET("", availabilityHandler.GetAvailability)`. Inject the service
   the same way `resultsService`/`resultsHandler` are wired.
4. **OpenAPI:** document the path + `periods`/`tz` params and the response schema
   in `server/internal/app/openapi/openapi.yaml` (mirror the `listOrgResults`
   entry at `:728` and add `CheckAvailabilityResponse` / `AvailabilityPeriod`
   schemas under `components/schemas`). The `/openapi` explorer and the embedded
   API reference pick it up at build.
5. **DB/ORM:** use Bun via the existing `db.Service` (`ListResults`,
   `ListIncidents`, `GetCheckBySlugOrUID`) so it runs unchanged on **both**
   Postgres and SQLite — no raw SQL (keep `/sync-pg-to-sqlite` a no-op here).

### Frontend (consume it; delete the client math)

In `web/dash0/src/components/checks/availability-table.tsx`:
- Replace the two `useAllResults` queries + the bucketing `useMemo` +
  `computeIncidentStats` with **one** call to the new endpoint
  (add `useCheckAvailability` to `web/dash0/src/api/hooks`).
- Render straight from the response; show `-` when `hasData=false`, and surface
  `partial`/`monitoredSeconds` (e.g. a "N d monitored" hint on partial rows).
- **Delete** `web/dash0/src/lib/availability.ts` (`computeAvailability`) and its
  test once nothing else imports them, and drop the `(1 − availability) × duration`
  downtime formula entirely.

## Relationship to other specs

- **Supersedes `specs/cancelled/2026-06-30-08-…` (the client-side band-aid),
  cancelled 2026-06-30.** That spec made the browser count raw rows so young checks
  aren't blank; this endpoint removes the client-side computation altogether and
  fixes downtime too. Its implementation is **still uncommitted in the working
  tree** — `web/dash0/src/lib/availability.ts`, `…/availability.test.ts`, and the
  edits to `web/dash0/src/components/checks/availability-table.tsx` — and is removed
  as part of this spec's frontend step (the table is rewritten to consume the
  endpoint; `availability.ts` and its test are deleted).
- **Should become the source for the org dashboard "24 h availability" KPI and the
  glance uptime strips** (`web/dash0/src/components/dashboard/dashboard-page.tsx`),
  which have the same root cause. Out of scope to migrate here, but the endpoint is
  designed to serve them (it already supports the multi-check batched query under
  the hood).

## Scope

- **In scope:** the new server endpoint; the `uptimebar.WindowAvailability`
  helper; the check-detail Availability table consuming it; removal of the
  client-side availability/downtime math.
- Lead `availabilityPct` + `downtimeSeconds` with probe-ratio; return the
  incident wall-clock block alongside.

## Out of scope (follow-ups)

- Org-wide / multi-check variant (`/orgs/{org}/availability?checkUid=a,b`) — the
  engine batches; trivial to add later.
- Migrating the dashboard KPI and glance strips to the endpoint.
- Maintenance-window and disabled-interval exclusion beyond `monitoredSeconds`
  (report `excludedMaintenanceSeconds` first; full subtraction later).
- Per-region breakdown (`groupBy=region`) — results are per-region; not v1.
- SLA target / error-budget fields.
- Response caching/precompute (the query is already index-served; revisit only if
  the dashboard fan-out makes it hot).

## Testing

- **`uptimebar.WindowAvailability` (unit, table-driven, `testify/require`,
  `t.Parallel()`):** raw-only window; aggregated-only window; mixed disjoint
  window (no double-count); `down`/`timeout`/`error` count as failures while
  `created`/`running` are skipped; `warning` counts as up; empty window →
  `Total==0` / `ok=false`.
- **Service (unit):** period-token parsing (`24h`/`7d`/`365d`/`1y`/`today`/`mtd`,
  reject `5x`); `monitoredSeconds` cap + `partial` for a check younger than the
  window; incident clamping at both window edges and for an unresolved incident;
  `downtimeSeconds` equals `(1 − avail) × monitoredSeconds`.
- **Integration (`server/test/integration/`, testcontainers, SQLite default +
  Postgres):** seed a check with a known mix of up/down raw rows and rolled-up
  hour/day buckets plus a couple of incidents; assert per-period
  `availabilityPct`, `downtimeSeconds`, and the incident block; assert `401`
  unauth and `404` for a missing check; assert SQLite/Postgres parity.
- **Frontend:** `vitest` for the new hook's mapping; `bun run lint` + `bun run
  build` in `web/dash0`.
- **Manual:** on a check < 24 h old, all four rows show real availability and a
  real, small downtime (not a calendar fraction); the 365 d row of a young check is
  flagged partial; an older check's *Today* row reflects today's probes.
```

## Implementation Plan

### Backend

1. **`uptimebar.WindowAvailability`** — new sibling of `BucketAvailability` in
   `server/internal/uptimebar/window.go`:
   `WindowAvailability(ctx, db ResultsLister, orgUID string, checkUIDs []string, start, end time.Time) (map[string]BucketStats, error)`.
   One `{raw, hour, day}` union query over `[start, end)` (`PeriodStartAfter=start`,
   `PeriodEndBefore=end`), generous limit; accumulate every row into a single
   `BucketStats` per check via the existing `accumulateRaw` / `accumulateAgg`
   (so the canonical counting rules stay shared). Empty/`n<=0` → empty map.
   - Unit test `window_test.go` (table-driven, `t.Parallel()`): raw-only;
     aggregated-only; mixed disjoint (no double-count); down/timeout/error = fail;
     created/running skipped; warning = up; empty window → `Total==0`/`ok=false`.

2. **New handler package `server/internal/handlers/availability/`** (mirror `results`):
   - `service.go`: DTOs `AvailabilityPeriod`, `PeriodIncidents`,
     `ListAvailabilityResponse{ Data []AvailabilityPeriod }`; sentinel errors
     (`ErrOrganizationNotFound`, `ErrCheckNotFound`, `ErrInvalidPeriod`); period-token
     parser (trailing durations `24h`/`7d`/`30d`/`90d`/`365d`/`1y` with suffixes
     `h`/`d`/`w`/`y` via a small parser since `time.ParseDuration` rejects `d`/`y`;
     calendar tokens `today`/`mtd`/`ytd` resolved against `tz`); validation
     (unknown token → `ErrInvalidPeriod`; count ≤ 12; max lookback ≤ day-tier
     retention); resolve org + check (uid or slug via `GetCheckByUidOrSlug`); per
     period resolve `[windowStart, now)`, call `uptimebar.WindowAvailability`,
     compute `monitoredSeconds = min(windowLength, now − check.CreatedAt)` +
     `partial`; probe-ratio `availabilityPct` (`null` + `hasData=false` when
     `totalChecks==0`) and `downtimeSeconds` (per-bucket `Σ failedFraction × bucketDuration`
     — here the whole window is one bucket, so `(1 − avail) × monitoredSeconds`);
     incident block from `ListIncidents` filtered to `started_at ∈ window`, clamped
     to the window (unresolved → now). No DB access in handler.
   - `handler.go`: embed `base.HandlerBase`; `GetAvailability` parses `periods`/`tz`,
     calls the service, `WriteJSON`; map `ErrOrganizationNotFound` →
     `ORGANIZATION_NOT_FOUND`/404, `ErrCheckNotFound` → `CHECK_NOT_FOUND`/404,
     `ErrInvalidPeriod` → `VALIDATION_ERROR`/400.
   - Unit tests `service_test.go`: token parsing (`24h`/`7d`/`365d`/`1y`/`today`/`mtd`,
     reject `5x`); `monitoredSeconds` cap + `partial` for a young check; incident
     clamping at both edges + unresolved; `downtimeSeconds == (1−avail)×monitoredSeconds`.

3. **Route** in `server/internal/app/server.go` near the results group (~:716):
   `orgChecksAvail := api.NewGroup("/orgs/:org/checks/:check/availability").Use(authMiddleware.RequireAuth)`
   → `orgChecksAvail.GET("", availabilityHandler.GetAvailability)`; wire
   `availabilityService`/`availabilityHandler` like `resultsService`/`resultsHandler`.

4. **OpenAPI** in `server/internal/app/openapi/openapi.yaml`: add path
   `/api/v1/orgs/{org}/checks/{check}/availability` (GET, `periods` required +
   `tz` optional params, 200/401/404) mirroring `listOrgResults`; add
   `CheckAvailabilityResponse` / `AvailabilityPeriod` / `PeriodIncidents` schemas.

5. **Integration test** `server/test/integration/availability_test.go` (testcontainers,
   SQLite default + Postgres): seed a check with known up/down raw rows + rolled-up
   hour/day buckets + a couple incidents; assert per-period `availabilityPct`,
   `downtimeSeconds`, incident block; assert 401 unauth, 404 missing check,
   SQLite/Postgres parity.

### Frontend (`web/dash0`)

6. Add `useCheckAvailability(org, checkUid, periods, opts)` hook to the api hooks
   module; export response types `CheckAvailabilityPeriod` / `CheckAvailabilityResponse`.

7. **Rewrite** `web/dash0/src/components/checks/availability-table.tsx`: drop the two
   `useAllResults` queries, the bucketing `useMemo`, `computeIncidentStats`, and the
   `(1 − availability) × duration` formula; make ONE `useCheckAvailability` call with
   `periods=today,7d,30d,365d`; render straight from the response — `-` on
   `hasData=false`, and a "N d monitored" hint on `partial` rows.

8. **Delete** `web/dash0/src/lib/availability.ts` and
   `web/dash0/src/lib/availability.test.ts` once nothing imports them.

9. Add a `vitest` test for the hook's response→row mapping
   (`availability-table` mapping or a small pure mapper).

### QA
`make build-backend lint-back test`; `make build-dash0` + `cd web/dash0 && bun run lint`
+ `bun run test`. Gate: no NEW dash lint errors in touched files. Final commit
`chore: all checks passing for server-side availability-statistics API`.
