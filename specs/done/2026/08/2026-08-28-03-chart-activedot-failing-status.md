---
model: sonnet
effort: low
---

# The response-time chart's hover dot renders error/timeout points green on a red line

## Problem

The response-time chart decides "failing = red" in three places, but only two
of them agree. The line gradient and the per-point dot renderer both route
through the shared `isFailingStatus()` helper
(`web/dash0/src/components/checks/response-time-chart.tsx:314`), which counts
`unknown` plus everything `statusStyle(status).isDown` flags — i.e. `down`,
`error` **and** `timeout` — as failing. The `activeDot` (hover/selected dot)
renderer instead hardcodes its own check at
`response-time-chart.tsx:1342`:

```ts
const fill =
  payload.status === "down" ||
  payload.status === "unknown"
    ? COLOR_DOWN
    : COLOR_UP;
```

So hovering an `error` or `timeout` point shows a **green** dot sitting on a
**red** line — the exact "the graph is red but the point looks green (up)"
confusion reported from the prod `sftp-test-rebex` outage investigation on
2026-08-28. The comment block above `isFailingStatus` explicitly says it is
shared "so line color and dot color agree on what counts as failing"; the
activeDot predates or bypassed that consolidation.

## Proposal

- Replace the hardcoded status comparison in the single-series `activeDot`
  renderer (`response-time-chart.tsx:1342`) with
  `isFailingStatus(payload.status) ? COLOR_DOWN : COLOR_UP`.
- Audit the multi-series (per-region) `activeDot`/dot renderers around
  `response-time-chart.tsx:1412` for the same hardcoding and apply the same
  substitution wherever a red-vs-neutral decision is made from `payload.status`
  outside `isFailingStatus()`.
- Tests: the chart already has component tests
  (`chart-progressive-render.test.tsx`, `chart-availability-strip.test.tsx`);
  add or extend a unit test asserting dot color classification — at minimum a
  direct test that the fill decision for statuses `error` and `timeout`
  matches the line gradient's (both red), and `warning`/`degraded`/`abandoned`
  stay non-red. Testing the extracted predicate is acceptable if driving
  recharts' activeDot render in jsdom proves impractical.

Constraints: `web/dash0` `eslint .` carries ~25 pre-existing errors on base —
the bar is "no NEW lint errors", not fixing the debt. No behavior change for
genuinely up/warning points; this is a color-consistency fix only.
