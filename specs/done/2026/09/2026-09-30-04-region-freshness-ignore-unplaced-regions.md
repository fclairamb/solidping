---
model: sonnet
effort: low
---

# Region freshness shows a "No result from Paris" warning for a region the check no longer runs in

## Problem
On https://solidping.k8xp.com/d/orgs/public/checks/66f65fe2-e621-44f0-a433-fae75f66c593 the Placement reads "Automatic, 2 regions: Gravelines, Kansas City", both report (49s and 18s ago), yet a "Region freshness" block says "No result from Paris since 08:09 AM, 2 other regions reporting" with a stale clock row "Paris · 39m 8s ago".

Paris was a region the check ran in before automatic placement moved it. It is no longer selected, so being silent is expected. The block should not appear when every selected region is reporting.

Cause: `BuildRegionFreshness` (`server/internal/handlers/checks/service.go:1848`) emits one entry per region found in `ListLastRealResultPerRegion` (raw results within retention), whatever `check.Regions` says today. The old region's last result ages past the stale threshold and is flagged `Stale`. `RegionFreshnessList` (`web/dash0/src/components/checks/check-freshness.tsx:59`) then shows the block because `silent.length > 0`.

## Proposal
1. In `BuildRegionFreshness` (`server/internal/handlers/checks/service.go:1848`), when `check.Regions` is non-empty, skip rows whose region is not in `check.Regions`. When it is empty (default placement, nothing to compare against), keep the current behaviour. The MCP diagnose tool shares this function (`server/internal/mcp/tools_diagnose.go:236`), so it gets the same fix. Update the function doc comment.
2. No frontend change needed: `RegionFreshnessList` and `check-summary-cards.tsx:34` (`regionsDisagree`) read the server list. Confirm the "Last checked" summary card no longer shows a per-region age line for Paris.

## Tests
- `server/internal/handlers/checks/` (existing `BuildRegionFreshness` test file): check placed on `[gravelines, kansas-city]`, rows for gravelines, kansas-city and a 39-minute-old `paris` -> result has 2 entries, none stale, no `paris`.
- Same file, negative case: a placed region (`kansas-city`) that is genuinely silent stays in the list with `Stale: true`, and a placed region with no row is still appended.
- Same file: `check.Regions` empty -> rows pass through unchanged (no filtering).
- `web/dash0/src/components/checks/check-freshness.test.tsx`: `RegionFreshnessList` renders nothing when the list holds only reporting regions on a non-stale check (guards the contract the server now honours).
- `mcp` diagnose test, if one covers silent regions: an unplaced silent region is not reported as silent.

## To verify
- That `check.Regions` holds the currently placed regions for auto-placed checks (the placement line in the screenshot suggests so, `check.regions` in `check-placement.tsx:36`), not the pool.
- Whether `check.StaleThreshold()`/`checks.status` (the "stale" status itself) also reads unplaced regions; the screenshot shows status "Up", so probably not.
