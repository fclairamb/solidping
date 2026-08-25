---
model: sonnet
effort: high
---

# The check page's response-time chart shows "No data available" while it is still loading — on first paint and on every day → week → month switch

## Problem

On the check detail page, the response-time chart renders a *terminal, negative*
empty state — **"No data available"** — during phases where the answer is simply
not in yet. The user reads "this check has no data", then a moment later a chart
appears. It is wrong twice: wrong at the moment it is shown, and wrong as a
signal (a check that has been up for a month is told it has nothing).

The branch is at
[response-time-chart.tsx:928](web/dash0/src/components/checks/response-time-chart.tsx:928):

```tsx
{isLoading ? (
  <Skeleton className="h-[300px] w-full" />
) : chartData.length === 0 ? (
  <div className="h-[300px] flex items-center justify-center text-muted-foreground">
    {t("detail.chart.noDataAvailable")}
  </div>
) : ( /* the chart */ )}
```

The skeleton *exists* — the bug is that `isLoading` is too narrow, so control
falls through to the empty state while a fetch is genuinely in flight.

### Why `isLoading` is false while the chart is still loading

`isLoading` comes from `useChartWindowResults`
([hooks.ts:1515](web/dash0/src/api/hooks.ts:1515)), which is deliberately a
**two-pass** fetch (spec 2026-08-22-07): pass 1 = rollups over the whole window,
pass 2 = raw over the seam between the newest rollup bucket and now. Its
contract, stated in its own doc comment, is:

> `isLoading` is pass 1 ONLY — the skeleton belongs to the first render, not to
> the raw merge.

That contract is right for the case it was written for (rollups arrived → draw
them, merge raw in later) and wrong for the case where **pass 1 settles with zero
rows**:

- [hooks.ts:1585](web/dash0/src/api/hooks.ts:1585) — `isLoading: hasRollupTier ? rollup.isLoading : raw.isLoading`
- [hooks.ts:1567](web/dash0/src/api/hooks.ts:1567) — raw is `enabled: !hasRollupTier || !rollup.isLoading`, i.e. it *starts* only once pass 1 has settled
- [hooks.ts:1556](web/dash0/src/api/hooks.ts:1556) — when pass 1 returns no rows, `seamStart` is `undefined` and raw correctly widens to the **whole window**

So the sequence on a week/month view whose rollup tier is empty — a check younger
than one rollup bucket, a freshly-filtered region, a window the aggregator has
not covered — is:

1. pass 1 in flight → `isLoading: true` → skeleton. Correct.
2. pass 1 settles with `[]` → `isLoading: false`, `chartData.length === 0`, raw
   only now becomes enabled → **"No data available"**.
3. raw returns → the chart appears.

Step 2 is the flash. The hook already computes and exports exactly the signal
that would have prevented it — `rawPending: raw.isLoading`
([hooks.ts:1588](web/dash0/src/api/hooks.ts:1588)) — and **the chart never reads
it**: `rawPending` is referenced only in tests
(`chart-progressive-render.test.tsx`, `use-chart-window.test.tsx`), never in
`response-time-chart.tsx`.

### The second path: a disabled query is not "loading"

`useResultTiers` reports `isLoading: queries.some((q) => q.isLoading)`
([hooks.ts:1477](web/dash0/src/api/hooks.ts:1477)). Two ways that is `false`
while nothing has been fetched:

- **A disabled query.** In react-query v5 `isLoading === isPending && isFetching`,
  so a query held back by `enabled` is `isPending` but **not** `isLoading`. Both
  gates here can do that: `enabled: !!org && …` ([hooks.ts:1439](web/dash0/src/api/hooks.ts:1439))
  and raw's `enabled: !hasRollupTier || !rollup.isLoading`.
