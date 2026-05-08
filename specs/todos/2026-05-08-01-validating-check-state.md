# Add a `validating` check state for confirmation windows

## Context

Today a check's `status` flips from `up` straight to `down` the moment its
streak threshold is met (`server/internal/handlers/incidents/service.go:183`).
There's no externally visible "we noticed a failure but haven't opened
an incident yet" state. The dashboard shows the check as still UP right
up to the moment the incident opens, then flips to DOWN.

This is the source of the most common operator-confused-by-monitoring
question: *"my service is failing, why isn't an alert firing?"* They
look at the dashboard, see a green check, and assume monitoring is
broken. The real answer — "we're inside the confirmation window" —
isn't surfaced.

BetterStack solves this with a transient `validating` state. The
status enum is `up | down | validating | paused | pending | maintenance`.
Once a failure is observed but the confirmation hasn't completed, the
check shows `validating` in the dashboard list, on detail pages, and in
the API response. When confirmation either fires (incident opens) or
clears (failure was transient), the state moves to `down` or `up`
accordingly. See [`docs/research/alerting-patterns.md §1.3`](../../docs/research/alerting-patterns.md).

## Goal

Make the in-confirmation state visible. A check that has observed at
least one failure but hasn't yet crossed the confirmation threshold
shows `validating` everywhere `up`/`down` shows today.

## Approach

A new `CheckStatus` enum value, `CheckStatusValidating`, joins the
existing five (`created`, `up`, `down`, `degraded`, plus the implicit
display states). The state machine in
`server/internal/handlers/incidents/service.go` decides between `up`,
`down`, and `validating` after every check result:

