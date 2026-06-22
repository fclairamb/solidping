# Replace count-based confirmation/recovery with time-based periods

## Context

Today, "how many failures before we open an incident?" and "how many
successes before we auto-resolve?" are both **count-based** integers
on a check:

- `incidentThreshold` (default 3) — N consecutive failures before incident.
- `recoveryThreshold` (default 3, sometimes adapted by relapse count)
  — N consecutive successes before auto-resolve.

The count semantics couple alert/resolve latency to `period`. A user
who tightens a check's frequency from 60 s to 10 s silently shrinks
their alert window from 3 minutes to 30 seconds. Most operators model
these in *seconds*: "I want to be sure the failure stuck around for
2 minutes before paging me", not "after 3 ticks".

BetterStack ships exactly the time-based shape we want
([`wiki/research/alerting-patterns.md §1.1, §2.1`](../../docs/research/alerting-patterns.md)):

- `confirmation_period` — wall-clock seconds before opening an incident.
  Allowed UI presets: Immediate / 30 s / 1 min / 2 min / 3 min / 5 min /
  10 min. API takes a free integer.
- `recovery_period` — wall-clock seconds the check must stay UP before
  auto-resolve. **Any failure inside the window resets the counter.**

This spec replaces the count-based fields with the time-based ones.
We drop `incidentThreshold` and `recoveryThreshold` entirely. There is
no second knob, no migration period, no opt-in flag — the time-based
semantics are the only mode going forward.

## Why drop count-based instead of supporting both

