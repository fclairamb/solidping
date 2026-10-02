# The response-time stats strip never shows unless you pick a region

## Problem

A user who set up several checks and drove the dashboard from a phone reported
the single thing he felt was missing: numbers for the response times over the
range he is looking at, so he can tell whether it got slower or faster instead
of eyeballing the line on the chart.

Those numbers already exist. The check detail page computes min / avg / max /
p95 / sample count over the chart window, tier-aware, and renders them as a
strip:
[`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:947`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:947)
for the computation,
[`:1960`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1960) for
the render.

The render is gated on `effectiveRegion && durationStats`. `effectiveRegion` is
`undefined` whenever the region filter is on "All"
([`:923`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:923)),
which is the default and the only state most users ever see. So the feature is
invisible until someone clicks a region chip they have no reason to click.

`computeDurationStats` already handles the all-regions case: `region`
undefined means "fold every point"
([`:319`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:319)). Its
own doc comment records the gate as deliberate — "not by the stats strip, which
only renders for a specific region" — but the reason is not written down, and
from the outside it reads as a feature that shipped switched off.

Two smaller problems in the same place:

- The strip lives inside the **Recent Results** card, under the result table's
  header. The numbers describe the **chart** window. They are far from the
  thing they summarise, and on a phone they are below the fold.
- The one-line flex row wraps to several lines on a narrow screen, where this
  user was.

## Proposal

1. **Render the strip for "All regions" too.** Drop the `effectiveRegion &&`
   half of the guard, keep `durationStats &&`. The label already names the
   window (`detail.results.stats.window`); extend it to name the scope as well,
   so "All" versus "eu-west" is never ambiguous.
2. **Decide the multi-region fold explicitly, and write it in the comment.**
   Across regions, `min`/`max` become the extremes over every region (honest,
   and arguably what you want), `avg` stays the totalChecks-weighted mean (so a
   dense region does not get out-voted by a sparse one), `p95` stays the
   unweighted average of per-row p95s. That last one is the weakest number in
   the set and is already flagged `isEstimate` whenever a rollup row is in the
   window — check whether a mixed-region window should flag it too.
3. **Move the strip to the chart card**, directly under the chart and above the
   region chips, since that is the window it describes. It stays scoped by the
   same `region` search param, so selecting a region still narrows it.
4. **Make it readable on a phone**: a two-or-three-column grid of labelled
   values rather than a wrapping flex row.

## Open questions

- Was the region gate guarding against a real correctness worry about mixing
  regions with different latencies into one average? If so, the fix is a
  clearer label ("across 3 regions"), not keeping it hidden. Decide this first
  — it is the only thing in the spec that could turn it into "don't".
- The strip and the chart read the same `chartWindowResults` query, so this
  adds no fetch. Confirm that is still true after the move.

## Resolved open questions

- Region gate: treat it as not a correctness guard. Render the strip for "All regions" and label the scope explicitly (for example "across 3 regions"). Do not keep it hidden.
- Same query: the strip and the chart keep reading `chartWindowResults`. Add no new fetch, and verify that after the move.

## Not in scope

The median (`specs/todos/2026-10-02-02-store-duration-p50.md`) and the
window-over-window trend
(`specs/todos/2026-10-02-03-response-time-trend-vs-previous-window.md`), both
asked for in the same report.
