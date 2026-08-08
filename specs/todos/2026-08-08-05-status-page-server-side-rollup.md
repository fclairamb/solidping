---
model: sonnet
effort: high
---

# Status page overall status is computed client-side, with wrong unknown/maintenance semantics

## Problem

The "All Systems Operational" banner at the top of a public status page is
computed entirely in the status0 frontend —
[status-page-view.tsx:73](web/status0/src/components/shared/status-page-view.tsx:73)
(`getOverallStatus`). The server's public payload
(`StatusPageResponse`, [service.go:274](server/internal/handlers/statuspages/service.go:274))
carries no aggregate at all. That has three consequences:

1. **`unknown` collapses to "operational".** `getOverallStatus` has no default
   branch: a resource with no check data (or status `unknown`/`created`)
   contributes nothing, so a page whose checks have never run proudly shows
   "All Systems Operational".
2. **Maintenance never reaches the banner.** `inMaintenance` is only rendered
   per-resource ([status-page-view.tsx:102](web/status0/src/components/shared/status-page-view.tsx:102),
   [:138](web/status0/src/components/shared/status-page-view.tsx:138)); there is
   no page-level "under maintenance" state, and a resource that goes down
   *during* its maintenance window flips the hero to "System Outage".
3. **No single source of truth.** Upcoming consumers — a public summary API,
   a page-level SVG badge, an embeddable JS widget (specs 2026-08-08-06/07/08)
   — would each have to reimplement the aggregation, and could disagree with
   the status page itself.

## Proposal

Compute the rollup server-side, once, and make status0 a consumer.

### Backend

- Add a page-level rollup function next to the existing group rollup
  ([check_group_status.go:23](server/internal/db/models/check_group_status.go:23),
  `RollupGroupStatus` — the shape to follow), producing a **page status enum**
  distinct from check wire statuses:
  `operational | degraded | down | maintenance | unknown`.
- Semantics, evaluated over all resources of all sections:
  1. Resources currently **in maintenance are excluded** from the outage scan
     (maintenance masks failures — expected behavior on every major status
     product).
  2. Any non-maintenance resource with status `down`/`error`/`timeout` → `down`.
  3. Else any resource with `degraded`/`warning` → `degraded`.
  4. Else if any resource is in maintenance → `maintenance`.
  5. Else if **no** resource has a usable status (all `unknown`/`created`/no
     check attached) → `unknown`.
  6. Else → `operational`.
  - Resources backed by a check group use the group's already-computed rollup
    status as their input.
- Add `overallStatus` (plus per-state `statusCounts`) to `StatusPageResponse`
  and populate it in `ViewStatusPage`
  ([service.go:1330](server/internal/handlers/statuspages/service.go:1330)) and
  the default-page view. Keep the rollup function pure and exported — specs 06
  (summary endpoint) and 07 (page badge) call it directly.

### Frontend (status0)

- Delete `getOverallStatus`; render the banner from `overallStatus`.
- Extend the hero badge beyond today's 3-way ternary
  ([status-page-view.tsx:274](web/status0/src/components/shared/status-page-view.tsx:274))
  to cover `maintenance` (blue/info styling) and `unknown` (muted/gray), with
  colors and variants centralized in
  [status-style.ts](web/status0/src/lib/status-style.ts).
- New labels in every status0 locale
  ([status.json](web/status0/src/locales/en/status.json) and siblings), e.g.
  "Under Maintenance" and "Status Unknown".

### Tests

- Table-driven tests for the rollup covering each rule above, including the
  maintenance-masks-down case and the all-unknown case (with a positive
  control: one `up` resource among unknowns → `operational`).
- Handler test asserting `overallStatus` is present on the public payload.
- Update the status0 E2E assertions on `overall-status-badge`
  (`data-testid`, [status-page-view.tsx:274](web/status0/src/components/shared/status-page-view.tsx:274)).

### Out of scope

The summary endpoint, page-level badge, and JS widget are follow-up specs
(2026-08-08-06, -07, -08) that build on this rollup.

## Implementation Plan

### Backend

1. `server/internal/db/models/page_status.go` (new file, next to
   `check_group_status.go`): `PageStatus` string enum (`operational | degraded
   | down | maintenance | unknown`), `PageResourceStatus{Status CheckStatus,
   InMaintenance bool}` input struct, `PageStatusCounts` tally struct, and the
   pure exported `RollupPageStatus([]PageResourceStatus) (PageStatus,
   PageStatusCounts)` implementing the 6 priority rules exactly as specced.
   No DB access, no request context — specs 06/07 call it directly.
2. `server/internal/db/models/page_status_test.go`: table-driven tests, one
   case per rule, including maintenance-masks-down and all-unknown +
   positive-control.
3. `server/internal/handlers/statuspages/service.go`:
   - Add `OverallStatus string` + `StatusCounts *StatusCountsResponse` to
     `StatusPageResponse` (new `StatusCountsResponse` wire struct mirroring
     `models.PageStatusCounts`), both `omitempty` since only the public view
     paths populate them.
   - `getCheckInfo` / `getGroupInfo` now also return the resource's raw
     `models.CheckStatus` (pre-`publicCheckStatus` mapping) alongside the
     existing `*ResourceCheckInfo` — that raw enum is what
     `RollupPageStatus` needs; the public string is a lossier display-only
     vocabulary.
   - `enrichResourceInfo` now also collects a `[]models.PageResourceStatus`
     from every resource it enriches (a lookup failure counts as an
     unknown-status resource, never silently dropped) and returns
     `models.RollupPageStatus(...)` of that list, so the rollup can never
     disagree with what was actually rendered.
   - `ViewStatusPage` sets `response.OverallStatus` /
     `response.StatusCounts` from that return value.
     `ViewDefaultStatusPage` already delegates to `ViewStatusPage`, so it
     inherits this for free — no separate change needed there.
   - The admin `GetStatusPage` call site is updated for the new
     `enrichResourceInfo` signature but does NOT set
     `OverallStatus`/`StatusCounts` (admin listings don't need it).
4. `server/internal/handlers/statuspages/service_test.go`: update the
   `getCheckInfo` call site for its new return arity.
5. `server/internal/handlers/statuspages/overall_status_test.go` (new file):
   handler-level tests driving `ViewStatusPage`/`ViewDefaultStatusPage`
   end-to-end across the same rule matrix, asserting `overallStatus` +
   `statusCounts` on the wire response.

### Frontend (status0)

6. `web/status0/src/api/hooks.ts`: add `overallStatus` and `statusCounts` to
   the `StatusPage` interface.
7. `web/status0/src/lib/status-style.ts`: add `"maintenance"` (blue/info) and
   keep/confirm `"unknown"` (muted/gray) cases; add an `"info"` `BadgeVariant`
   + matching `badge.tsx` CVA variant (blue) since none exists yet.
8. `web/status0/src/components/shared/status-page-view.tsx`: delete
   `getOverallStatus`; render the hero badge from `page.overallStatus`
   (falling back to `"unknown"` if absent, e.g. against an older cached
   response), covering all 5 states via `statusStyle` + new locale keys.
9. Locale files (`en`, `de`, `es`, `fr` `status.json`): add
   `underMaintenance` / `statusUnknown` keys alongside the existing
   `allSystemsOperational` / `someSystemsDegraded` / `systemOutage`.
10. `web/status0/e2e/`: new `overall-status-badge.spec.ts` using
    `page.route` to mock the public status page API response with each of
    the 5 `overallStatus` values, asserting the hero badge's exact rendered
    text (`data-testid="overall-status-badge"`) — avoids depending on live
    seed data the way `maintenance-badge.spec.ts` does.
