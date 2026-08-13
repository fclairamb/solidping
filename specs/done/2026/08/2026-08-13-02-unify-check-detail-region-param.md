---
model: sonnet
effort: high
---

# Check detail: merge `graphRegion` / `resultsRegion` into one `region` param, no reload on change

## Problem

The check-detail page (`/orgs/$org/checks/$checkUid`) carries **two independent
region search params** — `graphRegion` (response-time chart filter) and
`resultsRegion` (Recent Results table filter) — declared in `CheckDetailSearch`
([checks.$checkUid.index.tsx:97-105](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)).
The `validateSearch` comment at `:122-124` explicitly notes they're independent
and defers coupling ("see spec 2026-07-05-13"). Real usage shows that split has
no value: when an operator isolates a region they mean the *page* is scoped to
that region, so selecting a region on the chart should also filter the results
table (and stats strip), and vice versa. Today you end up with URLs like

```
…/checks/57c34008-…?resultsRegion=default&graphRegion=default
```

where both params must be set by hand, one selector at a time.

Second issue: **changing the region must not feel like a page reload.** Today
the chart holds its region in local `useState` seeded once from the
`initialRegion` prop ([response-time-chart.tsx:445-457](web/dash0/src/components/checks/response-time-chart.tsx))
and only writes *out* via `onSettingsChange` → `navigate({ replace: true })`
([checks.$checkUid.index.tsx:1087-1114](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)).
Because the chart never reads the URL after mount, the only way to make an
external region change (results-table selector, back/forward, in-app link)
reach the chart would be remounting it (e.g. a `key` prop) — which visually
reloads the chart (spinner, lost zoom/selection). The unified param must flow
through plain client-side `navigate` with the chart *controlled* by the URL
value, so a region switch re-renders in place: no route remount, no chart
remount, no refetch beyond what the region filter itself requires (region
filtering is client-side today — both the chart and the stats strip already
filter the fetched window in memory).

## Proposal

Frontend-only (dash0). No API change.

1. **Single `region` search param.** In `CheckDetailSearch`, replace
   `graphRegion` / `resultsRegion` with one optional `region?: string`.
   No back-compat for the old keys: `validateSearch` simply ignores
   `graphRegion` / `resultsRegion`, and an old deep link falls back to the
   default "All regions" view.

2. **One source of truth, controlled components.**
   - Route: read `region` from `Route.useSearch()`; keep the existing
     stale-slug guard (`observedRegions.includes(region)` → else treat as
     "All", [checks.$checkUid.index.tsx:620-627](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)).
   - Chart: drop the `selectedRegion` useState / `initialRegion` seeding in
     [response-time-chart.tsx:445-457](web/dash0/src/components/checks/response-time-chart.tsx);
     take `region` (and `onRegionChange`) as controlled props. The chart's own
     `effectiveRegion` guard (`:607-608`) stays as-is.
   - Results table: `setResultsRegion` (`:629-635`) becomes the same
     `setRegion` used by the chart chips — one navigate, `replace: true`,
     functional `search: (prev) => …` so unrelated params survive.
   - Selecting a region anywhere immediately scopes chart, Recent Results,
     and the duration-stats strip together.

3. **No reload on change.** Region changes go exclusively through
   `navigate({ to: ".", search: (prev) => …, replace: true })` — no
   `window.location`, no remount `key`, and the chart must not lose its
   fetched data, zoom window (`graphFrom`/`graphTo`), or selected point
   (`graphSelected`) when only `region` changes. (Whether a region switch
   should *clear* the selected point if that point belongs to another region
   is an implementer judgment call — clearing is acceptable, remounting is
   not.)

4. **Sweep the literal call sites.** ~20 navigations across dash0 pass the
   literal `search={{ graphPeriod: undefined, graphFull: undefined,
   graphRegion: undefined, resultsRegion: undefined }}` (dashboard-page,
   status-dashboard, dependencies-card, checks.index, events, incidents,
   status-pages, jobs.check, maintenance-windows, test.generate,
   test.templates, checks.new, checks.$checkUid.edit,
   checks.$checkUid.results.$resultUid, $org.tsx, dependencies.index).
   Update them to the single `region: undefined` key. The result-detail
   back-navigation comment referencing `resultsRegion`
   ([checks.$checkUid.results.$resultUid.tsx:25](web/dash0/src/routes/orgs/$org/checks.$checkUid.results.$resultUid.tsx))
   moves to `region` too.

5. **Tests.** Update `web/dash0/e2e/check-detail.spec.ts` (it references the
   old params): assert (a) selecting a region on the chart filters Recent
   Results and vice versa, (b) the URL carries `?region=…` only, and
   (c) switching region does not remount the chart (e.g. no
   loading-spinner reappearance / chart container identity preserved).

Out of scope: backend, status0, any per-region API change.
