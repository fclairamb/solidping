---
model: opus
effort: high
---

# Per-org rate limiter starves the same checks forever — rotate the deficit instead

## Problem

Live prod incident (2026-08-26, org `public`, check `korben.info (http)`
`5c4c74ef-56ac-46e5-a06f-8f0b7756a466`): a 1-minute check produced **zero
results for 7.5 hours**, resumed for 2 executions after a deploy, and starved
again — while its org kept executing other checks at full rate. The Gravelines
worker logged, once a minute, every minute:

```
INFO "Check execution rate-limited; deferring to next period" check_uid=5c4c74ef-… limit=10
```

Mechanism, all confirmed on prod:

- SaaS orgs without a billing-written row resolve to
  `MaxChecksPerMinute = 10` (`server/internal/entitlements/defaults.go:70`).
  The `public` org schedules 21 region-jobs ≈ 20 executions/min — 2× the cap.
  Its throughput has been pinned at a flat plateau for 30+ hours; the deficit
  all lands on the *same* checks.
- The limiter is a token bucket, burst = cap, refill = cap/60 per second
  (`server/internal/entitlements/service.go:306–341`, bucket at
  `limiterFor`).
- When the bucket is drained, the worker gate
  (`server/internal/checkworker/worker.go:950–973`) calls `releaseLease` →
  `calculateNextScheduledAt` (`worker.go:1548–1583`) → `NextAligned`
  (`internal/checkworker/scheduling/phase.go:124–172`). The phase within the
  period is a **stable hash of the check UID** (`JitterFor`,
  `phase.go:48–57`), so each check's arrival second is fixed forever.
  korben.info hashes to second :44 — by then, 10 earlier-phased org
  siblings have drained the bucket. Same phases, same ordering, same losers,
  every minute.
- The codebase *already documents* an anti-starvation invariant: a skipped
  job's receding `scheduled_at` sorts it ahead of any on-time job
  (`internal/checkworker/scheduling/scheduling.go:184–187`; claim `ORDER BY
  effective_scheduled_at ASC` in
  `internal/checkworker/checkjobsvc/service.go:719–787`). **The rate-limit
  path defeats it**: `ReleaseLease`
  (`checkjobsvc/service.go:869–890`) advances `scheduled_at` to the next
  aligned tick *and re-anchors `effective_scheduled_at` to it*, so a
  rate-limited job never accumulates overdue-ness and never earns priority.
- Aggravator: the fetcher claims the **slow lane first**
  (`checkjobsvc/service.go:136–230`), so expensive checks drink the org's
  tokens before fast ones.
- The same gate exists on the deported-agent claim path
  (`internal/handlers/agentws/handler.go:831–845`) with the same deferral.
- Stale doc: `MaxDeprioritizeOffset` is `2 × 15s = 30s`
  (`scheduling.go:55`) but described as "60s" at `scheduling.go:187` and
  `checkjobsvc/service.go:743–744`.

Net effect: an over-cap org degrades as "half the checks run perfectly,
the other half never run", silently. The correct degradation is "every
check runs at ~cap/demand of its configured rate".

## Proposal

On a rate-limited deferral, **advance `scheduled_at` to the next aligned
tick as today, but preserve `effective_scheduled_at`** (leave it at the
missed tick; on repeated deferrals leave it untouched so it keeps receding
relative to now). The existing claim ordering
(`ORDER BY effective_scheduled_at ASC`) then does the fairness for free:
last window's losers are claimed first in the next window, and the deficit
rotates round-robin across the org. No busy-loop risk: the claim predicate
still gates on `scheduled_at <= now + ahead`, so a deferred job cannot be
re-claimed inside the same window.

Scope:

1. In-process worker path: the `QuotaError` branch at `worker.go:956–973`
   must release the lease *without* re-anchoring `effective_scheduled_at`
   (new parameter or dedicated method on the backend/`ReleaseLease` chain —
   in-process `DirectBackend` and the checkjobsvc write at
   `service.go:808–860`).
2. Agent claim path: `agentws/handler.go:831–845` gets the identical
   treatment.
3. Keep `NextAligned` phase alignment for `scheduled_at` — phases must stay
   deterministic (spec 2026-07-20-05); only the *priority* anchor changes.
4. Keep updating `delay_ewma_ms` on deferral (it is the diagnostic that
   exposed this incident).
5. Fix the stale "60s" comments to match `MaxDeprioritizeOffset` = 30s.
6. Update the invariant comment at `scheduling.go:184–187` to state that
   the rate-limit path now upholds it.

Tests (this is where the spec earns its keep — prove the negative):

- Fairness regression test: an org at 2× its cap with N 1-minute checks at
  adversarially clustered phases; run M simulated windows; assert **every**
  check executed, each at ≈ cap/demand of its slots (tolerance), and no
  check at 0. A positive control asserting the pre-fix behavior (one check
  at 0) must fail against the fixed code.
- Same assertion through the agent claim path.
- No-regression test for the non-limited path: `effective_scheduled_at`
  re-anchoring on a *successful* execution is unchanged.

## Non-goals / open questions

- **Bucket scope.** The bucket is per-process, so a multi-region org
  effectively gets `cap × regions` (prod `public` sustains 13/min against a
  10/min cap: 10 in gravelines + 1 each in paris/lauterbourg/kansas-city),
  despite `defaults.go` describing an aggregate org rate. Recommendation:
  keep the per-process bucket in this spec (it errs generous, never
  stingy), *document* the semantics where the default is defined, and leave
  a shared per-org reservation to a follow-up spec if we ever want the cap
  to be exact.
- Slow-lane-first token consumption is noted but not changed here; the
  rotation makes it a latency bias rather than a starvation source.
- Passive checks (heartbeat/email) return before the gate
  (`worker.go:904`) and never consume tokens — unchanged, but worth a
  clarifying comment near the gate.
