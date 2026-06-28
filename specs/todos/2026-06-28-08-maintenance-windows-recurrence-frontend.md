# Maintenance Windows — Comprehensible Recurrence UI

## Context

For `daily` / `weekly` / `monthly` maintenance windows the dash0 schedule form is
**impossible to read**. Today it shows four raw datetime fields
(`Start`, `End`, `Recurrence`, `Recurrence end`) and the actual rule is hidden:

- **`End`'s date is meaningless under recurrence.** The backend only uses
  `endAt − startAt` (the duration) — see
  `server/internal/db/models/maintenance_window.go` `IsActiveAt`. So `End = 01/01/2020,
  23:00` is a lie; only "1 hour" is used. Worse, picking an End on a different day
  silently makes every occurrence hours/days long with zero feedback.
- **The cadence is hidden in `Start`'s date.** "Weekly" never says *which weekday*
  (it's `startAt`'s weekday); "Monthly" never says *which day-of-month*. The anchor date
  (often far in the past) reads as garbage.
- **No plain-language summary anywhere.** The user must mentally simulate `IsActiveAt`
  over four fields to know what the window does.

This is the **frontend half**; the backend half is
[`2026-06-28-07-maintenance-windows-recurrence-backend.md`](2026-06-28-07-maintenance-windows-recurrence-backend.md),
which fixes a monthly-drift bug and adds canonical `status` + `nextOccurrences` to the
API. **Phase 1 here ships independently of spec 07.** Phase 1's read-only views *prefer*
the server `status`/`nextOccurrences` when present and fall back to a client port; the
live form preview is always client-side (it must reflect unsaved edits).

We keep the **single-anchor model** (one weekday, one day-of-month). Multi-day weekly is
backend-future (spec 07). The form's **output contract is unchanged** —
`CreateMaintenanceWindowRequest { title, description?, startAt, endAt, recurrence,
recurrenceEnd? }` — so `maintenance-windows.new.tsx` / `…edit.tsx` are **untouched**; all
change is inside the form component, a new util, the detail/list views, i18n, the design
reference, and E2E.

---

## Current state (verified against source)

| Surface | Location | Note |
|---|---|---|
| Form (all the confusing UI) | `web/dash0/src/components/shared/maintenance-window-form.tsx` | controlled state; `isoToLocalInput`/`localInputToIso` helpers |
| Client status port | `web/dash0/src/lib/maintenance-window-status.ts` | `isActiveAt`/`computeMaintenanceStatus`; **has the same monthly-drift bug** as the backend |
| Detail view | `…/routes/orgs/$org/maintenance-windows.$maintenanceWindowUid.index.tsx` | renders raw start/end/recurrence/recurrenceEnd |
| List view | `…/routes/orgs/$org/maintenance-windows.index.tsx` | Schedule + Recurrence columns |
| New / Edit routes | `…maintenance-windows.new.tsx`, `…edit.tsx` | thin wrappers — **no change** |
| i18n | `web/dash0/src/locales/{en,fr,de,es}/maintenanceWindows.json` | en authoritative |
| Design reference (mandatory) | `web/dash0/src/routes/orgs/$org/design-reference.tsx` | new primitives must be registered here |
| E2E | `web/dash0/e2e/maintenance-windows.spec.ts` | asserts `mw-recurrence-end-input` as datetime-local — **must update** |

---

## Phase 1 — Plain-language summary + next-occurrence preview *(independently shippable)*

The high-leverage, low-risk win. Removes most of the confusion without changing inputs.

### 1.1 Schedule description util — `web/dash0/src/lib/maintenance-window-schedule.ts` (new)

- `nextOccurrences(w, from, n)` — client port of the backend `NextOccurrences`. **Step in
  UTC** (`setUTCDate`/`setUTCMonth`) and **anchor on the original `startAt` with month-end
  clamping** so it matches the (fixed) backend exactly. Returns `{ startAt, endAt }[]`.
  > Fix the **same monthly-drift bug** the backend fixes (spec 07 P1): the existing
  > `step()` in `maintenance-window-status.ts` compounds drift via repeated `setMonth`.
  > Either fix `maintenance-window-status.ts` in place and reuse it, or supersede it here
  > and update its callers. Client and server must produce identical occurrence lists —
  > use shared example vectors in tests.
