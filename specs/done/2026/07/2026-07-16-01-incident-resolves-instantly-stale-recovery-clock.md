---
model: opus
effort: high
---

# An incident can auto-resolve on its first success, ignoring the recovery period, because a stale recovery clock survives from the previous incident

## Problem

Observed on the `secnum-abyla-cup` check in org `acmetech`
(https://solidping.k8xp.com/dash0/orgs/acmetech/checks/secnum-abyla-cup),
configured with **confirmation period 30s** and **recovery period 300s**
(≈10 checks at the 30s interval):

| Time | Event |
|---|---|
| 1:32:22 PM | `incident.created` |
| 1:32:37 PM | `incident.resolved` ← **15 seconds later** |
| 1:34:52 PM | `incident.reopened` |
| 1:39:52 PM | `incident.escalated` |
| 1:45:29 PM | `incident.acknowledged` (User) |

The incident resolved 15 seconds after opening — on its first success — where
the 300s recovery period should have required ~10 consecutive successes. It
then reopened 2m15s later, confirming the check was still genuinely unhealthy.

### Root cause

`check.first_success_since_failure_at` is the recovery clock. It is **never
cleared when an incident opens, resolves, or reopens** — so a value left behind
by a *previous* incident makes the *next* incident resolve on its first success.

`deriveIncidentClocks` — [server/internal/handlers/incidents/service.go:263-288](server/internal/handlers/incidents/service.go#L263-L288):

```go
case isFailure && activeIncident == nil:
    // arms the confirmation clock only — stale success clock survives
    if check.FirstFailureAt == nil {
        t := now
        out.FirstFailureAt = &t
    }
case isFailure && activeIncident != nil:
    if check.FirstSuccessSinceFailureAt != nil {
        out.ClearFirstSuccessSinceFailureAt = true   // ← the ONLY clear site
    }
case isSuccess && activeIncident == nil:
    if check.FirstFailureAt != nil {
        out.ClearFirstFailureAt = true               // clears failure clock, not success clock
    }
case isSuccess && activeIncident != nil:
    if check.FirstSuccessSinceFailureAt == nil {     // ← no-op when a stale value is present
        t := now
        out.FirstSuccessSinceFailureAt = &t
    }
```

`ClearFirstSuccessSinceFailureAt = true` is set in **exactly one place**
(`service.go:274`), gated on `isFailure && activeIncident != nil`. Verified by
grep across the tree: the only other non-test occurrences are the DB writers
([postgres.go:2644-2647](server/internal/db/postgres/postgres.go#L2644-L2647),
[sqlite.go:2616-2619](server/internal/db/sqlite/sqlite.go#L2616-L2619)). None of
`createIncident` ([service.go:608](server/internal/handlers/incidents/service.go#L608)),
`resolveIncident` ([service.go:641](server/internal/handlers/incidents/service.go#L641)),
or `reopenIncident` ([service.go:770](server/internal/handlers/incidents/service.go#L770))
clears it.

The resolve path reads that clock unscoped —
[service.go:512-518](server/internal/handlers/incidents/service.go#L512-L518):

```go
func recoveryElapsed(check *models.Check, now time.Time) bool {
	if check.FirstSuccessSinceFailureAt == nil {
		return false
	}
	return !now.Before(check.FirstSuccessSinceFailureAt.Add(effectiveRecoveryPeriod(check)))
}
```

Nothing ties `FirstSuccessSinceFailureAt` to the incident being resolved — no
comparison against `incident.StartedAt` or `LastReopenedAt`.

### Failure sequence

1. Incident #1 is open; a success arrives → `first_success_since_failure_at = T0`.
2. Stability elapses → incident #1 resolves. **The clock is not cleared; it stays `T0`.**
3. Check keeps succeeding — `isSuccess && activeIncident == nil` clears only
   `FirstFailureAt`. Stale `T0` persists indefinitely.
4. Later the check fails → `isFailure && activeIncident == nil` arms
   `FirstFailureAt`; stale `T0` still survives.
5. Confirmation elapses → **incident #2 created**.
6. Next poll succeeds → `activeIncident = #2`, but line 284 sees
   `FirstSuccessSinceFailureAt != nil` and does **not** re-arm it to `now`.
7. `recoveryElapsed` compares `now` against an ancient `T0 + 300s` → **true** →
   incident #2 resolves on its first success.

The asymmetry is the bug: the failure clock is reset by the opposite signal
(every success), the success clock is not.

### Why it's intermittent

If any additional failure lands while the new incident is open *before* the
first success, `service.go:271-275` fires and clears the stale clock, and
recovery behaves correctly. The bug only manifests when the incident-opening
failure is immediately followed by a success — precisely the single-blip case
seen above.

### Open question to verify first

The dev deployment at `solidping.k8xp.com` pins its own image tag and may predate
the recent fix in
`specs/done/2026/07/2026-07-15-04-check-edit-confirmation-recovery-period-not-saved.md`
(commit `d65afef0`), where the check-edit form silently dropped
`recoveryPeriodSeconds` on save. **Before implementing, confirm the check's
actually-stored `recovery_period_seconds` is 300 and not 0** — a stored `0`
makes `recoveryElapsed` resolve on first success too, and would be a second,
independent path to the same symptom. Note `NewCheck`
([models/check.go:156-158](server/internal/db/models/check.go#L156-L158))
defaults to 120s while the column default is `0`
([check.go:87](server/internal/db/models/check.go#L87)), so rows created outside
`NewCheck` (imports, migrations, direct SQL) get resolve-immediately semantics.
The stale-clock bug is real and worth fixing regardless of what this check's
stored value turns out to be.

## Proposal

### 1. Clear the recovery clock when an incident opens

Make the clock symmetric — clear `FirstSuccessSinceFailureAt` in the
`isFailure && activeIncident == nil` branch of `deriveIncidentClocks`
([service.go:264-270](server/internal/handlers/incidents/service.go#L264-L270)).
This covers both `createIncident` and `reopenIncident`, since both are reached
from that branch:

```go
case isFailure && activeIncident == nil:
    if check.FirstFailureAt == nil {
        t := now
        out.FirstFailureAt = &t
    }
    if check.FirstSuccessSinceFailureAt != nil {
        out.ClearFirstSuccessSinceFailureAt = true
    }
```

### 2. Defense in depth: never trust a clock older than the incident

Have `recoveryElapsed` ignore a `FirstSuccessSinceFailureAt` that predates the
incident's `StartedAt` / `LastReopenedAt`, so the recovery clock can never be
older than the incident it is resolving. This requires threading the incident
into `recoveryElapsed` (its caller `handleSuccess`
[service.go:487](server/internal/handlers/incidents/service.go#L487) already has
it). This makes the invariant structural rather than dependent on every
transition remembering to clear.

Apply the same treatment to the group path, which calls the identical
`recoveryElapsed(check, ...)` —
[service.go:1165](server/internal/handlers/incidents/service.go#L1165),
`resolveGroupIncident` [service.go:1206](server/internal/handlers/incidents/service.go#L1206).

### 3. Consider a backfill

Existing rows may carry a stale `first_success_since_failure_at` with no open
incident. Decide whether to clear it via migration for checks with no active
incident, or let it self-heal (it will, on the next incident open, once fix #1
lands). Lean toward self-heal unless the implementer finds a reason otherwise —
but call it out explicitly rather than leaving it implicit.

### 4. Test coverage

The gap that let this ship: `TestRecoveryFlapResetsClock`
([validating_test.go:191](server/internal/handlers/incidents/validating_test.go#L191))
covers failure-during-recovery clearing the clock, and `resolve_test.go` covers
only *manual* resolve. There is **no test** for the open → success → resolve →
open again → success sequence.

Add table-driven coverage for:
- **The reported regression**: incident opens → resolves after recovery →
  reopens later → first success must **not** resolve it; only `now >= firstSuccess + recovery` does.
- Stale clock predating the incident is ignored (guard #2 directly).
- Existing single-incident recovery behavior does not regress.
- The same sequence on the group-incident path.
- `recovery_period_seconds = 0` still means resolve-immediately (intentional).

Use `testify/require` and `t.Parallel()` per `server/CLAUDE.md`. The service has
an injectable clock (`s.clock.Now()`), so these are unit-testable without
wall-clock sleeps — do not introduce timing-dependent tests
(see the wall-clock flake spec `2026-07-14-08`).

## Implementation Plan

### Step 0 — the open question is not verifiable here

The spec asks to confirm the deployed `secnum-abyla-cup` check's stored
`recovery_period_seconds`. The remote dev database is not reachable from this
task and querying it is out of scope. The stale-clock bug is real and provable
from the code alone, so it is fixed regardless; the stored-value question is
carried forward as unverified.

### Step 1 — `incidentClockFloor` helper (`service.go`)

Add a small pure helper next to `recoveryElapsed`:

```go
func incidentClockFloor(incident *models.Incident) time.Time
```

Returns the incident's current **onset**: `LastReopenedAt` when it is set and
after `StartedAt`, otherwise `StartedAt`. Returns the zero time for a nil
incident, so every comparison against it is vacuously satisfied and the
no-incident paths keep today's behavior.

### Step 2 — clear the recovery clock when an incident opens (Proposal #1)

In `deriveIncidentClocks`, the `isFailure && activeIncident == nil` branch also
sets `out.ClearFirstSuccessSinceFailureAt = true` when
`check.FirstSuccessSinceFailureAt != nil`. This makes the clock symmetric with
`FirstFailureAt` and covers both `createIncident` and `reopenIncident`, which
are both reached from that branch.

### Step 3 — re-arm a stale clock on success (write-side guard)

In the `isSuccess && activeIncident != nil` branch, arm the clock to `now` not
only when it is `nil` but also when the existing value **predates**
`incidentClockFloor(activeIncident)`. This is what makes a row that already
carries a stale clock at deploy time self-heal even if its incident is already
open — the reason no backfill migration is needed (see Step 6).

### Step 4 — `recoveryElapsed` ignores a pre-incident clock (Proposal #2, read-side guard)

Thread the incident through: `recoveryElapsed(check, incident, now)`. It returns
`false` when `FirstSuccessSinceFailureAt` is `nil` **or** predates
`incidentClockFloor(incident)`. Update both call sites — `handleSuccess`
(per-check) and `handleGroupSuccess` (group); both already hold the incident.
Update `RecoveryElapsedForTest` in `export_test.go` to match.

**Why both a write-side and a read-side guard, and why re-arm rather than only
reject:** an incident that is *already open* when this code ships carries a
non-nil clock that Step 2's clear-on-open never ran for. The read-side guard
rejects that clock forever, and the old write side (`if ... == nil`) would never
replace it — so a read-side reject *alone* would wedge those incidents open
until a failure happened to land and clear the clock. Step 3's re-arm is what
resolves that, and is precisely what makes the no-backfill decision in Step 6
sound.

Note that `incident.StartedAt` derives from `result.PeriodStart` while the clock
comes from `s.clock.Now()`. This is **not** a clock-skew hazard: `PeriodStart` is
never worker-supplied (`SubmitResultRequest` carries no timestamp), and every
production path stamps it with `time.Now()` in the same process that then calls
`ProcessCheckResult` (`workers/service.go`, `checkworker/worker.go`,
`backend/direct.go`, `heartbeat/service.go`, `emailcheck/handler.go`). For
reopens the floor is `LastReopenedAt = s.clock.Now()` — the same clock as the
recovery clock. There is no clock boundary between the two values.

### Step 5 — tests (`server/internal/handlers/incidents/`)

New `recovery_clock_test.go`, built on the existing `validatingSetup` (in-memory
sqlite, real `ProcessCheckResult`) plus a clock-free unit test for the guard.
No sleeps, no wall-clock dependence.

- **The reported regression**: open → success → resolve after recovery → fail
  again (reopen) → the first success must **not** resolve; only
  `now >= firstSuccess + recovery` does.
- Incident open clears a pre-existing stale `first_success_since_failure_at`
  (Step 2, asserted on the persisted row).
- A stale clock predating the incident is re-armed on success rather than
  honored (Step 3).
- `recoveryElapsed` rejects a clock older than `StartedAt` / `LastReopenedAt`,
  and a nil incident keeps legacy behavior (Step 4, via
  `RecoveryElapsedForTest`).
- Existing single-incident recovery does not regress.
- Same open → resolve → reopen → success sequence on the group path.
- `recovery_period_seconds = 0` still resolves immediately (intentional).

### Step 6 — backfill decision: **no migration, self-heal**

Follow the spec's lean. Every stale row heals on its own without a migration:
a check with no open incident heals on its next incident-opening failure
(Step 2), and a check whose incident is *already* open at deploy time heals on
its next success (Step 3). A migration would touch both
`postgres/migrations/` and `sqlite/migrations/` to clear a column that the code
now fixes on the very next result — cost without benefit. Decision recorded
here explicitly rather than left implicit.

### QA

`make build-backend lint-back test` — backend only; this spec touches no
frontend surface.
