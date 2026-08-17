---
model: sonnet
effort: medium
---

# Per-check availability computes its five periods serially

## Problem

The check detail page renders an availability table for five windows —
`1h,24h,30d,90d,365d`
([availability-table.tsx:31-38](web/dash0/src/components/checks/availability-table.tsx:31)) —
in a single request to `GET /orgs/:org/checks/:check/availability`.

The service walks those windows in a plain sequential loop —
[service.go:127-135](server/internal/handlers/availability/service.go:127):

```go
for i := range windows {
    row, periodErr := s.computePeriod(ctx, org.UID, check, windows[i], now)
    if periodErr != nil {
        return nil, periodErr
    }

    periods = append(periods, row)
}
```

and each `computePeriod` issues **two** independent database round trips —
[service.go:152](server/internal/handlers/availability/service.go:152)
(`uptimebar.WindowAvailability`) and `s.fetchIncidents`. Five windows is
therefore **ten serial round trips**, and the endpoint's latency is their sum.

Measured on `solidping.k8xp.com` (2026-08-16), one check:

| Request | Time |
|---|---|
| `periods=1h` | 0.96 s |
| `periods=24h` | 1.52 s |
| `periods=30d` | 2.16 s |
| `periods=90d` | 1.25 s |
| `periods=365d` | 1.36 s |
| **sum of the five** | **7.25 s** |
| `periods=1h,24h,30d,90d,365d` (what the page sends) | **8.14 s** |

The combined request costs the sum, confirming there is no concurrency and no
sharing of work between windows.

**The per-query cost is the dominant term and is not this spec's problem** — it
is [spec 2026-08-17-03](specs/todos/2026-08-17-03-uptimebar-queries-seq-scan-results-table.md),
which removes a full-table scan worth ~1.5 s per window. That spec should land
first. What remains after it is the shape fixed here: even with every query fast,
paying for ten round trips end to end is strictly worse than paying for the
slowest one, and the serial loop is what makes a future per-query regression
multiply by five instead of showing up once.

## Proposal

### 1. Compute the windows concurrently

The windows are fully independent: each `computePeriod` reads `org.UID`, `check`
and its own `windows[i]`, and writes only its returned `Period`. Nothing is
shared or ordered. Replace the loop with a bounded-concurrency fan-out using
`golang.org/x/sync/errgroup`:

- Write results into a **pre-sized slice by index** (`periods[i] = row`), not by
  appending — the response order must stay the order the caller requested, and
  appending from goroutines both races and scrambles it.
- Keep `errgroup`'s first-error semantics to preserve today's behaviour: any
  period failing fails the whole request. Use `errgroup.WithContext` so a failure
  cancels the siblings instead of leaving them running.
- **Set `g.SetLimit(...)`.** `periods` is caller-controlled, so an unbounded
  fan-out is a request-amplification vector: one HTTP request could otherwise
  open an arbitrary number of simultaneous DB connections. A small fixed limit
  (4–8) captures nearly all the win at bounded cost.

### 2. Bound the number of periods accepted

`parsePeriods` should reject an unreasonable period count with a
`VALIDATION_ERROR` rather than accepting, say, 200 tokens. Combined with the
`SetLimit` above this closes the amplification path at both ends. Suggest a cap
around 16 — comfortably above the 5 the UI sends and any plausible API use.

### 3. Consider collapsing the incident fetches

Each window separately calls `s.fetchIncidents` for the same check, differing
only in time range. Since the widest requested window contains all the others, a
single fetch over `[min(start), max(end))` sliced per window in Go would replace
five queries with one. Worth doing **only if** it stays clearly readable —
correctness of the per-window incident block matters more than one saved query,
and `incidentBlock` has window-relative semantics (clamping to `now`, to the
window edges, and to `check.CreatedAt`) that must not drift. If it complicates
that logic, leave the per-window fetches to the concurrency fix above.

## Verification

- A request for all five periods issues its window queries concurrently and
  returns in approximately the time of the **slowest** window, not their sum.
- Response rows stay in the requested token order, verified with a deliberately
  out-of-order `periods` list (e.g. `365d,1h,30d`).
- Values are identical to the serial implementation for a fixed dataset — this
  is purely a scheduling change.
- A failing period still fails the whole request, with the same error code as
  today.
- `go test -race` passes on the new fan-out; add a test that exercises it with
  several periods so the race detector has something to inspect.
- A request with more periods than the cap returns `VALIDATION_ERROR`.
