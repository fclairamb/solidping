---
model: sonnet
effort: medium
---

# Expose the multi-region `regionSpread` override in the check edit UI

## Problem

Spec `2026-07-20-05-multi-region-full-period-and-offset` (done) made every
region of a multi-region check run at the full check period, staggered by an
inter-region offset that defaults to `period / regions` and can be overridden
per check via the API field `regionSpread`
([check.go:97](server/internal/db/models/check.go:97), validation
`0 ≤ regionSpread < period`, `null` reverts to the automatic default —
see `TestRegionSpreadValidation` in
[reconcile_test.go:367](server/internal/handlers/checks/reconcile_test.go:367)).

The dashboard shipped only the informational copy — the regions hint
("Each selected region runs the check every {{period}}",
[checks.json:128](web/dash0/src/locales/en/checks.json:128)) and the usage-page
multi-region note ([org.json:34](web/dash0/src/locales/en/org.json:34)) — but
**no input**: there is no way to view or set the spread from the UI. Users
who want near-simultaneous sampling (e.g. a 1s spread for comparative
cross-region latency) must `PATCH` the check by hand.

## Proposal

Add an optional **"Region spread"** control to the check form (new + edit),
in the regions section:

- **Visibility**: only rendered when ≥ 2 regions are selected. Presented as
  a secondary/advanced field so the default path stays zero-config.
- **Input**: reuse the same duration-input primitive the form already uses
  for the check `period` — start from the design reference
  ([design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx))
  and add the pattern there if it is missing.
- **Default display**: when unset, show the computed automatic value as
  placeholder/help text — e.g. "Automatic: 20s (period / 3 regions)" for a
  1-minute check on 3 regions — with a hint that leaving it empty keeps
  automatic spreading.
- **Save semantics**: empty input ⇒ send `regionSpread: null` (clears the
  override; backend reverts to the automatic default). Never send `0` for
  "unset" — `0` is a valid explicit value (all regions fire on the same
  aligned second).
- **Validation**: client-side mirror of the backend bound
  `0 ≤ spread < period`, plus graceful rendering of the backend
  `VALIDATION_ERROR` (its message names `regionSpread`). Changing the period
  below an existing spread must surface the error rather than silently
  saving.
- **Hint line**: when a custom spread is set, the existing `regionsHint`
  sentence should reflect it (e.g. append "staggered {{spread}} apart").
- **Locales**: all languages under `web/dash0/src/locales/`.
- **Mobile**: field must lay out correctly on small screens (repo rule).

### Tests

- Playwright E2E (`web/dash0/e2e/`): create/edit a multi-region check —
  set a spread, save, reload, verify persistence; clear it, verify the
  automatic placeholder returns; verify the field is absent with < 2
  regions; verify an out-of-range value shows the validation error.

## Implementation Plan

Backend contract confirmed via `service.go` / `reconcile_test.go` before touching
any frontend code (no backend changes needed):
- `CreateCheckRequest.RegionSpread` / `UpdateCheckRequest.RegionSpread` are both
  `*string` (`json:"regionSpread,omitempty"`), parsed by
  `timeutils.Duration.Scan` (accepts `"HH:MM:SS"` and Go duration strings like
  `"5s"`), validated `0 <= spread < period` (`errRegionSpreadOutOfRange`,
  `service.go:147`).
- Response always serializes `regionSpread` as `"HH:MM:SS"` (same format as
  `period`), omitted when the check uses the automatic default.
- Because `encoding/json` cannot distinguish `null` from an absent key for a
  `*string`, the wire idiom (mirroring `checkGroupUid` / `escalationPolicyUid`
  already in `check-form.tsx`) is: send `""` on **edit** to clear back to
  automatic, omit the key (`undefined`) on **create** or when untouched. The
  spec's "send `regionSpread: null`" is the product intent (clear the
  override); the actual JSON payload uses `""`, not literal `null`.

Steps (one commit per step, `make fmt` before each commit):

1. **Types**: add `regionSpread?: string` to `Check`, `CreateCheckRequest`,
   `UpdateCheckRequest` in `web/dash0/src/api/hooks.ts`, and to
   `CheckFormData` in `check-form.tsx`.
2. **Design reference**: extend the existing "Duration input (number + unit)"
   pattern in `design-reference.tsx` with a `seconds` unit option (region
   spread needs finer granularity than the maintenance-window duration input
   that pattern was built for), and note the check-form usages (period +
   region spread) in its description.
3. **Region Spread field** in `check-form.tsx`'s Scheduling card, rendered
   only when `selectedRegions.length > 1`:
   - New state `regionSpreadValue` (string, `""` = unset) + `regionSpreadUnit`
     (`seconds` | `minutes` | `hours`), seeded from `initialData.regionSpread`
     via a `parseRegionSpread` helper (mirrors `parsePeriod`, picks the
     largest whole unit that divides evenly, defaulting to seconds).
   - Reuses the number+unit primitive (`<Input type="number">` + unit
     `<Select>`), same visual style as the existing passive-type period
     input.
   - Automatic-value display: `Math.floor(regionPeriodSeconds /
     selectedRegions.length)` (the same `regionPeriodSeconds` the existing
     regions hint already computes), shown via `formatDuration` in a help
     line when the field is empty; shows the "leave empty" hint text either
     way (empty vs custom shows different copy).
4. **Save semantics**: in `handleSubmit`, when the field is visible, submit
   `regionSpread: secondsToHMS(value)` if set, else `""` on edit / `undefined`
   on create (matching the `checkGroupUid` idiom) — never `undefined` when the
   user explicitly typed `0`.
5. **Client-side validation**: mirror `0 <= spread < period` before submit,
   blocking with the same message shown inline under the field
   (`data-testid="region-spread-error"`); backend `VALIDATION_ERROR` on submit
   still falls through to the existing top-level error `Alert` (no dedicated
   backend-error field-matching needed — the message is already
   self-explanatory and this mirrors how other period-bound errors in this
   form surface today).
6. **Hint line update**: when a valid custom spread is set, swap
   `form.regionsHint` for a new `form.regionsHintSpread` key that appends
   "staggered {{spread}} apart".
7. **Locales**: add `regionSpread`, `regionSpreadHelp`,
   `regionSpreadAutomatic`, `regionSpreadRangeError`, `regionsHintSpread` to
   `checks.json` in `en`, `de`, `es`, `fr`.
8. **E2E test**: new `web/dash0/e2e/check-region-spread.spec.ts`. Since the
   local dev/test server has only the single `"default"` region unless
   `SP_REGIONS` is seeded, the test creates a throwaway private region via
   `POST /api/v1/orgs/test/private-regions` (the same lightweight mechanism
   `check-ssh-tunnel.spec.ts` already uses) to get a real second region to
   select, covering: field absent with 1 region; set a spread, save, reload,
   verify persistence; clear it, verify the automatic placeholder returns;
   out-of-range value shows the inline validation error.
9. **QA**: `make build-dash0`, `cd web/dash0 && bun run lint` (no new errors
   in touched files), run the new/affected Playwright spec.
