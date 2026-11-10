---
model: opus
effort: high
pr: batch
automerge: false
---

# Unmeasured time depends on the check's current region count, so a region change rewrites history

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/52

## Source

In-app report on a check detail page (v0.44.0): after the check's regions changed, the
Availability card shows large "unmeasured" amounts on every row although the check ran the
whole time. The reporter expects unmeasured time not to depend on a specific region. The
screenshot (a UDP check, every 5 min, auto placement, 2 regions now, results from 4 different
regions over the last hour) shows 100% availability with "22m 30s unmeasured" for the last hour,
"11h 52m" for 24 h, "15d 2h" for 30 days and "42d 14h" of 76 days since creation.

The report is right: those figures are an artefact of the region count, not missing measurements.

## Problem

- Coverage is `results ÷ expected probes`, with
  `expected = window ÷ period × max(1, len(check.Regions))`
  (`server/internal/db/models/coverage.go:9-24`), and unmeasured time is
  `(1 − coverage) × monitored` (`server/internal/handlers/availability/service.go:279-283`).
- `len(check.Regions)` is the **current** region set, applied to the whole window. A check that
  ran in one region for most of its life and in two since a placement change expects twice the
  probes it could have produced: "since creation" reads ~55% unmeasured, which matches the
  screenshot (42d of 76d). The last-hour figure is exactly 15 results ÷ (60/5 × 2) = 62.5%,
  i.e. 22m 30s unmeasured, while the chart shows results every 5 minutes from some region.
- Auto placement (spec `2026-09-25-06-auto-region-placement-and-quorum`) moves checks between
  regions on its own, so this happens without any user action.
- The same expectation feeds the SLO budget coverage (`server/internal/handlers/slos/service.go:628`,
  `server/internal/slo/budget.go:161`) and the status page bucket coverage
  (`server/internal/handlers/statuspages/service.go:3991-3994`).

## Why a spec

A product decision (what "unmeasured" means for a multi-region check), and the change spans
three subsystems (availability card, SLO budgets, status pages) plus the uptimebar aggregates
that would have to supply per-bucket counts.

## Proposal

1. Define unmeasured time as wall-clock time where **no region** produced a result: per
   aggregate bucket (raw / hour / day tier, whatever `uptimebar` already returns), the bucket is
   measured in proportion to `min(1, results_in_bucket ÷ (bucket_length ÷ period))`, with no
   region multiplier. Sum over buckets for the window.
2. Use the same per-bucket rule for the status page coverage and the SLO budget coverage, so
   the three never disagree.
3. Keep `models.ExpectedProbes` (with regions) only where it is a capacity figure
   (entitlements), and rename/introduce the region-independent helper for coverage.
4. Tests: a check whose region count changed mid-window reads 0 unmeasured when every period
   had a result from some region; a real gap (no result from any region for 2 h in 24 h) reads
   2 h unmeasured; regression tests on the availability service, SLO budget and status page
   coverage. Update the coverage notes in the docs/wiki that describe spec 2026-09-25-02.

## Open questions

1. **Should a multi-region check with one region silent while another reports count as fully
   measured?** Recommended: yes (unmeasured = nobody measured), as the reporter asks; a silent
   region is a region-health concern, already surfaced by "Region went dark" and the per-region
   chart. Trade-off: a 2-region check running in one region only hides that half the probes
   are missing from the availability card.
2. **Per-bucket granularity for long windows** (30 days / since creation use hour or day
   rollups): recommended to accept the rollup granularity (a day bucket with results all day
   long counts measured by its count), rather than adding a per-period gap table. Trade-off: a
   few-hour gap inside a day rollup is only seen proportionally.

## Closing the issue

This spec closes fclairamb/solidping-business#52. The issue was closed when the spec was
written; when the spec is archived to `specs/done/`, check it is closed and close it by hand
otherwise:

    gh issue close 52 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"

## Resolved open questions

1. Yes: a period is measured when any region reported (autopilot: recommended answer).
2. Accept the rollup granularity, no per-period gap table (autopilot: recommended answer).
