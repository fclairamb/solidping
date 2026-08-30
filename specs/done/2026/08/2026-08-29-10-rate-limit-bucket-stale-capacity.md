# Per-org rate-limit bucket keeps its creation-time capacity after an entitlements change

## Problem

`Service.limiterFor(orgUID, capacity)` in `server/internal/entitlements/service.go`
creates the org's token bucket on first use and returns the cached bucket on
every later call **without ever reconciling its capacity**:

```go
bucket, ok := s.limiters[orgUID]
if !ok {
    bucket = newTokenBucket(capacity, s.now())
    s.limiters[orgUID] = bucket
}
return bucket
```

`tokenBucket.allow` refills at `b.capacity / 60` per second — the *stored*
capacity, not the currently resolved limit. So when an org's
`MaxChecksPerMinute` is raised (billing push, superadmin editor), every process
that already holds a bucket for that org keeps enforcing the OLD cap until it
restarts. The `QuotaError` meanwhile reports the NEW resolved limit, which makes
the deferral log line actively misleading: `rate-limited ... limit=100` while
the bucket actually refills at 10/min.

The `ResetOnSet` behavior only helps the process that performs the Set (the main
API server). Deported check workers (`solidping-checks-*`) each run their own
in-process entitlements service fed by DB reads — they resolve the new limit
correctly but never rebuild the bucket.

## Observed in production (org `public`, 2026-08-29)

1. Org sat at the SaaS default 10 checks/min; the workers' buckets were created
   with capacity 10.
2. `maxChecksPerMinute` was raised to 100 via the superadmin editor, and the
   org's demand went to ~70/min spread over 6 regions (~12–14/min per worker).
3. Every worker kept refilling at 10 tokens/min → a permanent ~2–4 skips/min
   per worker. `checksPerMinute.skippedToday` climbed past 600 while the
   dashboard showed demand 70 / limit 100 with no reason to skip anything.
4. One check (`archive.org`, phase-aligned last in its worker's minute) was
   deferred every single minute for ~1h — zero results despite a healthy
   worker, surfacing as a check that "never runs".

Workaround: restart the checks workers; fresh buckets pick up the new capacity.

## Proposal

- In `limiterFor`, when the cached bucket's capacity differs from the resolved
  `capacity`, update it in place (adjust `capacity`; on a raise optionally
  top up `tokens` by the delta so the raise takes effect immediately; on a
  lower, clamp `tokens` to the new capacity). Replacing the bucket outright is
  also acceptable — a one-time burst on change is better than a permanently
  wrong rate.
- Regression test: create bucket at 10, drain it, resolve with capacity 100,
  and require `allow` to succeed at a >10/min pace without a service restart.
- Audit the starvation rotation while here: the deferral is supposed to make
  "this window's loser next window's first pick" (spec 2026-08-26-02), yet the
  same check lost every window for an hour under a persistent deficit.

## Related

Found while diagnosing the same org as
[[2026-08-29-09-configequal-type-blind-job-sync]] — the two bugs compounded:
one check never ran (this bug) while six others failed on a stale config
snapshot (the configEqual bug).
