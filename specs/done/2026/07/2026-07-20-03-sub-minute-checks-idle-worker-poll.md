# Sub-minute checks run once per minute on idle workers (deported agents)

**Status:** Done
**Date:** 2026-07-20
**Reported as:** a 10s-period check assigned to a deported agent (private
location) executes only once per minute
(`/dash0/orgs/acmetech/checks/7f6276d9-7acc-4aea-b65c-dad9f6a5d8e1` on the
k8xp dev deployment).

## Diagnosis

The check worker's fetcher loop (`internal/checkworker/worker.go`) had exactly
three wake-up triggers after a claim attempt:

1. `completionChan` — a runner finished a job;
2. the `check.created` hint channel;
3. a flat `time.After(time.Minute)` fallback poll.

Meanwhile the claim path bounds each job's claim-ahead window to
`min(FetchMaxAhead, period/2, 30s)` (spec 2026-07-05-08 D3), so a 10s-period
job is only claimable **5 seconds** before it is due.

On an idle worker the sequence therefore was:

1. The job runs; completion wakes the fetcher (~1s after the tick).
2. The job was just rescheduled ~10s out; its claim window opens 5s before
   that. The immediate re-claim finds nothing.
3. Nothing else fires — no completions (pool idle), no created checks — so the
   fetcher sleeps the full fallback minute.
4. It wakes ~50s after the job became due, runs it once → **one execution per
   minute regardless of the configured period.**

A deported agent runs this exact loop over the WS backend and is idle by
construction (it only carries its private location's checks), so private
locations hit this deterministically. The in-process cloud worker has the same
latent bug; a busy pool masks it because completions keep re-triggering claims.

## Fix

The claim path now returns a **next-eligible hint** alongside the claimed
jobs: how long until the earliest still-unleased job in the caller's scope
becomes claimable (its `scheduled_at` minus its own per-job claim-ahead
window). The fetcher sleeps on that hint instead of the flat minute, floored
at 100ms so it can never spin, capped by the unchanged 1-minute fallback.

- `checkjobsvc.Service.ClaimJobs` / `ClaimJobsForAgent` return
  `(jobs, nextEligibleIn, error)`. The hint is computed inside the claim
  transaction, **after** the lease writes (so this batch's own claims are
  excluded), over a bounded horizon (`1min + 30s`) with a bounded scan
  (first 50 rows by `scheduled_at`). Leased rows are excluded — their owner's
  release/completion drives the next wake.
- The agent WS protocol's `jobs` frame carries the hint as `retryInMs`
  (rounded up). Optional on both sides: old agents ignore it, old servers omit
  it and the agent falls back to the old 1-minute cadence.
- `WorkerBackend.ClaimJobs` (Direct + WS) forwards the hint;
  `fetcherLoop` sleeps `nextPollDelay(hint)`.

With the fix, the idle-agent cycle for a 10s check becomes: completion at
~T+1s → claim returns empty + hint ≈ 4s → fetcher wakes at ~T+5s → claims the
job (window open) → runner sleeps in-slot until T+10s → executes on schedule.

## Files

- `server/internal/checkworker/checkjobsvc/service.go` — hint computation
  (`nextEligibleIn`), claim signatures, agent select extracted to
  `selectAvailableJobsForAgent`.
- `server/internal/checkworker/backend/{backend,direct,ws}.go` — interface +
  both backends.
- `server/internal/agents/protocol.go` — `retryInMs` on the jobs frame.
- `server/internal/handlers/agentws/handler.go` — hint on the claim response.
- `server/internal/checkworker/worker.go` — `nextPollDelay`, fetcher wait.

## Verification

- `checkjobsvc`: `TestClaimJobsNextEligibleHint` — hint ≈ `scheduled_at −
  period/2` for a 10s job; zero for claimed-in-batch / foreign-leased /
  beyond-horizon jobs; floored positive for eligible-but-unclaimed; agent
  variant scoped to (org, region).
- `checkworker`: `TestNextPollDelay` — fallback/cap/floor behavior.
- `agentws`: `TestClaimCarriesNextEligibleHint` — end-to-end over a real WS
  round trip, raw frame (`retryInMs`) and `WSBackend.ClaimJobs` return.

## Rollout note

The server computes the hint and the agent honors it, so **both** the server
and the deported agent binary must carry this change for a private location to
get sub-minute cadence; either side alone safely degrades to the previous
once-per-minute behavior.
