---
model: sonnet
effort: high
---

# Public status page initial load is too slow: load it in stages instead of one 1.2 s / 210 KB call

## Problem
The HAR from `status.solidping.io` (`default/solidping`) shows one blocking call that gates the whole first paint:

- `GET /api/v1/status-pages/default/solidping`: 1215 ms (1203 ms `wait`, 7 ms `receive`), 210 KB decoded, zstd.
- Nothing renders before it returns. Every other request is under 220 ms (HTML 159 ms, the font 215 ms).
- Payload breakdown (measured in the HAR): `sections` 197 KB (3 sections, 14 resources, ~17.5 KB `availability` each), `recentUpdates` 30 KB (100 entries, window = `historyDays`), everything else under 1 KB.
- The server wait is the cost: `computeStatusPageView` (`server/internal/handlers/statuspages/service.go:2597`) does live enrichment, then availability/response-time enrichment (`:2631`), then active incidents, then `loadRecentUpdates` (`:2653`), all in one memoized blob. `Cache-Control: public, max-age=60` only helps warm hits.

The pieces for a staged load already exist but the page does not use them:
- `GET .../status-pages/{org}/{slug}/summary` (`handler.go:645`, hook `usePublicStatusPageSummary` in `web/status0/src/api/hooks.ts:437`) is the cheap rollup used by TV mode.
- `?include=` already narrows the page view (`view_options.go`: tokens `availability`, `responseTime`; `include=` means neither). Memo keys are per include set (`memo.go:97`).
- The page route calls `usePublicStatusPage(org, slug)` with no `include`, so it always pays for everything (`web/status0/src/components/pages/status-page-route.tsx:23`, `index-page.tsx:31`).

## Proposal
Keep the endpoints, call the page view several times with a growing `include`, and render each stage as it lands.

1. Stage 1, summary: fetch `/summary` first and render the header, overall status banner and status counts from it (same data as TV mode). Touches `status-page-route.tsx`, `index-page.tsx`, and a skeleton for the rest.
2. Stage 2, checks without details: `GET .../status-pages/{org}/{slug}?include=` (neither section). Renders sections and resources with current status, no availability bars or response-time charts. Server cost is `loadSectionsWithResources` + `enrichResourceInfo` only.
3. Stage 3, details: `GET ...?include=availability,responseTime` (the existing narrow-by-token path). Fills in the bars and charts. Use the same query key shape as `usePublicStatusPage` (`hooks.ts:364`) so the three results do not collide, and keep `placeholderData`/`keepPreviousData` so stage 2 data stays on screen while stage 3 loads.
4. Stage 4, incidents: add a new `include` token `updates` (backend `view_options.go`, `ViewOptions`, `memo.go` key, `service.go:2653`). Without `updates`, `computeStatusPageView` skips `loadRecentUpdates` and omits `recentUpdates`. With it, the window is capped at the last 7 days (`min(page.HistoryDays, 7)`), passed down to `ListPublicStatusUpdates`. Active incident publications (the banner, `service.go:2641`) stay in every stage: they are cheap and the banner must not appear late.
5. Backward compatibility: a request with no `include` keeps returning everything it returns today (all sections, full `historyDays` of updates). Third parties, the embed widget (`web/status0/src/embed/widget.ts`) and the Atom feed must not change. Document the new token in `server/internal/app/openapi/openapi.yaml` and the status page docs under `web/docs/`.
6. Server-side time: the 1.2 s is the first thing to explain. Before building stage 3, profile `enrichWithAvailability` (`service.go:2631`) on the prod-sized page (14 resources). If it is N+1 queries per resource, batch them. Record the before/after wait time in the PR.
7. Cold cache: in `memo.go`, warm the three include shapes used by the page (`""`, `availability,responseTime`, `+updates`) so the first visitor after expiry does not pay for the heaviest one. Only if profiling in step 6 shows a cold-compute cost; otherwise skip.
8. Stage 4 UI: the recent-updates timeline shows a skeleton, then the 7-day list; add a "show older" action that refetches with the page's full `historyDays` window (needs a way to ask for it: e.g. `updatesDays` param, bounded by `historyDays`). Keep it to one extra param, no pagination.

## Tests
- `server/internal/handlers/statuspages/view_options_test.go`: `include=updates` parses; `include=availability,updates` parses; unknown token still 400; absent `include` still equals all sections (including updates).
- `server/internal/handlers/statuspages/memo_test.go`: `include=` and `include=updates` are distinct memo entries; a narrow read does not trigger `loadRecentUpdates` (counting stub, like the existing `computeReads` pattern at `:306`).
- `server/internal/handlers/statuspages/service` test: without `updates`, `recentUpdates` is omitted; with it, entries older than 7 days are excluded even when `historyDays` is 90; absent `include` returns the full `historyDays` window (negative/regression case).
- Same test file: `include=` response has no `availability` on resources and no `overallAvailabilityPct`; `showAvailability`/`showResponseTime` still describe page settings.
- `web/status0/src/api/hooks.test.ts`: URL built for each stage (`?include=`, `?include=availability,responseTime`, `...,updates`); query keys differ per stage.
- `web/dash0` is not touched. A status0 Playwright/unit test: with a delayed stage 3 response, the page shows sections and statuses before availability bars appear; a failing stage 3 (500) leaves stage 2 content visible and does not blank the page.
- Locked page (401 `STATUS_PAGE_LOCKED`) at stage 1 still shows the unlock form, and no later stage fires.

## To verify
- The app logs were not available when this spec was written; the 1.2 s is taken from the HAR only. Check prod server logs (or `SP_PROFILER_ENABLED`) for the time split between `enrichResourceInfo`, `enrichWithAvailability`, the incidents query and `ListPublicStatusUpdates`, and whether the HAR request was a memo miss.
- Whether `/summary` already carries everything stage 1 needs (name, description, branding, `overallStatus`, `statusCounts`, language, theme). If not, extend it rather than adding a new endpoint.
- Whether `ListPublicStatusUpdates` (`service.go:2665`) can take a days argument already (it takes `page.HistoryDays`) or needs a signature change.
- Whether the CSP / `statuspagecache` headers (`statuspagecache.ApplyGated`) need anything for the extra requests (they should not).

## Decisions
- The 7-day cap on incidents applies only when `updates` is requested in an explicit `include`. A request with no `include` keeps the full `historyDays` window, so existing consumers are unchanged.
- The new token is named `updates`, since tokens mirror JSON field names (`view_options.go:13`) and the field is `recentUpdates`.
