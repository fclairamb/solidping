---
model: opus
effort: high
---

# The ICMP burst limits are too tight, and the execution budget ignores `count` so any burst that meets packet loss is truncated and over-reports loss

*Raised by Florent on 2026-09-21, after a user deleted his account over sampling
density. Lowering the `interval` floor to 50ms is the ask. The budget defect
below is why that alone would not help.*

## Background — where this came from

A user signed up, created a 10-second ICMP check, ran it for 1h49m and deleted
everything. Asked why, he said the sampling was far too sparse: he is not
watching for outages, he is watching for **packet-loss bursts, latency spikes,
jitter and transient path anomalies**. He asked for ~50ms sampling, or "a
special high-frequency/burst monitoring mode at 50-250 ms for limited
diagnostic periods".

He did not know `count` and `interval` exist, because the form does not show
them (that is spec `2026-09-21-02`). This spec is about the limits themselves
and about a defect that makes the feature misreport exactly the events he cares
about.

## Problem 1 — the limits are arbitrary and too tight

`server/internal/checkers/checkicmp/checker.go`:

```go
// Validate Count (1-10)
if cfg.Count != 0 && (cfg.Count < 1 || cfg.Count > 10) { ... }

// Validate Interval (100ms - 60s)
if cfg.Interval != 0 && (cfg.Interval < 100*time.Millisecond || cfg.Interval > 60*time.Second) { ... }
```

Verified against production with `validate_check`:

| config | result |
|---|---|
| `{count: 10, interval: "100ms"}` | `valid: true` |
| `{count: 20, interval: "50ms"}` | `count: must be between 1 and 10` |
| `{count: 10, interval: "50ms"}` | `interval: must be between 100ms and 60s` |

So the densest burst available is **10 packets over 900ms**, once per execution.

**The interval floor costs nothing to lower.** `interval` only ever *adds*
delay: total run time is `Σ(per-packet time) + (count-1) × interval`. Going
from 100ms to 50ms at `count: 10` makes the burst 450ms **shorter**, not
longer. There is no runtime argument for the 100ms floor.

## Problem 2 — the execution budget does not know about `count` (the real blocker)

`server/internal/checkworker/worker.go` resolves one number and uses it for two
unrelated things:

```go
checkTimeout := r.schedParams.ExecutionTimeout(checkJob.CostEWMAMs)
if perCheck, userSet := perCheckTimeout(checkJob.Config); userSet {
    checkTimeout = perCheck          // the user's `timeout`
}
...
execCtx, cancel := context.WithTimeout(context.Background(), checkTimeout+time.Second)
```

`ExecutionTimeout` (`scheduling/scheduling.go:261`) clamps on cost EWMA and a
ceiling. **It never looks at `count`.** Meanwhile the same `timeout` value is
handed to the checker as the **per-packet** read deadline —
`performICMPPings(ctx, ip, isIPv6, count, timeout, interval)` →
`performSingleICMPPing(..., timeout, i)` → `context.WithTimeout(ctx, timeout)`.

So one value means both "how long may one packet take" and "how long may the
whole burst take". The burst needs, worst case:

```
count × timeout + (count-1) × interval
```

and it is given `timeout + 1s`. Solving `count×t + (count-1)×i ≤ t + 1s`:

```
(count - 1) × (timeout + interval) ≤ 1s
```

| count | interval | max usable timeout |
|---|---|---|
| 10 | 100ms | **11ms** — unusable |
| 10 | 50ms | **61ms** — barely |
| 5 | 100ms | 150ms |
| 3 | 100ms | 400ms |

With `timeout` **unset** it is worse, not better: the resolved budget (15s by
default) is threaded back into the checker config by `configWithDefaultTimeout`
as the per-packet timeout, so **one** lost packet consumes 15s of a 16s budget
and the rest of the burst is cut off.

### Why this is a correctness bug, not just a limit

`performICMPPings` appends a failure for every packet it did not get to:

```go
for i := 0; i < count; i++ {
    if ctx.Err() != nil {
        results = append(results, pingResult{Success: false, Error: ctx.Err()})
        continue
    }
```

Those are counted in `metricPacketsSent` / `metricPacketsReceived` /
`metricPacketLossPct`. **Packets that were never transmitted are reported as
lost.** The moment a burst meets real loss it blows its budget, truncates, and
reports a loss percentage higher than the truth — on the exact check whose
purpose is measuring loss. A 10-packet burst that loses 2 packets to a 1s
timeout reports 100% loss, not 20%.

## What to do

1. **Lower the interval floor to 50ms.** Free; it shortens runs. Consider 10ms
   — the ask mentions 50–250ms, and nothing in the loop objects.
2. **Raise the count ceiling** once 3 is fixed, not before. 60 packets at 50ms
   is a 3-second window and a plausible target.
3. **Give the burst its own budget.** Either size the execution budget as
   `count × timeout + (count-1) × interval` (+ margin), or split the two
   meanings: a per-packet `timeout` and a separate overall budget. The second
   is clearer and stops `timeout` being overloaded.
4. **Never count an unsent packet as lost.** Whatever the budget, a truncated
   burst must report what it actually sent — shrink `packets_sent`, or mark the
   result partial. This is the half that is a bug today regardless of limits.
5. **Consider sending concurrently.** `performSingleICMPPing` opens and closes
   a socket per packet and the loop is sequential, so burst duration scales with
   loss. `fping` and `mtr` send asynchronously and collect replies, which makes
   the run time `(count-1) × interval + timeout` **independent of loss**: 10
   packets at 50ms with a 1s timeout is 1.45s worst case instead of 10.45s.
   That is what makes a dense burst cheap enough to widen the limits properly,
   and it directly answers "as long as the checker doesn't take too long to
   run".

## Out of scope

Continuous 50ms sampling as a check period. That is 20 results rows per second
per check against a scheduler built on discrete executions, and a 10
checks-per-minute org cap (`defaultMaxChecksPerMinuteSaaS`). If it is wanted it
is a bounded on-demand diagnostic run, not a check, and it needs its own spec.

## Also missing, named by the same user

There is **no jitter figure** anywhere in the checkers — only `rtt_ms_min`,
`rtt_ms_max`, `rtt_ms_avg` (the sole `jitter` hit in the tree is an unrelated
`jitter_ms` in `checksleep`). Standard deviation or mdev over the burst is the
same loop that already computes min/max/avg, and it is the number that makes
this line-quality data rather than uptime data. Worth folding in here or
splitting out.