- `describeSchedule(w, t)` — returns a localized one-line human summary using the i18n
  keys below. Examples (en):
  - none → `One-time: Jul 1 2026, 22:00 – 23:00`
  - daily → `Every day, 22:00 – 23:00 (1h)`
  - weekly → `Every Wednesday, 22:00 – 23:00 (1h)`
  - monthly → `On day 1 each month, 22:00 – 23:00 (1h)`
  - append ` · until Jan 1 2030` when `recurrenceEnd` set, else ` · no end date` for
    recurring.
  - Use `Intl.DateTimeFormat`/`toLocaleDateString`/`toLocaleTimeString` for
    weekday/date/time names (no hand-translated weekday tables). Day-of-month phrased as
    "day {{n}}" (not "1st") to avoid locale ordinals.
- `formatDuration(ms, t)` — compact `1h`, `30m`, `2h 30m`, `1d` using i18n unit keys.

### 1.2 Live summary panel in the form

Below the schedule card, render a summary panel (accent-tinted, mirror the agreed
mockup) that updates live as the user edits:

- Line 1: `describeSchedule(currentDraft, t)`.
- Line 2: alerts-paused reminder (reuse existing copy intent).
- Line 3: `Next: <date> · <date> · <date>` from `nextOccurrences(draft, new Date(), 3)`
  (or "Next: <date>" for one-time; nothing if past).

`data-testid="mw-schedule-summary"`. Build it from existing primitives (a tinted
`Card`/`div` + `lucide-react` `CalendarClock`); register the pattern in
`design-reference.tsx`.

### 1.3 Detail page summary

In `…$maintenanceWindowUid.index.tsx`, add the summary sentence prominently at the top of
the Schedule card, and a "Next occurrences" list. **Prefer server data** when present:
use `window.status` for the badge (falling back to `computeMaintenanceStatus`) and
`window.nextOccurrences` for the list (falling back to the client util). Keep the raw
start/end/recurrence rows below as detail.

### 1.4 List page

Replace the separate `Schedule` + `Recurrence` columns' raw text with the human summary
(`describeSchedule`) as the primary line (keep it compact; truncate on mobile). Status
badge prefers `window.status` when present.

---

## Phase 2 — Inputs that match the recurrence

Restructure the form's schedule section so the three real concepts are explicit:
**cadence**, **time-slot (time + duration)**, **validity range**. Encode/decode to the
unchanged `startAt/endAt/recurrence/recurrenceEnd` contract.

### 2.1 Field layout by recurrence

Keep the existing `Recurrence` `Select` (None/Daily/Weekly/Monthly). Then:

**`none` (one-time):** keep today's two `datetime-local` inputs (`Start` `mw-start-input`,
`End` `mw-end-input`). This is the one case where full datetimes are correct.

**`daily` / `weekly` / `monthly`:** replace the two datetime pickers with:

| Field | Control | testid | Shown for |
|---|---|---|---|
| Repeat on (weekday) | single-select chip row Mon–Sun | `mw-weekday-{0..6}` | weekly |
| Day of month | `Select` 1–28 | `mw-dayofmonth-select` | monthly |
| Start time | `<input type="time">` | `mw-start-time-input` | daily/weekly/monthly |
| Duration | number `Input` + unit `Select` (minutes/hours), or `hh:mm` | `mw-duration-input` | daily/weekly/monthly |
| First day | `<input type="date">` | `mw-first-day-input` | daily/weekly/monthly |
| Until (optional) | `<input type="date">` | `mw-recurrence-end-input` | daily/weekly/monthly |

- **Weekday picker is single-select** (model is single-anchor). Default = First day's
  weekday. Selecting a weekday snaps the anchor (see encode).
- **Day of month capped at 28** to avoid month-end drift surprises; helper text: "Months
  shorter than your chosen day run on their last day." ("Last day of month" is
  backend-future — spec 07.)
- **Until becomes date-only** (was datetime-local) — encode to end-of-day.

### 2.2 Encode (form state → API)