- **An empty tier list.** `[].some(…)` is `false`, so a pass with no tiers reads
  as "loaded". `rollupTiers` is `[]` for every range where raw *is* the tier
  ([hooks.ts:1545](web/dash0/src/api/hooks.ts:1545)); the `hasRollupTier` ternary
  covers today's callers, but the primitive itself lies.

### Why switching range makes it visible

`updateTimeRange` ([response-time-chart.tsx:415](web/dash0/src/components/checks/response-time-chart.tsx:415))
flips `timeRange`, which changes `bounds`, `rollupTier` and therefore **both**
query keys. Nothing is cached under the new keys, so the chart re-enters the
sequence above from scratch — including step 2 — on every day → week → month
click. Switching *back* to a range whose keys are still cached paints instantly,
which is why the flash reads as intermittent rather than systematic.

### What must not regress

The progressive render is the point of the two-pass design and is pinned by
[chart-progressive-render.test.tsx:120](web/dash0/src/components/checks/chart-progressive-render.test.tsx:120)
— *"draws pass-1 data while pass 2 is still pending, and keeps it across the
merge"*. **A fix that shows the skeleton whenever `rawPending` is true breaks
it**: rollups on screen must stay on screen while raw merges in, in the very same
DOM node (the test asserts node identity, because a remount would drop an
in-progress zoom drag). The distinction the fix has to draw is *empty **and**
still fetching* vs *empty **and** settled*.

## Proposal

Keep the two-pass contract; make the **empty state** conditional on the whole
window having settled, not on pass 1 alone.

### 1. Make the emptiness question honest in the hook

In `useChartWindowResults` ([hooks.ts:1515](web/dash0/src/api/hooks.ts:1515)),
export a derived flag alongside the existing ones — name it for what it means,
e.g. `isEmptyPending`: *no rows merged yet and at least one pass has not
settled*. It must be true when either pass is pending-but-not-yet-fetching
(disabled or just re-keyed), not only when `isLoading`/`rawPending` is true — so
`useResultTiers` needs to surface `isPending` (`queries.some((q) => q.isPending)`,
**and** `tiers.length > 0 && queries.length === 0` guarded so an empty tier list
never reads as settled) in addition to `isLoading`/`isFetching`.

Leave `isLoading`, `rawPending`, `rawError` semantics exactly as documented —
other call sites (`checks.$checkUid.index.tsx`) depend on them.

### 2. Use it in the chart

At [response-time-chart.tsx:928](web/dash0/src/components/checks/response-time-chart.tsx:928),
make the three-way branch:

- **skeleton** when `isLoading` (unchanged: first paint of pass 1) **or** when
  `chartData.length === 0 && isEmptyPending` (the new case — empty so far, still
  fetching);
- **chart** whenever `chartData.length > 0`, regardless of `rawPending`
  (unchanged — this is the progressive render);
- **"No data available"** only when the window has settled with nothing.

Reuse the existing `<Skeleton className="h-[300px] w-full" />` — it already
animates (`animate-pulse`, [skeleton.tsx](web/dash0/src/components/ui/skeleton.tsx))
and it already occupies the exact 300 px the chart will, so nothing below it
jumps. No new component, no new i18n string. Add a `data-testid` to the empty
state (`response-time-chart-no-data`) so tests can assert its *absence*, which is
the whole point.

Check whether the same fall-through exists in the sibling surfaces that share
this hook — the region chips / duration stats on
[checks.$checkUid.index.tsx](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1167)
and `availability-table.tsx` — and fix them the same way if so; do not widen
beyond that.

### 3. Tests — prove the negative

Extend
[chart-progressive-render.test.tsx](web/dash0/src/components/checks/chart-progressive-render.test.tsx),
which already mocks `useChartWindowResults` and so can pin every phase exactly:

1. **The bug, as a test.** Pass 1 settled empty, pass 2 pending
   (`isLoading: false`, `rawPending: true`, `data: []`) → the skeleton is
   present and `response-time-chart-no-data` is **absent**.
