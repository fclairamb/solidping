---
model: sonnet
effort: high
---

# Flap state is invisible and the reopen cooldown hides its actual window — expose both, and shorten the Flapping copy

## Problem

The adaptive-recovery flapping layer (spec 2026-06-30-07) works, but nothing about
it is observable from the outside:

- `checks.flap_count` / `checks.last_outage_at` exist on the model
  (`server/internal/db/models/check.go:149-150`) but are not serialized in
  `CheckResponse` (`server/internal/handlers/checks/service.go:2868-2893`), so the
  API returns nothing and the dashboard can show nothing.
- The optional `incidents.flap_level` from spec 2026-06-30-07 was never
  implemented — `grep -rn "flap_level" server/` finds nothing. An incident gives
  no hint that it opened at an escalated flap level.
- The only visible effect of the 6h window is "incidents stay open ~30 min", which
  reads as *slow recovery*, not as *backoff at work*. This caused a real
  "the 6h flapping window doesn't seem to be applied" investigation today (org
  `stonal`, check `http-laplacedelimmobilier-com`, incidents #362–#365): the
  window was applying correctly the whole time.

Separately, the **Reopen Cooldown Multiplier** field in the check form asks for a
bare `N` and leaves the actual window as prose ("Window = N × check interval, min
2 min, max 30 min"). A user who sets `60` on a 1-minute check silently gets a
30-minute window (the clamp in `calculateCooldown`,
`server/internal/handlers/incidents/service.go:1183-1207`) — nothing tells them.
The Flapping Window field already renders a computed "= 6 h" hint
(`web/dash0/src/components/shared/check-form.tsx:1434-1436`); the reopen cooldown
renders none.

Finally, the four Flapping field descriptions are long (the reopen-cooldown one is
three sentences) — the numeric mechanics belong in the computed hints, not the
prose.

## Proposal

### 1. Expose flap state (API + dashboard)

**Backend — shared computation first.** `effectiveRecoveryPeriod`
(`incidents/service.go:1005-1036`) and the lazy-reset rule in `bumpFlap`
(`incidents/service.go:1045-1056`) are pure functions over `models.Check`. Lift
them onto the model (e.g. `check.EffectiveRecoveryPeriod(now)` and
`check.EffectiveFlapCount(now)`) so the checks handler can reuse them without an
import cycle; the incidents package delegates to the model methods. Pure refactor
— existing incident tests (`recovery_clock_test.go`, `service_test.go`) must pass
unchanged.

**The lazy-reset trap (do not skip this).** `flap_count` only resets at the *next*
outage onset — a check whose last outage was 12h ago can still hold
`flap_count = 4` in the row. The API must expose **effective** values, not raw
columns: effective flap count is 0 when `last_outage_at` is nil or older than the
flapping window.

**`CheckResponse`** (`checks/service.go:2868`) gains an optional block, omitted
when the feature is off or no flap state has accumulated:

```json
"flapState": {
  "flapCount": 1,
  "lastOutageAt": "2026-08-24T09:12:00Z",
  "effectiveRecoveryPeriodSeconds": 1800
}
```

**Incidents.** Add `incidents.flap_level int notnull default 0`, recorded from
`check.FlapCount` right after `recordFlap` in `createIncident`
(`incidents/service.go:1082`) and in `reopenIncident` (~`:1264`). Expose it as
`flapLevel` (omitempty on 0) in `IncidentResponse`
(`incidents/service.go:2144`). Migration: this release's consolidated
`NNN_v0_18_0` file per the repo rule (both dialects, both directions) — beware
the applied-migration pitfall: local dev DBs that already ran v0_18_0 need a
reset or `solidping migrate repair` (guard runs `warn` in dev).

**dash0.**
- Check detail page (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`):
  when `flapState` is present, show a line such as
  *"Flapping: 2nd outage within 6h — required recovery ×2 = 30 min"*.
- Incidents list + incident detail: a small "flapping ×N" badge when
  `flapLevel > 0`. Start from the design-reference page for the badge primitive
  (mandatory per CLAUDE.md).

### 2. Show the reopen cooldown as actual time

In `check-form.tsx` under the Reopen Cooldown Multiplier input (~`:1426-1428`),
render a computed hint mirroring the flapping window's `= 6 h`, using the form's
current period and the same clamp as `calculateCooldown` (min 2 min, max 30 min):

- normal: `= 5 min`
- clamped: `= 30 min (capped, from 60 min)` — also applies to the 2-min floor
- `0`: `off — always opens a new incident`

Reuse the existing `formatDuration`. Also update `flappingSummary`
(`check-form.tsx:851`) to show the actual window (`cooldown 30 min`) instead of
the bare `cooldown ×60`.

### 3. Shorten the Flapping descriptions

Rewrite to one short sentence each, moving the numeric mechanics into the
computed hints (`web/dash0/src/locales/en/checks.json:188-212` — keys
`flappingHelp`, `reopenCooldownHelp`, `flappingWindowHelp`,
`flapBackoffFactorHelp`, `maxRecoveryMultiplierHelp`). Suggested en copy:

- `flappingHelp`: "De-duplicate short blips and require longer stability before
  resolving a check that keeps flapping."
- `reopenCooldownHelp`: "A relapse inside this window reattaches to the previous
  incident instead of paging again."
- `flappingWindowHelp`: "Outages within this window escalate the required
  recovery time. 0 = off."
- `flapBackoffFactorHelp`: "Each flap multiplies the required recovery by this.
  1 = off."
- `maxRecoveryMultiplierHelp`: "Required recovery never exceeds this × the base
  recovery period."

All four locales (`de`, `en`, `es`, `fr`) get every new/changed key —
`bun run test:unit` enforces locale-key parity and must be run.

## QA

- Backend: table-driven tests for `EffectiveFlapCount` (window lapsed → 0),
  `flapState` presence/absence in `CheckResponse`, and `flap_level` recorded on
  create *and* reopen. Prove the refactor is behavior-neutral: the existing
  incidents suite passes without edits.
- Frontend: unit tests for the clamp hint (normal / capped / floor / 0), locale
  parity via `bun run test:unit`; a Playwright assertion that the reopen-cooldown
  hint renders in the check form.

## Implementation Plan

1. **Model refactor (pure).** Add to `models.Check`:
   `FlappingWindowElapsed(now)` (the shared lazy-reset predicate),
   `EffectiveFlapCount(now)` (lazy-reset-aware read of `FlapCount`),
   `EffectiveRecoveryPeriod()` (today's raw-`FlapCount` math, unchanged
   behavior — used inside an active incident's own lifecycle, where
   `FlapCount` is always fresh) and `EffectiveRecoveryPeriodAt(now)` (same
   math driven by `EffectiveFlapCount(now)` — used for external state
   exposure). `incidents/service.go`'s `bumpFlap`, `recoveryElapsed`, and
   `effectiveRecoveryPeriodSeconds` delegate to these; the old
   `effectiveRecoveryPeriod` free function and `recoveryHardCeiling` const
   are removed. `export_test.go`'s `EffectiveRecoveryPeriodForTest` keeps its
   signature and forwards to `check.EffectiveRecoveryPeriod()`.
   `recovery_clock_test.go` / `service_test.go` are not touched. Verified
   green as-is before continuing.
2. **`incidents.flap_level` column + model + `IncidentResponse`.** Add
   `FlapLevel int` to `models.Incident` and `*int` to `IncidentUpdate`; wire
   the `flap_level` column into `applyIncidentSetFields` (both dialects,
   reusing the existing `*int` case). Set `incident.FlapLevel =
   check.FlapCount` right after `recordFlap` in `createIncident`, and set it
   via `IncidentUpdate.FlapLevel` in `reopenIncident`. Expose `flapLevel`
   (omitempty) on `IncidentResponse` / `incidentToResponse`.
3. **Migration.** Add `incidents.flap_level int not null default 0` as a new
   trailing SECTION in `015_v0_18_0.up.sql` / `.down.sql`, both dialects.
4. **`CheckResponse.flapState`.** Add `FlapStateResponse` + `FlapState
   *FlapStateResponse` to `checks/service.go`'s `CheckResponse`, populated in
   `convertCheckToResponse` via `check.EffectiveRecoveryPeriodAt(time.Now())`
   / `check.EffectiveFlapCount(time.Now())`, omitted when the effective flap
   count is 0 (covers both "feature off" and "no flap state accumulated").
5. **OpenAPI.** Document `flapState` on the check schema and `flapLevel` on
   the incident schema in `openapi.yaml`.
6. **Backend tests.** Table-driven tests for `EffectiveFlapCount` (including
   window-lapsed → 0) and `EffectiveRecoveryPeriodAt` on `models.Check`;
   `flapState` presence/absence + shape in `CheckResponse`; `flap_level`
   recorded on create and reopen in the incidents service tests.
7. **dash0: check detail flapping line.** In
   `checks.$checkUid.index.tsx`, render a one-line flapping summary when
   `flapState` is present.
8. **dash0: incident flapping badge.** Add a small badge primitive to the
   design reference, then use it in the incidents list and incident detail
   when `flapLevel > 0`.
9. **dash0: reopen-cooldown computed hint + `flappingSummary`.** In
   `check-form.tsx`, add a `formatReopenCooldown`-style helper mirroring the
   flapping-window hint (normal / capped / floor / 0 cases), render it under
   the Reopen Cooldown Multiplier input, and update `flappingSummary` to show
   the resolved window instead of the bare multiplier.
10. **Copy.** Shorten the five help strings in `en`, then translate
    genuinely into `de`/`es`/`fr`; verify key-set parity across all four
    locale files.
11. **Frontend tests.** Vitest unit tests for the cooldown-hint helper (all
    four cases) and locale parity; a Playwright assertion that the hint
    renders in the check form.
12. **QA gate.** `make build-backend lint-back test`, `make build-dash0`,
    `bun run lint`, `bun run test:unit`; fix any findings.
