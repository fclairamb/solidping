---
model: opus
effort: high
---

# A remote region with slow database round-trips loses check leases and stalls

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/63

## Source

Daily pod-log review (prod, 0.44.3): one remote checker region (its database is the master,
reached over a long network path) was flagged `Region stalled: live workers but jobs overdue`
for ~18 minutes, then `Region recovered`. During that window its checker logged
`failed to release lease: no rows updated: job may have been claimed by another worker` twice,
and its steady `slow SQL query` WARN flood was +120 % over its baseline. Another region logged
one `Failed to claim jobs` with an `i/o timeout` to the database the same night. The reporter's
hypothesis: under slow DB round-trips the lease expires before the worker releases it, so another
worker claims the job.

The other points of the same report are handled elsewhere: the per-event watchdog WARN is fixed
in commit `05ccee3f` (`fix: stop warning on every ops notice…`), the `browser` sidecar OOM is
tracked by fclairamb/solidping-business#57, the dev S3 403 is a dev bucket credential to check
in the deployment (not code), the star-prompt line can be silenced per deployment with
`SP_APP_STARPROMPT=false`, and the dash0 file-read error is already fixed on `main`.

## Problem

- The lease is set at claim time to `max(scheduled_at, now) + period + 30s`
  (`server/internal/checkworker/checkjobsvc/service.go:1061-1067`). It is not extended while the
  check runs, and nothing relates it to the check's own timeout or to how long the worker's
  database round-trips take.
- The release (`ReleaseLease` / `ReleaseLeaseWithSchedulingState`, `service.go:1153`, `:1231`)
  only matches `lease_worker_uid = ?`, and `execRelease` (`service.go:1279`) maps 0 rows to
  `ErrJobClaimedByAnother`. So when a worker's claim, run, result submission and release together
  take longer than the lease, a peer re-claims the job (the claim predicates accept
  `lease_expires_at < now`, `service.go:645` and the following claim queries), the check runs
  twice, and the first worker's release fails with the logged error.
- With a short period (e.g. 30 s → 60 s lease) and a remote region where each statement costs
  hundreds of milliseconds, the margin is thin. The regionsweep stall detector
  (`server/internal/regionsweep/apply.go:107`) then sees overdue jobs while the workers are alive.
- Not yet proven: the 18-minute stall may also come from claims timing out (`i/o timeout`)
  rather than from lost leases alone. The fix must start by measuring.

## Why a spec

- Correctness hinges on concurrency (lease ownership between workers of one region).
- Touches the claim, the release and possibly the result submission paths, and maybe a config
  knob.

## Proposal

1. **Measure first.** Add, per region, a histogram of the time from claim to release (or to
   result submission) and a counter of `ErrJobClaimedByAnother` on release
   (`prommetrics`). Log the release failure with the lease's `lease_expires_at`, `now`, the
   check period and timeout, so a log line alone shows by how much the lease was missed.
2. **Size the lease on what the run can take.** Compute the lease as
   `max(scheduled_at, now) + max(period, checkTimeout) + margin`, where `margin` defaults to
   30 s and is configurable per deployment (`SP_CHECKERS_LEASE_MARGIN`), so a remote region can
   be given more slack without code changes.
3. **Or extend it while running.** If step 1 shows runs regularly outliving the lease, have the
   worker extend `lease_expires_at` (guarded by `lease_worker_uid`) before submitting when the
   remaining lease is under the margin. Pick 2 or 3 from the step 1 data (see open question).
4. **Claim timeouts.** `Failed to claim jobs` with `i/o timeout`: check the claim context
   deadline against the slow-query latency of a remote region, and make a timed-out claim back
   off rather than leave the region idle.
5. Tests: a checkjobsvc test where the lease expires between claim and release proves the
   second claim is refused while the lease is valid (with the new sizing) and that the release
   error is counted; a regionsweep test is not needed.
6. Docs: the operations runbook (`wiki/runbooks/operations-gotchas.md`) gains a line on the
   lease margin knob for remote regions.

## Open questions

- **Size the lease up front (2) or extend it during the run (3)?** Recommended: 2 first. It is
  one statement and one knob, with no extra write per run; 3 adds a write per long run and only
  pays off if step 1 shows runs much longer than `period + timeout`.

## Closing the issue

This spec closes fclairamb/solidping-business#63 (its other points are handled as listed in
Source). The implementing PR body **must** carry a `Closes fclairamb/solidping-business#63` line.
When the spec is archived to `specs/done/`, verify the issue is closed and, if not, close it by
hand:

    gh issue close 63 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