Two knobs that compute the same decision split the support burden
without adding flexibility. Every code path that touches incident
opening or resolving has to read both fields, decide which one to
honor, and document the precedence. The dashboard would have to expose
both in the form. Customers would mix them and produce confusing
bugs ("it says I have 2 minutes confirmation but it pages after one
failure"). A clean cut is cheaper to ship, simpler to document, and
matches BetterStack's lived experience that wall-clock is what
operators actually want.

The migration is a one-shot rewrite at deploy time:
`incidentThreshold * period → confirmationPeriod` (in seconds), same
for recovery. Existing checks keep their effective alerting window;
the field name and unit change. After deploy, the threshold columns
go away.

## Goal

A check has two new fields, in seconds:

- `confirmationPeriod` (default 0 = "open immediately on the first
  failure", matching today's `incidentThreshold = 1` behavior).
- `recoveryPeriod` (default 0 = "resolve immediately on the first
  success").

The fields `incidentThreshold` and `recoveryThreshold` are removed
from the model, API, dashboard, CLI, and MCP surface in the same
release.

The state machine in `server/internal/handlers/incidents/service.go`
stops counting consecutive results and instead tracks two timestamps:

- `firstFailureAt` — set on the result that flips streak from 0 to 1.
  Cleared on the next success. The incident opens when
  `now - firstFailureAt >= confirmationPeriod`.
- `firstSuccessSinceFailureAt` — set on the result that follows a
  failure during an open incident. Cleared on any subsequent failure.
  The incident auto-resolves when
  `now - firstSuccessSinceFailureAt >= recoveryPeriod`.

The flap-reset rule is encoded by clearing the timestamp on the
opposite-sign result. A failure during the recovery window clears
`firstSuccessSinceFailureAt`, so the recovery clock restarts on the
next observed success.

## Approach

The `validating` state from spec
[`2026-05-08-01-validating-check-state.md`](2026-05-08-01-validating-check-state.md)
becomes load-bearing here: a check is `validating` while
`firstFailureAt` is set but `confirmationPeriod` hasn't elapsed.
The two specs work together — implement them in this order: 01 first
(introduces the state), then 02 (changes the trigger condition).

### State transitions

```
        check result UP                  check result DOWN
              ↓                                 ↓
[up]                                 [up]──────► [validating]
              ↑                       ↑              │
              │   ──────check result UP──────┘       │
              │                                      │
              └──────────check result UP             │
                            (after recoveryPeriod)   │
                                                     │
                              now-firstFailureAt >= confirmationPeriod
                                                     │
                                                     ▼
[down] ◄──────────check result DOWN──────────  [down]
   │                                              ▲
   │                                              │
   └────check result UP                  ─────────┘
        (recovery clock starts)         check result DOWN
                                        (recovery clock resets)
```

### Persisted fields

A new `incident_progress` table or, simpler, two columns on `checks`:

- `first_failure_at` (nullable timestamp) — set when a failure result
  arrives while the check is `up`. Cleared by a success.
- `first_success_since_failure_at` (nullable timestamp) — set when a
  success arrives during an active incident. Cleared by a failure
  during the recovery window.

Two columns on `checks` is the simpler choice. `incident_progress` would
be a separate row, accessed alongside the check on every result —
extra join, no benefit.

### Behavior change for already-failing checks at deploy time

Pre-existing data: a check sitting in `down` with `streak = 5` and the
old `incidentThreshold = 3`. After deploy, the streak is irrelevant.
The migration sets `confirmationPeriod = old_threshold * old_period`
and `recoveryPeriod = old_recovery_threshold * old_period`. Active
incidents stay open; the check status stays `down`. The recovery clock
starts fresh at the next observed success.

## Files to edit

### Migration

`server/internal/db/sqlite/migrations/` and the parallel postgres
migration:

```sql
-- Up migration: replace count thresholds with time-based seconds.
ALTER TABLE checks ADD COLUMN confirmation_period_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checks ADD COLUMN recovery_period_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checks ADD COLUMN first_failure_at TIMESTAMP NULL;
ALTER TABLE checks ADD COLUMN first_success_since_failure_at TIMESTAMP NULL;

-- Translate the old count-based thresholds to seconds. period is stored
-- as a duration; convert to seconds via the existing helper.
UPDATE checks
SET confirmation_period_seconds =
        CASE WHEN incident_threshold > 1
             THEN incident_threshold * (period / 1000000000)
             ELSE 0
        END,
    recovery_period_seconds =
        CASE WHEN recovery_threshold > 1
             THEN recovery_threshold * (period / 1000000000)
             ELSE 0
        END;

-- Drop the count fields.
ALTER TABLE checks DROP COLUMN incident_threshold;
ALTER TABLE checks DROP COLUMN recovery_threshold;
```

The down migration is the inverse: re-add the count columns, derive
counts via `ceil(seconds / period_seconds)`, drop the time columns.

### `server/internal/db/models/check.go`

- Drop `IncidentThreshold` and `RecoveryThreshold` from the struct.
- Add `ConfirmationPeriodSeconds`, `RecoveryPeriodSeconds` (both
  `int`).
- Add `FirstFailureAt`, `FirstSuccessSinceFailureAt` (both `*time.Time`).
- Update `NewCheck` and `CheckUpdate` accordingly.
- Update the JSON tags (`confirmationPeriodSeconds`,
  `recoveryPeriodSeconds`).

### `server/internal/handlers/incidents/service.go`

`ProcessCheckResult` (around line 75) becomes:

```go
// Pseudocode after the existing maintenance-window check.
isSuccess := /* same as today */
isFailure := /* same as today */

now := time.Now()

// Track validation/recovery clocks. These are set on the FIRST
// transition into the respective branch and cleared on the opposite
// signal. The flap-reset rule is encoded by clearing one when the
// other is set.
update := models.CheckUpdate{}
if isFailure {
    if check.FirstFailureAt == nil && incident == nil {
        update.FirstFailureAt = &now
    }
    if incident != nil {
        update.ClearFirstSuccessSinceFailureAt = true
    }
} else if isSuccess {
    if incident == nil {
        update.ClearFirstFailureAt = true
    }
    if incident != nil && check.FirstSuccessSinceFailureAt == nil {
        update.FirstSuccessSinceFailureAt = &now
    }
}

// Display-state derivation, integrating with spec 01 (validating).
var displayStatus models.CheckStatus
switch {
case incident != nil:
    displayStatus = models.CheckStatusDown
case check.FirstFailureAt != nil || (isFailure && update.FirstFailureAt != nil):
    displayStatus = models.CheckStatusValidating
default:
    displayStatus = models.CheckStatusUp
}

// Decide if it's time to open an incident.
shouldOpenIncident := false
if incident == nil && isFailure {
    firstFailure := check.FirstFailureAt
    if firstFailure == nil { firstFailure = update.FirstFailureAt }
    if firstFailure != nil && now.Sub(*firstFailure) >= time.Duration(check.ConfirmationPeriodSeconds)*time.Second {
        shouldOpenIncident = true
    }
}

// Decide if it's time to auto-resolve.
shouldResolveIncident := false
if incident != nil && isSuccess {
    firstSuccess := check.FirstSuccessSinceFailureAt
    if firstSuccess == nil { firstSuccess = update.FirstSuccessSinceFailureAt }
    if firstSuccess != nil && now.Sub(*firstSuccess) >= time.Duration(check.RecoveryPeriodSeconds)*time.Second {
        shouldResolveIncident = true
    }
}
```

The existing `handleFailure` / `handleSuccess` branches collapse into
the open/resolve decisions above. The streak/threshold logic
(`StatusStreak`, `IncidentThreshold`, `effectiveRecoveryThreshold`,
`tryReopenIncident`'s relapse-based threshold adaptation) is removed
entirely.

`StatusStreak` itself stays in the model and continues to be updated
for *display* purposes ("the API has been up for 247 ticks") — but
no decision keys on it.

### Group incidents

The same time-clock logic applies per-member, with one wrinkle: the
group-incident open trigger today fires when *any* enabled member is
failing past its threshold. After this spec, it fires when any member's
`firstFailureAt` is older than its `confirmationPeriod`. Group resolve
fires when *all* failing members' `firstSuccessSinceFailureAt` is
older than their respective `recoveryPeriod`.

### `server/internal/handlers/checks/service.go`

Validate the new fields on create/update:
- `confirmationPeriodSeconds >= 0`, max 86400 (1 day).
- `recoveryPeriodSeconds >= 0`, max 86400.

Reject the legacy `incidentThreshold` / `recoveryThreshold` keys with
a clear validation error pointing at the new field names — gives
clients still on the old payload shape an actionable error rather than
silently ignoring them.

### Frontend (`web/dash0/`)

- `src/api/hooks.ts`: rename `incidentThreshold` / `recoveryThreshold`
  in `Check` interface to `confirmationPeriodSeconds` /
  `recoveryPeriodSeconds`.
- `src/components/shared/check-form.tsx`: replace the two integer
  fields with two duration pickers presenting the BetterStack-style
  presets:
  - Immediate (0)
  - 30 s
  - 1 min
  - 2 min
  - 3 min
  - 5 min
  - 10 min
  - Custom (free integer in seconds)
- `src/locales/{en,fr,de,es}/checks.json`: add labels:
  - `form.confirmationPeriod`, `form.confirmationPeriodHelp`
  - `form.recoveryPeriod`, `form.recoveryPeriodHelp`
  - `form.confirmationImmediate` ("Immediate"), preset labels.
- Drop the old `form.incidentThreshold` / `form.recoveryThreshold`
  keys.

### CLI

`server/internal/cli` — any place that prints or accepts the
threshold field. Replace with the new fields.

### MCP

`server/internal/mcp/tools_checks.go` — `createCheckDef`,
`updateCheckDef` schema changes. Rename in `propConfirmationPeriod`,
`propRecoveryPeriod` (or fold under `propPeriod` cluster). LLM
descriptions explain wall-clock semantics so the model doesn't try to
pass count-based numbers.

### Docs

- `wiki/api-specification.md`: replace `incidentThreshold` /
  `recoveryThreshold` mentions with the new field names. Add a note
  in the Conventions section pointing at this spec for the rename
  history.
- `wiki/conventions/checker-config.md`: add a "Confirmation & recovery"
  section explaining the wall-clock model and the flap-reset rule.
- `wiki/features/notifications-and-escalation.md`: update the section
  on suppression to clarify the new validating-state interaction.

## Out of scope

- Per-region quorum (multi-region confirmation). Different concept;
  see [`alerting-patterns.md §1.2`](../../docs/research/alerting-patterns.md).
- Distinct-from-recovery `degraded` state when a percentage of regions
  is failing.
- A "warning" threshold for response times (latency-based alerting).
  Separate concern.

## Verification

1. `make build-backend lint-back` clean.
2. `make test`: existing tests update to the new fields; add new
   tests covering the timestamp transitions:
   - confirm-period 60 s, period 10 s, two failures within 30 s →
     no incident yet.
   - confirm-period 60 s, two failures spanning 65 s → incident
     opens on the second failure (timestamp comparison).
   - recovery-period 60 s, success at t=0 then failure at t=30 →
     `firstSuccessSinceFailureAt` cleared, incident remains open.
   - confirm-period 0, single failure → incident opens immediately
     (matches default behavior).
3. Migration smoke: load a fixture DB with a check that has
   `incidentThreshold=3, period=60s`. Run migrations. Confirm the
   check now has `confirmationPeriodSeconds=180, recoveryPeriodSeconds=...`
   and the threshold columns are gone.
4. Frontend: create a check via the dashboard, set confirmation
   period to "2 min" via the preset dropdown, verify the API payload
   carries `confirmationPeriodSeconds: 120`.
5. MCP: `create_check` tool accepts `confirmationPeriodSeconds`,
   rejects `incidentThreshold` with a clear error.

## Implementation Plan

1. Migration: add the four new columns, translate from old, drop
   the old columns. Both sqlite and postgres branches.
2. Model edits: rename fields, update factories, update update
   structs.
3. State machine rewrite in `incidents/service.go`. Remove the
   `IncidentThreshold` / `RecoveryThreshold` reads and the streak-
   based decisions. Add the timestamp-based open/resolve checks.
4. Group-incident parallel rewrite (`handleGroupFailure`,
   `handleGroupSuccess`).
5. Backend tests: replace existing threshold-based tests with
   timestamp-based equivalents.
6. Validation in `checks/service.go`: range-check the new fields,
   reject the legacy field names with a helpful error.
7. Frontend: rename in `api/hooks.ts`, replace the form fields with
   duration pickers, update locales.
8. CLI + MCP migrations.
9. Docs: api spec, checker-config conventions, notifications-and-
   escalation page.
10. Completeness audit, archive, merge.

The DB migration is the only irreversible step — the rollback exists
but loses the time fidelity by re-coarsening to counts. Take a backup
before running in production.
