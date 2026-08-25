---
model: sonnet
effort: low
---

# Two rollup points sharing a `period_start` sort nondeterministically in the response-time series

## Problem

[`trimResponseTimeSeries`](server/internal/handlers/statuspages/service.go#L2495)
merges the two tier branches of `fetchRecentResults` into one per-(check,
region) series with:

```go
sort.SliceStable(rows, func(i, j int) bool {
    if !rows[i].PeriodStart.Equal(rows[j].PeriodStart) {
        return rows[i].PeriodStart.After(rows[j].PeriodStart)
    }

    return rows[i].PeriodType == models.PeriodTypeRaw &&
        rows[j].PeriodType != models.PeriodTypeRaw
})
```

The comparator has exactly two rungs: `period_start DESC`, then
raw-beats-rollup. It says nothing about **two rollup points that share a
`period_start`** — and those are routine, not exotic: the rollup tiers are
`hour`, `day` and `month`
([`responseTimeRollupTiers`](server/internal/handlers/statuspages/service.go#L2406)),
so the `hour` bucket at midnight and the `day` bucket for that same day carry
the same `period_start`, every day. At a month boundary all three collide.

For such a pair the comparator returns `false` in both directions, so
`sort.SliceStable` preserves input order — which is whatever order the DB
happened to return. The row-order guarantee that
[`applyResultsFilter`](server/internal/db/postgres/postgres.go#L2410) used to
provide (`ORDER BY result.period_start DESC, result.uid DESC`) did not survive
the move to `RecentResultsPerCheck` in spec `2026-08-22-05`.

Impact today is cosmetic: both points existed before that change too, and the
chart renders them identically, which is why this was accepted rather than
blocking `2026-08-22-05`'s archive. The reason to fix it anyway is that
order-dependent output with no defined order is the classic source of an
intermittently failing snapshot / E2E assertion later — one that will read as a
flake and cost far more to diagnose than the tie-break costs to add now.

## Proposal

Add a final, total tie-break to the comparator in `trimResponseTimeSeries`,
restoring what the replaced query guaranteed:

```go
if !rows[i].PeriodStart.Equal(rows[j].PeriodStart) {
    return rows[i].PeriodStart.After(rows[j].PeriodStart)
}

iRaw := rows[i].PeriodType == models.PeriodTypeRaw
jRaw := rows[j].PeriodType == models.PeriodTypeRaw

if iRaw != jRaw {
    return iRaw
}

return rows[i].UID > rows[j].UID // uid DESC — matches the pre-2026-08-22-05 query
```

`uid DESC` is the tie-break to prefer over, say, ordering by period type: it is
what the previous query guaranteed, so any assertion written against the old
behaviour keeps holding, and it is total (UIDs are unique) rather than merely
finer-grained.

Update the function's doc comment — it currently explains only the raw-wins
rung — to say that the final rung is `uid DESC` and why (a deterministic series
for equal-`period_start` rollup points across tiers).

### Test

Add a unit test in `server/internal/handlers/statuspages/` covering the case the
current comparator leaves open:

- Build one check/region series with two **rollup** points of different
  `period_type` (`hour` and `day`) sharing one `period_start`, plus a couple of
  ordinary points on either side so the primary rung is exercised too.
- Feed it in both possible input orders and assert the same output order both
  times, with the higher UID first.
- Keep a case that pins the existing raw-beats-rollup rung, so the new rung
  cannot silently take precedence over it.

**Confirm the test fails without the fix.** With `sort.SliceStable` and only two
rungs, a single input order will pass by accident — feeding both permutations is
what makes the test actually detect the missing tie-break, so verify the
red state before committing the fix rather than assuming it.

### QA

- `make build-backend lint-back`
- `go test ./internal/handlers/statuspages/`

Note: `go test ./internal/config/` fails 3 tests on this machine because the
hostname (`Host-003.lan`) fails the worker-slug regex. Run it with
`SP_NODE_NAME=testnode` if you touch it at all; the failure is pre-existing and
unrelated to this spec.