- `recurrence = none`: as today (`localInputToIso(start)`, `localInputToIso(end)`).
- recurring:
  - Compose a local datetime from **First day** + **Start time**, then **snap** so the
    anchor's cadence matches the picker, choosing the first matching day **on/after**
    First day:
    - daily → use First day as-is.
    - weekly → advance to the next chosen weekday on/after First day.
    - monthly → set day-of-month to the chosen day (clamped to that month's length) on/after
      First day.
  - `startAt = toIso(snappedLocalDateTime)`.
  - `endAt = toIso(snapped + durationMs)`.
  - `recurrenceEnd = Until ? toIso(end-of-day local of Until) : null`.

### 2.3 Decode (API → form state, for edit prefill)

From `startAt`/`endAt`:
- `startTime` = local `HH:mm` of `startAt`; `durationMs` = `endAt − startAt` → duration
  input; `firstDay` = local date of `startAt`; weekday = `startAt`'s weekday; dayOfMonth =
  `startAt`'s day-of-month (if > 28, clamp the selector to 28 and surface a note);
  `until` = local date of `recurrenceEnd`.
- Switching recurrence in the form keeps time/duration; recomputes cadence defaults from
  the current First day.

### 2.4 Validation (inline, before submit)

- Title non-empty (existing).
- Duration > 0.
- **Warn** (non-blocking) when duration ≥ the recurrence interval (occurrences overlap):
  e.g. daily with >24h. Show as a muted warning under the summary, not a hard error
  (backend accepts it).
- First day required for recurring; Until (if set) strictly after First day.
- Reuse the existing `errors` map + inline `text-destructive` pattern.

### 2.5 Design reference (mandatory — `CLAUDE.md`)

Add to `design-reference.tsx`: the **weekday chip selector**, the **duration input**
(number+unit), and the **schedule-summary panel**, each with its import line. If a chip
selector already exists in the catalog, reuse it.

---

## i18n — `web/dash0/src/locales/{en,fr,de,es}/maintenanceWindows.json`

en authoritative; translate all four (`fr`, `de`, `es`). Add under `form` and new groups:

- `form.startTime`, `form.duration`, `form.durationUnitMinutes`, `form.durationUnitHours`,
  `form.firstDay`, `form.repeatOn`, `form.dayOfMonth`, `form.dayOfMonthHelp`.
- `summary.once`, `summary.daily`, `summary.weekly`, `summary.monthly` (with
  interpolation `{{start}}`, `{{end}}`, `{{duration}}`, `{{weekday}}`, `{{day}}`, `{{date}}`),
  `summary.until` (`{{date}}`), `summary.noEnd`, `summary.alertsPaused`.
- `summary.nextLabel`, `summary.nextNone`.
- `duration.hoursShort` (`h`), `duration.minutesShort` (`m`), `duration.daysShort` (`d`).
- `form.errors.durationRequired`, `form.errors.durationOverlapsWarning`,
  `form.errors.firstDayRequired`, `form.errors.untilAfterFirstDay`.

Prefer `Intl` for weekday/month/date/time rendering — do **not** add weekday-name tables.
No hardcoded English strings in components.

---

## E2E — `web/dash0/e2e/maintenance-windows.spec.ts` (extend)

Use `./fixtures` `authenticatedPage` (test mode: org `test`, `test@test.com`/`test`).

- **Update the existing weekly test:** it asserts `mw-recurrence-end-input` is a
  datetime-local; it is now a date input, and Weekly now reveals `mw-weekday-*` chips +
  `mw-start-time-input` + `mw-duration-input` + `mw-first-day-input` instead of
  `mw-start-input`/`mw-end-input`.
- New assertions:
  - Selecting **Weekly** shows the weekday chip row; selecting a chip updates
    `mw-schedule-summary` to "Every <weekday> …".
  - Selecting **Monthly** shows `mw-dayofmonth-select` and the summary reads "On day N …".
  - The summary panel shows a non-empty `Next:` line for a future recurring draft.
  - **Round-trip:** create a weekly window via the new inputs → open edit → the weekday,
    start time, duration, and first day are prefilled to the same values.
  - One-time (None) still uses `mw-start-input` / `mw-end-input`.
- Screenshot each state into `test-results/screenshots/`. Treat any flake as a bug to
  root-cause (capture logs/guards), never re-run blindly
  ([[feedback_flaky_tests_are_bugs]]).

---

## Key files

| File | Change |
|---|---|
| `web/dash0/src/lib/maintenance-window-schedule.ts` | **New** — `nextOccurrences`, `describeSchedule`, `formatDuration` |
| `web/dash0/src/lib/maintenance-window-status.ts` | **~** fix monthly-drift to match backend (or supersede) |
| `web/dash0/src/components/shared/maintenance-window-form.tsx` | **~** summary panel (P1) + restructured inputs + encode/decode (P2) |
| `…/routes/orgs/$org/maintenance-windows.$maintenanceWindowUid.index.tsx` | **~** summary + next-occurrences; prefer server `status`/`nextOccurrences` |
| `…/routes/orgs/$org/maintenance-windows.index.tsx` | **~** human-readable schedule column; prefer server `status` |
| `web/dash0/src/api/hooks.ts` | **+** optional `status?`, `nextOccurrences?` on `MaintenanceWindow` (additive, from spec 07) |
| `web/dash0/src/routes/orgs/$org/design-reference.tsx` | **+** weekday chips, duration input, summary panel |
| `web/dash0/src/locales/{en,fr,de,es}/maintenanceWindows.json` | **+** new keys (4 locales) |
| `web/dash0/e2e/maintenance-windows.spec.ts` | **~** update + extend |
| `…new.tsx`, `…edit.tsx` | **No change** (output contract unchanged) |

---

## Implementation order

1. **Phase 1 first (ship-able alone):** schedule util (§1.1) with parity-tested
   `nextOccurrences` + drift fix → live summary in form (§1.2) → detail (§1.3) → list
   (§1.4) → i18n for summary keys → E2E summary assertions. `make dev-test`, click
   through on mobile + desktop.
2. **Phase 2:** restructured inputs + encode/decode (§2.1–2.3) → validation (§2.4) →
   design-reference (§2.5) → remaining i18n → E2E input + round-trip assertions.
3. `make lint` + `make test-dash`. Keep dash0 lint to **no NEW errors** — base is red
   with pre-existing react-hooks errors; do not fix that debt here
   ([[project_dash0_eslint_debt]]).

---

## Verification

```bash
make dev-test   # backend + dash0, SP_RUNMODE=test, port 4000
# Maintenance → New: pick Weekly → choose a weekday → summary reads "Every <day>, HH:MM–HH:MM (Nh)"
#   and a "Next: …" preview appears. Pick Monthly → "On day N each month, …".
# Save, reopen Edit → weekday/time/duration/first-day round-trip correctly.
# Detail + List show the human summary; status badge correct.
# Resize to mobile width — fully usable (chips wrap, inputs stack).
make test-dash  # Playwright E2E
make lint       # no NEW dash0 lint errors
```

Cross-check parity: a window created here, fetched from the API, must show the **same**
`nextOccurrences` the client preview computed (spec 07 makes the server canonical).

---

## Out of scope

- Multi-day weekly / arbitrary RRULE selection (needs the backend model change in
  spec 07's Future section). Weekday picker stays single-select.
- "Last day of month" option (needs backend clamping semantics; day-of-month capped at 28
  for now).
- Local-timezone anchoring of recurring times (V1 stays UTC-anchored; client steps in UTC
  to match the server).

---

## Implementation Plan

Backend already ships `status` and `nextOccurrences` (array of `{startAt, endAt}`) on every
`MaintenanceWindowResponse` (`server/internal/handlers/maintenancewindows/service.go`).
`models.NextOccurrences` anchors every monthly occurrence on the **original** `startAt` via
`addMonths(start, k)` with month-end clamping (no compounding drift). The client port must
match that exactly: `adder(startAt, k)`, stepping in **UTC**.

### Step 1 — Types (additive)
- `web/dash0/src/api/hooks.ts`: add `MaintenanceOccurrence { startAt; endAt }`; add optional
  `status?: "active" | "upcoming" | "past"` and `nextOccurrences?: MaintenanceOccurrence[]`
  to `MaintenanceWindow`.

### Step 2 — Schedule util `web/dash0/src/lib/maintenance-window-schedule.ts` (new)
- `nextOccurrences(w, from, n)` — UTC-anchored client port of backend `NextOccurrences`
  (daily/weekly add days; monthly = `addMonthsUTC(start, k)` clamped to month length; honor
  `recurrenceEnd`; include currently-active occurrence first; `none` returns the single
  window when `endAt > from`). Returns `{ startAt, endAt }[]`.
- `formatDuration(ms, t)` — compact `1h` / `30m` / `2h 30m` / `1d` using `duration.*Short`.
- `describeSchedule(w, t)` — localized one-line summary using `summary.*`, `Intl` for
  weekday/date/time (no weekday tables), day-of-month as "day {{n}}". Appends
  `summary.until` / `summary.noEnd` for recurring.
- Also fix the monthly-drift bug in `maintenance-window-status.ts` (`step()` compounds
  `setMonth`): re-anchor monthly on the original start with clamping, stepping in UTC, so
  `computeMaintenanceStatus`/`isActiveAt` match the backend. Daily/weekly also stepped in UTC.

### Step 3 — Unit tests `web/dash0/src/lib/maintenance-window-schedule.test.ts` (new)
- Parity vectors mirrored from `server/internal/db/models/maintenance_window_test.go`
  (Jan-31 monthly no-drift to Feb-28/Mar-31; daily slot; weekly weekday; `none`;
  `recurrenceEnd` cutoff). Assert `nextOccurrences` + `isActiveAt` agree with the documented
  backend results, and `describeSchedule`/`formatDuration` output.

### Step 4 — Schedule summary panel component (P1.2)
- Reusable `MaintenanceScheduleSummary` (tinted card + `CalendarClock`) rendering
  `describeSchedule`, alerts-paused reminder, and the `Next: …` line from `nextOccurrences`.
  `data-testid="mw-schedule-summary"`. Used by the form (live, client-computed draft) and
  registered in `design-reference.tsx`.

### Step 5 — Form Phase 1: live summary (P1.2)
- Render the summary panel below the schedule card in `maintenance-window-form.tsx`, built
  from a `currentDraft` derived from live form state.

### Step 6 — Detail page (P1.3)
- `maintenance-windows.$maintenanceWindowUid.index.tsx`: summary sentence at top of Schedule
  card + "Next occurrences" list. Prefer `window.status` (fallback
  `computeMaintenanceStatus`) and `window.nextOccurrences` (fallback client util).

### Step 7 — List page (P1.4)
- `maintenance-windows.index.tsx`: replace raw Schedule + Recurrence cells with
  `describeSchedule` as the primary line; status badge prefers `window.status`.

### Step 8 — Form Phase 2: inputs that match recurrence (§2.1–2.4)
- Keep the Recurrence select. `none` → today's two `datetime-local`
  (`mw-start-input`/`mw-end-input`). Recurring → weekday chip row (`mw-weekday-{0..6}`,
  weekly), day-of-month select 1–28 (`mw-dayofmonth-select`, monthly), start time
  (`mw-start-time-input`), duration number + unit (`mw-duration-input`), first day
  (`mw-first-day-input`), until date (`mw-recurrence-end-input`, now `type=date`).
- Encode: compose First day + Start time, snap anchor to cadence on/after First day
  (weekly → next chosen weekday; monthly → chosen day clamped), `endAt = start + duration`,
  `recurrenceEnd = end-of-day(until)`. Decode for edit prefill from `startAt`/`endAt`.
- Validation: duration > 0, first-day required, until > first-day, non-blocking overlap
  warning when duration ≥ interval. Reuse `errors` map + `text-destructive`.

### Step 9 — i18n (4 locales)
- Add `form.startTime/duration/durationUnitMinutes/durationUnitHours/firstDay/repeatOn/
  dayOfMonth/dayOfMonthHelp`, `summary.*`, `duration.*Short`, and the new `form.errors.*`
  to `en/fr/de/es/maintenanceWindows.json`.

### Step 10 — Design reference (§2.5)
- Register the weekday-chip selector, duration input, and schedule-summary panel in
  `design-reference.tsx` with import lines.

### Step 11 — E2E (extend `web/dash0/e2e/maintenance-windows.spec.ts`)
- Update the weekly test (date `mw-recurrence-end-input`, weekday chips + time + duration +
  first-day instead of start/end). Add: weekday chip updates summary; monthly shows
  day-of-month select and "On day N …"; non-empty `Next:` for a future recurring draft;
  weekly round-trip (create via new inputs → edit prefilled); one-time still uses
  `mw-start-input`/`mw-end-input`.

### QA
- `make build-dash0`; `cd web/dash0 && bun run lint` (no NEW errors in touched files).
- Run the affected E2E file if the local server is in test mode; otherwise author-only.