- **First failure observed** (StatusStreak transitions to 1 with a
  failure result) → `validating`. Stay there until either:
  - The streak reaches `incidentThreshold` → `down` (incident opens).
    This is a *separate* lifecycle event from "validating started";
    the incident-open event still fires on the same edge it does today.
  - A successful result resets the streak to 0 → `up` (the failure
    didn't persist, no incident opens, no notification fires).
- **Already in `down`** → unchanged. `validating` is *only* the
  intermediate state on the up-side of the threshold.

The `validating` state never triggers notifications. It's purely a
display state. No channels fire for "entering validating", no events
land in the audit log for "left validating" — those are dashboard-only
transitions. Existing notifications continue to fire only when the
incident *opens* (i.e. `down` is reached).

### Why not introduce it on the recovery side too

Tempting to mirror: "in `down`, observed first success → `recovering`".
We're skipping that for v1. Rationale: the operator who's already
been paged knows the incident is open and is watching for resolve.
"Recovering" doesn't add information they don't already have. If the
follow-up time-based recovery period spec
([`2026-05-08-02-time-based-confirmation-and-recovery-periods.md`](2026-05-08-02-time-based-confirmation-and-recovery-periods.md))
lands first, the *resolve* side of the asymmetry will go away naturally
because resolve becomes wall-clock, not streak-counted.

## Files to edit

### `server/internal/db/models/check.go`

Add the new enum value next to the existing ones:

```go
const (
    CheckStatusCreated    CheckStatus = 1
    CheckStatusUp         CheckStatus = 3
    CheckStatusDown       CheckStatus = 4
    CheckStatusValidating CheckStatus = 5  // NEW
    CheckStatusDegraded   CheckStatus = 7
)
```

The numeric value 5 fills the gap left between `CheckStatusDown` (4)
and `CheckStatusDegraded` (7). The enum is persisted in the DB as the
integer; old rows with statuses 1/3/4/7 keep working unchanged.

### `server/internal/handlers/incidents/service.go`

`ProcessCheckResult` needs a new branch around the existing
`newStatus` calculation (~lines 107–112). Pseudocode:

```go
// New: validating-state derivation. Computes the externally-visible
// status after this result. Different from the incident-state machine
// below, which still keys on isFailure/isSuccess.
var displayStatus models.CheckStatus
switch {
case isSuccess:
    displayStatus = models.CheckStatusUp
case isFailure && (incident != nil || newStreak >= check.IncidentThreshold):
    // Already in an incident, or this result crosses the threshold —
    // visible state is `down`, same as today.
    displayStatus = models.CheckStatusDown
case isFailure:
    // First failure(s) inside the confirmation window — display
    // validating until either the streak crosses or success resets it.
    displayStatus = models.CheckStatusValidating
}

// Replace the existing `newStatus = ... CheckStatusUp/Down` lines.
```

The `incident-open / incident-resolve` decisions further down keep
their existing logic — they don't gate on `validating`.

### `web/dash0/src/api/hooks.ts`

Add `"validating"` to the `CheckStatus` TypeScript union (or whichever
type alias mirrors the backend enum). Search for sites that render
status colors and add a yellow/amber pill for the new value
distinct from `up` (green) and `down` (red).

### Status indicators

- `web/dash0/src/components/checks/*` — wherever a status dot or pill
  renders.
- `web/dash0/src/routes/orgs/$org/checks.index.tsx` — the row badge
  (currently a green/red dot for up/down).
- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` — the
  detail header.
- `web/dash0/src/components/dashboard/*` — the org dashboard tiles.

Use the existing `bg-yellow-500/10 text-yellow-500` palette already
applied to the `timeout` result status — same visual weight as
"degraded but not failing".

### `web/dash0/src/locales/{en,fr,de,es}/checks.json`

Add a `status.validating` key per locale:
- en: `"Validating"`
- fr: `"En validation"`
- de: `"Wird geprüft"`
- es: `"Validando"`

### Tests

Add `TestProcessCheckResultEntersValidatingOnFirstFailure`:

- Create a check with `IncidentThreshold = 3`.
- Submit one failure. Assert `check.Status == CheckStatusValidating`,
  no incident opened.
- Submit a second failure. Assert still `validating`, still no
  incident.
- Submit the third failure. Assert `check.Status == CheckStatusDown`
  and an incident exists.
- Reset (new check). Submit a failure (status: validating), then a
  success. Assert status returns to `up`, no incident, no event row.

## Out of scope

- Adding a `validating` notification (intentional — see Approach).
- Mirror state on the recovery side (deferred to spec 02).
- Per-region partial-down state ("3 of 5 regions failing"). Multi-region
  quorum is a separate concept tracked in
  [`docs/research/alerting-patterns.md §1.2`](../../docs/research/alerting-patterns.md).
- Surfacing `validating` in events feed, status pages, or external
  webhooks. Display-only first; integrations later.

## Verification

1. `make build-backend lint-back test` clean.
2. `make build-dash0 lint-dash` clean.
3. Manual smoke against `make dev-test`:
   - Create a check pointing at a 500-returning URL with
     `incidentThreshold = 3`.
   - Watch the dashboard list. Within 1–2 check intervals the status
     pill should transition from green ("up") to yellow ("Validating"),
     then to red ("Down") on the third failure.
   - Repair the URL. Watch the next successful result return the status
     to green ("up") if still in validating, or trigger normal incident
     resolution if it had reached `down`.
4. Translation parity: switch FR / DE / ES; confirm the validating pill
   uses the localized label.

## Implementation Plan

1. Add `CheckStatusValidating` constant to the model + a migration-free
   note (the DB column is already an int; nothing to migrate).
2. Edit `ProcessCheckResult` in `incidents/service.go` to derive
   `displayStatus` separately from the incident state machine. Keep all
   existing incident-side branches unchanged.
3. Add backend tests (table-driven: streak progression scenarios).
4. Update the TypeScript `CheckStatus` union and the status-pill /
   status-dot components.
5. Add the `status.validating` locale key in en/fr/de/es (single commit).
6. Run QA, completeness audit, archive, merge.