2. **Positive control.** Everything settled with no rows (`isLoading: false`,
   `rawPending: false`, `data: []`) → "No data available" **is** shown. Without
   this, test 1 passes by simply deleting the empty state.
3. **No regression.** Re-assert the existing progressive-render case: rollups +
   `rawPending: true` → the chart wrapper renders, same DOM node across the
   merge, no skeleton.
4. **Range switch.** Re-render with a changed `initialPeriod`/window in the
   pending-empty state and assert the skeleton, not the empty state.

Cover the hook itself in
[use-chart-window.test.tsx](web/dash0/src/api/use-chart-window.test.tsx): the new
flag is true while raw is disabled behind pass 1, true while raw is in flight
after an empty pass 1, and false once both passes settle.

### Notes

- Unrelated to `specs/todos/2026-08-25-02-flaky-chart-window-waitfor.md` (a test
  flake in the same hook's suite), but both touch
  `use-chart-window.test.tsx` — expect to rebase one onto the other.
- Per `CLAUDE.md`, start from the design reference
  (`/dash0/orgs/default/design-reference`) before touching UI; `Skeleton` is
  already catalogued there, so this needs no addition to it.
- Run `bun run test:unit` in `web/dash0` as well as the Playwright suite — a
  dash0 unit run is easy to skip and is where all four assertions above live.

## Implementation Plan

1. **`useResultTiers` (hooks.ts:1426)** — add `isPending` to the returned
   object: `queries.some((q) => q.isPending) || (tiers.length > 0 &&
   queries.length === 0)`. The second disjunct is a defensive guard (per the
   spec text) — with the current `useQueries({ queries: tiers.map(...) })`
   wiring, `queries.length` always equals `tiers.length`, so it can never
   actually fire today, but it documents/protects the invariant "an empty
   tier list must never be mistaken for a settled pass" if that wiring ever
   changes. Leave `isLoading`/`isFetching`/`error` untouched.

2. **`useChartWindowResults` (hooks.ts:1515)** — add a new returned field
   `isEmptyPending: data.data.length === 0 && (rollup.isPending ||
   raw.isPending)`, computed after the existing `data` memo. Leave
   `isLoading`, `rawPending`, `rawError` untouched (call sites depend on
   them). Document in a doc comment why this is different from `isLoading`:
   it also catches a pass that is disabled-but-not-yet-fetching (raw held
   back behind pass 1) or a tier list not yet resolved, not only an
   in-flight fetch.

3. **`response-time-chart.tsx:928`** — destructure `isEmptyPending` from the
   hook call (~line 462). Change the three-way branch to:
   - skeleton when `isLoading || (chartData.length === 0 && isEmptyPending)`
   - chart when `chartData.length > 0` (unchanged)
   - "No data available" (with `data-testid="response-time-chart-no-data"`)
     otherwise.

4. **Sibling surfaces** — `checks.$checkUid.index.tsx:659` destructures only
   `data` from `useChartWindowResults` and never renders a terminal empty
   state off it (`observedRegions`/`durationStats` sections are simply
   omitted while empty, not replaced by a false "no data" message), so the
   bug this spec fixes does not exist there — no change needed.
   `availability-table.tsx` uses `useCheckAvailability`, a wholly separate
   REST endpoint/hook, not `useChartWindowResults` — out of scope, no shared
   fall-through, no change.

5. **Tests** — extend `chart-progressive-render.test.tsx`'s `state()` helper
   default to include `isEmptyPending: false`, then add the four cases from
   the spec (bug, positive control, no-regression re-assert, range-switch).
   Extend `use-chart-window.test.tsx`'s `Consumer` to also render
   `isEmptyPending`, and add hook-level cases: true while raw is disabled
   behind pass 1, true while raw is in flight after an empty pass 1, false
   once both passes settle.

6. QA gate: `bun run test:unit`, `make build-dash0`, `bun run lint` (scoped
   to touched files for new-error-free, full run for the gate).
