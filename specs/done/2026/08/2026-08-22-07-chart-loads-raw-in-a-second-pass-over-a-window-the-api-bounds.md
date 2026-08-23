---
model: opus
effort: high
---

# A 30-day chart downloads 24 h of 1-minute samples to draw 3 % of its width, and blocks the whole render on it

**Depends on `2026-08-22-04`**, which splits the chart fetch at the raw/rollup
boundary so the two tiers are already separate requests. This spec turns those
two requests into two **render passes** and narrows the raw one. It cannot land
first: without the split there is no raw request to narrow.

## Problem

Raw retention is 24 h by default, so the raw tier is not "the current open
bucket" the way
[`chartFetchParams`](web/dash0/src/components/checks/response-time-chart.tsx#L168)
describes it — it is a full day of samples, at the check's period, per region.
The chart requests the whole thing for every range that includes raw.

A 30-day view of a 1-minute check across 3 regions:

| tier | points | share of x-axis it can occupy |
|---|---|---|
| raw | 4 320 | **3 %** (24 h of 30 d) |
| hour | 504 | 23 % |
| day | 90 | 100 % |
| **total** | **~4 900** | |

At `size: 1000`, [`useAllResults`](web/dash0/src/api/hooks.ts#L1178)'s
`do … while (cursor)` loop turns that into **five sequential HTTP round-trips**,
each waiting on the previous, before a single pixel is drawn. 88 % of the
payload is raw points that will be squashed into 3 % of the chart width and are
individually invisible — the dot renderer already refuses to draw them
(`dotsEnabled` is false at that density).

And it repeats: the check-detail route re-fetches the **entire** window every
`refetchInterval`, which is `periodMs` — **every 60 s for a 1-minute check**
([`checks.$checkUid.index.tsx:585`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx#L585)).
Not incremental. All five round-trips, all five queries, per open dashboard,
per minute.

### What raw is actually for on a wide range

The stated reason to include raw alongside rollups is real: the aggregator
never rolls up a bucket until it closes, so without raw the chart's right-hand
edge is missing — the most recent, most interesting part. But that argument
justifies **the open bucket only**, not 24 hours of it. Everything older than
the newest closed rollup bucket is already represented, at a density the chart
can actually render.

### What the API does and does not already offer

`periodStartAfter` and `periodEndBefore` already exist and are already parsed
([`handler.go:63`](server/internal/handlers/results/handler.go#L64)), so a
client *can* restrict the window today. Two things are missing:

1. **The client cannot know where raw ends.** Raw retention is a system
   parameter (`performance.aggregation_retention_raw_hours`), resolved by
   [`systemconfig.ResolveAggregationRetention`](server/internal/systemconfig/retention.go#L53)
   and surfaced only on the admin Aggregation tab. Nothing org-facing exposes
   it. A client guessing too wide over-fetches; guessing too narrow leaves a
   visible gap at the tier seam.

2. **Nothing stops a wide raw request.** The endpoint is public API — dash0,
   the MCP tools, and third-party scripts all reach it. A `periodType=raw`
   request spanning a year is planned and executed against the largest table in
   the system with nothing to clamp it.

Also worth correcting while here: `periodEndBefore` does **not** filter on
`period_end`. Both dialects implement it as `period_start < ?`
([postgres `:2392`](server/internal/db/postgres/postgres.go#L2393)). For raw
rows the distinction is immaterial; for an aggregated bucket that starts inside
the window and ends outside it, the row is returned. That is the right
behaviour for charting — a partially-visible bucket should be drawn — but the
parameter name asserts something the code does not do.

## Proposal

### 1. Raw covers the seam, not the window

The rule, applied per range:

| range | rollup tier (pass 1) | raw window (pass 2) |
|---|---|---|
| hour | — | full window (raw **is** the tier) |
| day, period ≥ 5 min | — | full window (raw **is** the tier) |
| day, period < 5 min | `hour` | newest closed `hour` bucket → now |
| week | `hour` | newest closed `hour` bucket → now |
| month | `hour,day` | newest closed `hour` bucket → now |

When raw is the primary tier for the range, nothing changes. When rollups cover
the range, raw is asked only for the **seam** — the span between the newest
rollup bucket returned by pass 1 and now. For a month view that is at most one
open hour: **~180 points instead of 4 320**, one round-trip instead of five.

The seam boundary comes from pass 1's own data — the newest returned rollup
row's period — so no extra request is needed to compute it. That data
dependency is what makes this two passes rather than two parallel fetches, and
it is also what makes it correct when the aggregator is lagging: if rollups are
behind, the seam is wider and raw automatically fills more of it, which is
exactly the desired behaviour and the case a fixed "last hour" constant would
get wrong.

### 2. Two render passes, not one blocked render

Pass 1 (rollups) renders as soon as it resolves — a month view draws from ~600
points in one round-trip. Pass 2 (raw) merges into the same series and
re-renders. The chart is interactive after pass 1; it does not show a skeleton
until raw arrives.

Requirements on the transition:

- Pass 2 must **not** unmount or remount the chart, reset zoom, drop the pinned
  selection, or change the y-domain in a way that makes the plot visibly jump.
- If pass 2 fails, the chart stays usable on pass-1 data and surfaces the
  failure without discarding what is drawn.
- If pass 1 returns nothing (a check younger than one rollup bucket), pass 2
  must still run over the full window — a brand-new check has raw and nothing
  else, and must not render empty.
- The existing skeleton/loading state belongs to pass 1 only.

`detectGaps` already keys off `periodType` and already treats a tier transition
as "not a gap", so the seam needs no new gap logic — but pin that with a test,
because the seam is now the normal case rather than an edge case.

### 3. Polling refreshes the seam, not the window

The `refetchInterval` re-fetch keeps its cadence but re-runs **pass 2 only**.
Rollup buckets for closed periods do not change; re-downloading a month of them
every 60 s is pure waste. Pass 1 refreshes on a much slower cadence (the rollup
bucket width is the natural choice) or on an explicit user action — range
change, zoom, manual refresh.

This is where most of the steady-state cost of an open dashboard goes, and it
is the part of this spec with the largest cumulative effect.

### 4. API: bound the raw tier server-side, and say what was bounded

Client-side windowing fixes dash0. The endpoint is public, so the server must
hold the floor regardless of caller:

- When the request's `periodType` includes `raw`, clamp the effective
  `periodStartAfter` to `max(requested, now − rawRetention)` before building
  the filter. Rows older than that do not exist — the aggregator deleted them —
  so this removes work without removing results.
- Resolve `rawRetention` through `systemconfig.ResolveAggregationRetention`,
  never from `cfg.Aggregation.RetentionRaw` directly: the Aggregation settings
  tab writes `performance.*` DB parameters that never reach the koanf struct,
  and a reader clamping to 24 h while the job keeps 168 h would silently drop
  six days of raw that no rollup covers. `uptimebar`'s raw clamp
  ([`window.go:86`](server/internal/uptimebar/window.go#L86)) is the existing
  precedent — follow it rather than inventing a second resolution path.
- Report the effective window on the response so a client can tell a clamp from
  an empty range, and can size a follow-up request. Add a top-level `window`
  object alongside the existing `data` / `pagination` keys:

  ```json
  { "data": [...], "pagination": {...},
    "window": { "periodStartAfter": "…", "periodEndBefore": "…", "clamped": true } }
  ```

  Additive and optional — no existing consumer breaks.
- Document `periodEndBefore`'s real semantics (`period_start < value`, so a
  bucket straddling the end is included) in
  [`openapi.yaml`](server/internal/app/openapi/openapi.yaml) and
  `wiki/api-specification/`. Do **not** change the behaviour; charting depends
  on the straddling bucket being returned.

### Testing

- **Point-count assertion.** A month view of a 1-minute, 3-region check
  requests **≤ 1 rollup page and ≤ 1 raw page**, and the raw request's window is
  the seam, not 24 h. Assert on the issued request parameters, not on timing.
- **Seam continuity.** The merged series has no gap region at the
  rollup→raw boundary, and no duplicated point where the two tiers meet. The
  aggregator deletes source rows after rollup, so the tiers are disjoint in
  time — a duplicate means the seam was computed wrong.
- **Lagging aggregator.** With rollups deliberately stale by several hours, the
  seam widens and raw fills it; the chart's right edge is complete. This is the
  case a hard-coded "last hour" window fails, so it must be a fixture, not an
  assumption.
- **Brand-new check.** A check with raw rows and zero rollup rows renders its
  data — pass 1 empty must not short-circuit pass 2.
- **Progressive render.** With pass 2 pending, the chart is already drawn from
  pass-1 data (assert on rendered points, not on a loading flag). With pass 2
  rejected, pass-1 data stays on screen.
- **Zoom and selection survive pass 2.** An active zoom window and a pinned
  result are still in effect after the raw merge re-renders.
- **Polling scope.** Across a `refetchInterval` tick, the rollup request is
  **not** re-issued and the raw request is. This is the assertion that pins § 3;
  without it the optimisation silently regresses to re-fetching everything.
- **Server clamp (Postgres, testcontainers).** A `periodType=raw` request with
  `periodStartAfter` a year back returns the same rows as one clamped to the
  retention boundary, reports `clamped: true`, and — per spec `2026-08-22-04` —
  produces no `Seq Scan on results`.
- **Clamp honours the DB parameter.** With
  `performance.aggregation_retention_raw_hours` set to a non-default value, the
  clamp follows it, proving resolution goes through `systemconfig`. Include the
  positive control: a request *inside* the boundary is not clamped and reports
  `clamped: false`.

### Acceptance

- A month view issues one rollup request and one raw request, and draws from
  ~600 points plus the open bucket instead of ~4 900.
- The chart is interactive before raw arrives.
- A steady-state open dashboard re-fetches only the seam per tick.
- No visible discontinuity, duplicate point, or lost zoom/selection at the
  tier seam.

## Out of scope

- The raw/rollup query split itself — spec `2026-08-22-04`, a hard prerequisite.
- Server-side downsampling of raw to a pixel budget (LTTB or similar). It would
  make raw affordable at any width, but it is a much larger change and this spec
  removes the need for it on wide ranges by not requesting raw there at all.
  Revisit only if a *narrow* range with a very dense check turns out to be slow —
  which has not been measured.
- Recharts render cost. No React profiling has been done; do not speculatively
  rework the render path here.

## Implementation Plan

### A. Server — bound the raw tier and report the effective window (§4)

1. **`results.Service` learns the config.** `NewService(db, cfg)` (cfg may be
   nil → documented defaults), mirroring `availability.Service`. Callers:
   `app/server.go`, `mcp/handler.go`, the two Postgres plan tests.
2. **Clamp, raw-only.** In `ListResults`, when
   `models.PeriodTypesTierSide(filter.PeriodTypes) == models.PeriodTierRaw`,
   set `filter.PeriodStartAfter = uptimebar.RawTierStart(requested, now, rawHours)`
   with `rawHours` from `systemconfig.ResolveReadSideRetention(ctx, s.db, s.cfg)`.
   The spec says "includes raw"; the implementation deliberately narrows that to
   "**is only** raw": a mixed or unfiltered request also selects rollup rows,
   whose retention is months, and clamping those to the raw band would delete
   real results rather than remove dead work. `PeriodTierMixed` is already the
   thing spec 04 forbids clients from sending.
3. **`window` on the response.** New `WindowResponse{periodStartAfter,
   periodEndBefore, clamped}`, always emitted alongside `data`/`pagination`, so
   `clamped:false` is an observable answer and not just an absent key.
4. **Docs.** `openapi.yaml` gains the `periodStartAfter` / `periodEndBefore` /
   `limit` query params (currently undocumented) stating
   `periodEndBefore` = `period_start < value`, so a bucket straddling the end is
   returned; `OrgResultListResponse` gains `window`. Same wording in
   `wiki/api-specification/results-incidents.md`. Behaviour unchanged.

### B. Client — one shared two-pass window (§1, §2, §3)

5. **Extract the pure plan** into `web/dash0/src/lib/chart-window.ts`:
   `TimeRange`, `ChartTierFetch`, `ZoomWindow`, `CHART_WITH_FIELDS`,
   `chartFetchParams` (now taking an optional `rawStartAfter`), plus
   `seamStartFrom(rollupRows)`. `response-time-chart.tsx` re-exports the old
   names so existing imports and the spec-04 guard test keep working.
6. **Seam rule.** `seamStartFrom` groups pass-1 rows by region, takes each
   region's newest bucket edge (`periodEnd`, falling back to `periodStart`), and
   returns the **oldest** of those. Anchoring on the exclusive upper edge is what
   makes the two tiers provably disjoint: every rollup row ends at or before it
   and the raw query asks for `period_start >= it`, so no span can be drawn
   twice — without depending on the aggregator having already deleted that
   bucket's raw rows (true today, but an invariant of a different component).
   Taking the per-region minimum is what makes a region whose rollups lag get its
   own wider seam. No rollup rows at all → `undefined` → raw spans the full
   window (brand-new check).
7. **`useResultTiers` gains `enabled`** (only that — per-pass loading/error come
   free from calling it once per pass).
8. **`useChartWindowResults(org, checkUid, {timeRange, periodMs, zoom}, …)`** in
   `api/hooks.ts` runs the two passes: pass 1 = rollup tier (skipped when raw is
   the tier), pass 2 = raw, gated on pass 1 having settled and keyed on the seam.
   Merges through `mergeResultTiers`. Exposes `isLoading` = pass 1 only,
   plus `rawError` / `rawPending`. The window bound is resolved **once per hook
   instance** (`chartWindowBounds` memoized on the range/zoom alone) and handed
   to both passes via `chartFetchParamsForWindow`: an unzoomed start is
   `startOfMinute(now)`, and pass 2's plan is rebuilt after pass 1 settles, so
   re-deriving it per pass would let a minute tick re-key the raw query for the
   same window — and desynchronise the chart from the route, whose whole point is
   to share that key.
9. **Polling scope.** Pass 1 refetches at the rollup bucket width (1 h); pass 2
   keeps the caller's fast cadence. Range/zoom changes re-key both.
10. **The y-domain rescale is accepted, not prevented.** §2 asks that pass 2 not
    "change the y-domain in a way that makes the plot visibly jump". The chart's
    `<YAxis>` carries no `domain`, so recharts rescales from the merged data and
    adding raw at the right-hand edge can move the axis. That is kept
    deliberately: pinning the domain to pass 1's range would flatten a genuine
    spike in the newest points — the part of the chart people are actually
    looking at — and letting raw overflow a held scale would draw points outside
    the plot. What §2 is protecting against (a *jump*, i.e. the chart being torn
    down and rebuilt) is instead met by never unmounting: pinned by
    `web/dash0/src/components/checks/chart-progressive-render.test.tsx`, which
    asserts node identity across the merge. Recorded here rather than left to a
    review comment, per the audit of 2026-08-23.
11. **Both consumers use the hook** — `response-time-chart.tsx` and
    `checks.$checkUid.index.tsx` — so their query keys stay identical and the
    route remains a cache hit. The chart surfaces `rawError` as an inline note
    without discarding pass-1 data (`detail.chart.rawError`, 4 locales).

### C. Tests

- `lib/chart-window.test.ts` — seam rule (per-region minimum, lagging region,
  empty pass 1, seam never precedes the window start) and `chartFetchParams`
  with/without `rawStartAfter`; existing spec-04 guards extended to cover the
  seam form.
- `api/use-chart-window.test.tsx` (jsdom) — request-parameter assertions: one
  rollup + one raw request for a month view, raw window == seam not 24 h;
  lagging aggregator widens it; brand-new check fetches raw over the full window
  (the positive control that points ARE fetched); pass-2-pending still yields
  pass-1 rows with `isLoading` false; pass-2 rejection keeps pass-1 rows and sets
  `rawError`; across a poll tick raw is re-issued and rollup is not; **the chart
  and the check-detail route's real call-site inputs produce exactly one HTTP
  request per tier** (spec 04's cache-hit criterion, restated for the two-pass
  hook — the seam is a key input the spec-04 test cannot see); and the window
  bound survives a late-arriving `periodMs` across a minute tick.
- `components/checks/chart-seam.test.ts` — `detectGaps` (exported for the test)
  reports no gap across a rollup→raw transition, with a positive control that a
  real same-tier gap IS reported; plus the disjointness of the two tiers driven
  through the real pipeline (`seamStartFrom` → raw window → a server honouring
  `period_start >= …` → `mergeResultTiers`) over a deliberately adversarial
  fixture that still holds raw for the rolled-up hours, so a seam anchored one
  bucket too wide fails it.
- Go: `chart_raw_clamp_postgres_test.go` — a year-back raw request returns the
  same rows as one clamped to the boundary, reports `clamped:true`, and does not
  `Seq Scan on results`; with `performance.aggregation_retention_raw_hours` set
  to a non-default value the clamp follows it; positive controls — a request
  inside the boundary reports `clamped:false` and is left untouched, and a
  rollup-tier request a year back is NOT clamped.
