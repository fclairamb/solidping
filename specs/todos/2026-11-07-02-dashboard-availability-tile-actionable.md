---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# The dashboard's 24 h availability tile warns "Degraded" with nothing to act on, next to an all-green banner

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/48

## Source

In-app report on the org dashboard (`/d/orgs/:org`, v0.44.0, French UI). The 24 h availability tile shows **99.88 %** with a yellow **Degraded** badge, while the banner above says "All systems operational" with a green "24h SLA Operational" pill, the Down tile says 0 and the Active incidents tile says 0. The tile cannot be clicked. The reporter's complaint: a clear warning, and no way to find out why or act on it.

The report is right on both counts: the page contradicts itself, and the tile is the only KPI tile that is not a link.

## Problem

- **The badge reads as a current state but measures the past 24 h.** `availabilityTier` (`web/dash0/src/lib/availability-tier.ts:19-24`) maps the server-computed `stats.availability24h` to `operational` (>= 99.9), `degraded` (>= 99) or `down`. 99.88 % is any short outage in the last 24 h (about 1.7 min of downtime across the fleet), already resolved. The label "Degraded" (`locales/*/dashboard.json`, `kpi.availabilityBadge.degraded`) says the fleet is degraded *now*.
- **The banner contradicts it.** `OverallStatusBanner` (`web/dash0/src/components/dashboard/dashboard-page.tsx:612-620`) shows the "24h SLA Operational" pill whenever `availabilityPct !== null`, whatever the tier. With a degraded 24 h figure the same page says the 24 h SLA is both operational and degraded.
- **The tile is a dead end.** The availability tile is a plain `<div data-testid="kpi-tile-availability">` (`dashboard-page.tsx:397`); the three other tiles are `<Link>`s (checks, down checks, active incidents). The active-incidents link would not help either: the dip may come from resolved incidents, or from failed results that never opened an incident (below the check's incident threshold).
- **Nothing in the UI names the checks behind the figure.** The checks list (`routes/orgs/$org/checks.index.tsx:161`) filters on current status only; no view ranks checks by their own 24 h availability. `GetOrgAvailability24h` (`server/internal/db/{sqlite,postgres}`) only returns the org total.

## Why a spec

- A product/UX decision the reporter didn't settle: what "degraded" should mean on this tile, and where the tile should lead.
- Possibly a new or changed API (a per-check 24 h availability breakdown) if the tile is to name the culprits.

## Proposal

1. **Copy that says "past", not "now".** Rename the non-operational tiers of the tile's badge to describe the 24 h window, e.g. `degraded` → "Below 99.9 %" / « Sous 99,9 % », `down` → "Below 99 %" / « Sous 99 % » (keep the tier colours). The sub-line becomes "Last 24 h, all checks". Update every locale of `dashboard.json`.
2. **Banner consistency.** Show the "24h SLA Operational" pill only when the tier is `operational`; for `degraded` / `down` either hide it or show the matching warning pill (see open question 2).
3. **Make the tile a link** (`data-testid="kpi-tile-availability"` kept on the link), to the place that explains the figure (open question 1).
4. **Tests.** Extend `web/dash0/e2e/dashboard.spec.ts` (it already mocks `stats.availability24h`): at 99.88 % with no incidents, the badge does not read "Degraded", the banner does not show the "SLA Operational" pill, and clicking the tile navigates to the chosen target. Keep the existing no-data cases green. Unit-test any tier/label change in `lib/availability-tier.test.ts`.
5. Add the linked availability tile state to the design reference if its look changes.

## Open questions

1. **Where does the tile lead?**
   - (a) **Recommended:** the incidents list, all states (`/orgs/$org/incidents`), which shows the resolved incidents of the last day. Cheap, no API change. Trade-off: a dip with no incident (failures below the incident threshold) still lands on an empty list.
   - (b) The checks list ordered by 24 h availability, worst first. Fully answers "which checks?", but needs a per-check 24 h availability field (or sort) on the checks list API: a bigger change.
   - (c) The events page filtered on the last 24 h.
2. **Banner pill when the 24 h tier is not operational:** recommended, hide the pill (the banner then only states the current state, which is what "All systems operational" means). Alternative: a warning pill "24h availability below target", more information but more noise.

## Closing the issue

This spec closes fclairamb/solidping-business#48. The implementing PR body **must** carry a `Closes fclairamb/solidping-business#48` line. When the spec is archived to `specs/done/`, verify the issue is closed and, if not, close it by hand:

    gh issue close 48 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
