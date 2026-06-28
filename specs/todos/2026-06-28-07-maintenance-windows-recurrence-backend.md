# Maintenance Windows — Recurrence Backend Correctness & Canonical Description

## Context

Recurring maintenance windows (`daily` / `weekly` / `monthly`) are confusing and, in
two places, **incorrect**. This is the backend half of a two-spec effort to make
recurrences understandable; the frontend half is
[`2026-06-28-08-maintenance-windows-recurrence-frontend.md`](2026-06-28-08-maintenance-windows-recurrence-frontend.md).

This spec is **independently shippable** and should land **first** — the frontend
summary/preview (spec 08) can consume the canonical `status` + `nextOccurrences`
fields this spec adds, and both sides must agree on the stepping math.

The recurrence model is **single-anchor**: a recurring window stores one `startAt`
(the anchor) + one `endAt`; only `endAt − startAt` (the duration) is used for
occurrences, and the cadence is *derived from the anchor* — its time-of-day (daily),
its weekday (weekly), or its day-of-month (monthly). We **keep** that model here.
Multi-day weekly / arbitrary RRULE is explicitly out of scope (see Future).

---

## Current state (verified against source)

| Surface | Location |
|---|---|
| Model + `IsActiveAt` + `addDays/addWeeks/addMonths` + `isActiveForRecurrence` | `server/internal/db/models/maintenance_window.go` |
| Service (validation, response shape `MaintenanceWindowResponse`) | `server/internal/handlers/maintenancewindows/service.go` |
| HTTP handlers | `server/internal/handlers/maintenancewindows/handler.go` |
| Incident suppression (calls `IsActiveAt`, 60s cache) | `server/internal/handlers/incidents/service.go`, `…/maintenance_cache_test.go` |
| MCP tools | `server/internal/mcp/tools_maintenance.go` |
| Public `inMaintenance` flag (status0 already server-computed) | `web/status0/src/api/hooks.ts` (consumer) |
| User docs | `web/docs/docs/features/maintenance-windows.md` |

There is **no** `server/internal/db/models/maintenance_window_test.go` today — the
recurrence math is untested at the unit level.

---

## Problems to fix

### P1 — Monthly recurrence drifts (calendar-overflow bug)

`addMonths` uses `t.AddDate(0, n, 0)`, and `isActiveForRecurrence` steps **cumulatively**
(`current = adder(current, 1)` in a loop). Two compounding defects:

1. **Go overflow normalization:** `Jan 31 + 1 month → Mar 3` (Feb has no 31st, so it
   rolls over). A "monthly on the 31st" window silently skips February and lands on the
   wrong March day.
2. **Cumulative compounding:** because each step adds one month to the *previous*
   (already-drifted) occurrence rather than to the original anchor, the error
   accumulates: `Jan 31 → Mar 3 → Apr 3 → May 3 …`. After a few months the window no
   longer matches the intended day at all.

This is a real suppression bug: a window meant to silence alerts on the 31st can fail
to suppress, or suppress on the wrong day. The same compounding exists in the frontend
port (`web/dash0/src/lib/maintenance-window-status.ts`) and must be fixed in lockstep
(spec 08).

### P2 — MCP tool advertises an interface the backend rejects

`tools_maintenance.go` documents `recurrence` as an **iCalendar RRULE string**:

> `"FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"` … `"FREQ=MONTHLY;BYMONTHDAY=1"`

But the service only accepts the enum `none|daily|weekly|monthly` (`isValidRecurrence`),
so every RRULE example returns `ErrInvalidRecurrence`. An MCP/LLM client that follows
the description **cannot create a recurring window**. The description must match reality.

### P3 — No canonical "what does this rule do" on the API

`MaintenanceWindowResponse` carries only the raw `startAt/endAt/recurrence/recurrenceEnd`.
Every consumer that wants "is it active now?" or "when is it next?" must re-derive it:
dash0 ported `IsActiveAt` by hand (with the P1 bug), status0 needs the same, MCP answers
"when next" by guessing. There is no single source of truth for **status** and
**next occurrences**.

---

## Approach

1. Make `IsActiveAt` correct by anchoring every occurrence to the **original** `startAt`
   with **month-end clamping**, and finding the active occurrence by index math instead
   of a compounding loop.
2. Add reusable, tested helpers `Status(...)` and `NextOccurrences(...)` in the `models`
   package — the canonical recurrence semantics.
3. Surface `status` + `nextOccurrences` as **additive, backward-compatible** read-only
   fields on `MaintenanceWindowResponse`.
4. Correct the MCP `recurrence` documentation to the real enum + anchor semantics.
5. Test it all (new model test file) and update user docs.

**Timezone decision (V1, documented, not changed here):** occurrences step in stored-UTC
wall-clock (`AddDate`), so a recurring time is anchored to UTC and drifts ±1h in local
time across DST. We keep this for V1 but **document it** and require the frontend port to
step in UTC too (`setUTCDate`/`setUTCMonth`) so client and server agree. Local-timezone
anchoring is a future enhancement (see Future).

---

## Tasks

### 1. Fix the recurrence stepping — `server/internal/db/models/maintenance_window.go`

- Rewrite `addMonths` to clamp to the target month length instead of overflowing:
  ```go
  // addMonths shifts t by n months, clamping the day to the last day of the
  // target month (Jan 31 +1mo -> Feb 28/29, not Mar 3).
  func addMonths(t time.Time, n int) time.Time {
      y, m, d := t.Date()
      first := time.Date(y, m, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
      target := first.AddDate(0, n, 0) // first-of-month never overflows
      last := daysIn(target.Year(), target.Month())
      if d > last {
          d = last
      }
      return time.Date(target.Year(), target.Month(), d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
  }
  ```
  Add a `daysIn(year int, month time.Month) int` helper
  (`time.Date(year, month+1, 0, …).Day()`).
- Rewrite `isActiveForRecurrence` to **anchor on the original start** and clamp per
  occurrence, removing the cumulative-drift loop. Compute occurrence `k` as
  `adder(start, k)` (each from the original `start`), find the largest `k` whose
  occurrence-start is `≤ target`, then test `[occStart, occStart+duration)`. A direct
  index calculation is preferred over a loop; if a loop is kept, it MUST recompute from
  `start` each step (`adder(start, k)`), never from the previous occurrence.
- `addDays` / `addWeeks` are correct (no month boundaries) — leave them, but they now
  feed the original-anchor logic.

### 2. Canonical recurrence helpers — same file

Add (exported, documented, the single source of truth):

```go
// Occurrence is one concrete activation of a (possibly recurring) window.
type Occurrence struct {
    StartAt time.Time `json:"startAt"`
    EndAt   time.Time `json:"endAt"`
}

// Status returns "active", "upcoming", or "past" at now.
func Status(w *MaintenanceWindow, now time.Time) string

// NextOccurrences returns up to n occurrences whose end is at/after `from`,
// in chronological order, honoring RecurrenceEnd. For recurrence "none" it
// returns the single window when not yet past, else empty.
func NextOccurrences(w *MaintenanceWindow, from time.Time, n int) []Occurrence
```

- `Status` extracts exactly the logic the frontend ported (`computeMaintenanceStatus`):
  active if `IsActiveAt`; else past if non-recurring and after start, or recurring and
  after `RecurrenceEnd`; else upcoming.
- `NextOccurrences` must use the **same** anchored+clamped stepping as task 1 (share the
  adder helpers). Cap `n` defensively (e.g. ≤ 100). Skip occurrences whose `EndAt` is
  before `from` so the "currently active" occurrence is included as the first entry.
- Refactor `IsActiveAt` to lean on these where it simplifies — but keep its external
  behavior identical (incident suppression depends on it).

### 3. Expose `status` + `nextOccurrences` — `…/maintenancewindows/service.go`

- Add to `MaintenanceWindowResponse`:
  ```go
  Status          string              `json:"status"`
  NextOccurrences []models.Occurrence `json:"nextOccurrences"`
  ```
- In `convertWindowToResponse`, compute both from `time.Now().UTC()`:
  `Status(window, now)` and `NextOccurrences(window, now, 3)`. Keep the default count at
  **3** (matches the frontend preview). Times serialize as RFC3339 UTC (frontend renders
  local).
- Additive only — existing fields and shapes are unchanged. No new request fields.
- `convertWindowToResponse` is pure today (no clock); inject `now` (pass it in, or read
  `time.Now().UTC()` at the call sites) so it stays unit-testable.

### 4. Fix MCP recurrence docs — `server/internal/mcp/tools_maintenance.go`

Replace the RRULE text on `propRecurrence` (create **and** update) with the truth:

> `recurrence`: one of `none`, `daily`, `weekly`, `monthly`. The cadence is anchored to
> `startAt`: `daily` repeats `startAt`'s time-of-day every day; `weekly` repeats on
> `startAt`'s weekday; `monthly` repeats on `startAt`'s day-of-month (clamped to the last
> day of shorter months). Each occurrence lasts `endAt − startAt`. Omit (or `none`) for a
> one-off window. iCalendar RRULE strings are **not** supported.

Also enrich the `get`/`list` tool descriptions to mention the response now includes
`status` and `nextOccurrences`. Keep wording terse.

### 5. User docs — `web/docs/docs/features/maintenance-windows.md`

In "Defining a Window", clarify the anchor semantics and month-end clamping (e.g. "A
monthly window anchored on the 31st runs on the last day of shorter months"). Add the
`status` / `nextOccurrences` response fields to any API-shape mention. Keep it short.

---

## Tests

### `server/internal/db/models/maintenance_window_test.go` (new)

Table-driven, `t.Parallel()`, `testify/require` (per `server/CLAUDE.md`). Cover:

- **`IsActiveAt` — none:** inside / before / after; boundary (`target == startAt` active,
  `target == endAt` inactive).
- **`IsActiveAt` — daily:** active on day 0 and day N at the right time-of-day; inactive
  between slots; respects `RecurrenceEnd`; inactive before `startAt`.
- **`IsActiveAt` — weekly:** matches the anchor weekday only; N weeks later.
- **`IsActiveAt` — monthly (the P1 regression):**
  - anchor Jan 31 → active Feb 28 (29 in a leap year), Mar 31, Apr 30 — assert **no
    drift** across ≥4 months.
  - anchor on the 15th → 15th every month.
  - anchor Jan 30 across a leap Feb (active Feb 29) and non-leap Feb (active Feb 28).
- **`Status`:** active / upcoming / past for recurring and non-recurring, incl. after
  `RecurrenceEnd`.
- **`NextOccurrences`:** correct count, chronological, first entry is the
  currently-active occurrence when one is active, empty for a past one-off, honors
  `RecurrenceEnd`, monthly clamping holds across the returned set.

### Service/handler test — `…/maintenancewindows/service_test.go`

Assert `convertWindowToResponse` populates `status` and a capped `nextOccurrences`
(use a fixed injected `now`). If a handler test exists, assert the JSON contains the new
keys.

### MCP regression — `server/internal/mcp/tools_maintenance_test.go`

Assert an RRULE-style value (`"FREQ=WEEKLY;BYDAY=MO"`) is rejected with the
invalid-recurrence error (locks the docs to behavior). Add/confirm a happy-path for the
enum values.

### Existing suppression tests

Re-run `server/internal/handlers/incidents/maintenance_cache_test.go`; add a monthly
month-end case there if not covered, since suppression now benefits from the P1 fix.

---

## Key files

| File | Change |
|---|---|
| `server/internal/db/models/maintenance_window.go` | **~** fix `addMonths`+stepping; **+** `Occurrence`, `Status`, `NextOccurrences`, `daysIn` |
| `server/internal/db/models/maintenance_window_test.go` | **New** — table-driven recurrence tests |
| `server/internal/handlers/maintenancewindows/service.go` | **+** `status` + `nextOccurrences` on response; compute in `convertWindowToResponse` |
| `server/internal/handlers/maintenancewindows/service_test.go` | **+** assert new response fields |
| `server/internal/mcp/tools_maintenance.go` | **~** correct `recurrence` docs (create+update); mention new fields |
| `server/internal/mcp/tools_maintenance_test.go` | **+** RRULE-rejected regression |
| `web/docs/docs/features/maintenance-windows.md` | **~** anchor + clamping + new fields |

---

## Verification

```bash
make test          # backend unit/integration incl. new model + service tests
make lint          # golangci-lint clean (no relaxing .golangci.yml)
# Manual: create a monthly window anchored on the 31st via API, GET it,
# confirm nextOccurrences lands on Feb 28/29, Mar 31, Apr 30 (no drift)
# and status reflects active/upcoming/past correctly.
```

API smoke (test mode):
```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/maintenance-windows' \
  | jq '.data[] | {title, recurrence, status, nextOccurrences}'
```

---

## Out of scope / Future

- **Multi-day weekly & arbitrary RRULE** (`BYDAY` sets, "first Monday", `BYMONTHDAY`
  lists). The single-anchor model can't express them; the MCP author's RRULE intent
  (P2) is the seed for this. It would need a structured/RRULE recurrence column +
  migration + a rewrite of `IsActiveAt`, `NextOccurrences`, and incident suppression +
  data backfill. Capture as its own epic.
- **Local-timezone anchoring** of recurring times (DST-stable in local wall-clock).
  Requires storing the window's timezone and stepping in that zone. V1 stays UTC-anchored
  and documented.
- **Overlapping-occurrence validation** (duration ≥ recurrence interval). Harmless today;
  the frontend (spec 08) warns. No backend hard error so existing data keeps working.

---

## Implementation Plan

Backend-only, additive and backward-compatible. No DB schema change (the single-anchor
model is kept). Steps map 1:1 to the spec Tasks.

1. **Recurrence stepping fix** (`server/internal/db/models/maintenance_window.go`)
   - Add `daysIn(year int, month time.Month) int` helper.
   - Rewrite `addMonths` to clamp the day to the last day of the target month
     (`Jan 31 +1mo → Feb 28/29`, never `Mar 3`), stepping first-of-month so the
     intermediate never overflows.
   - Rewrite `isActiveForRecurrence` to anchor every occurrence on the **original**
     `start` (`adder(start, k)`), via direct index math, not a compounding loop —
     compute `k` from the elapsed interval, then clamp by scanning a tiny ±2 window of
     `k` (handles month-length variance) to find the latest occurrence whose start
     `≤ target`, then test `[occStart, occStart+duration)`.
   - `addDays`/`addWeeks` unchanged.

2. **Canonical helpers — same file**
   - `type Occurrence struct { StartAt, EndAt time.Time }` (JSON `startAt`/`endAt`).
   - `func adderFor(recurrence string) timeAdder` shared by stepping + occurrences.
   - `func Status(w *MaintenanceWindow, now time.Time) string` → `active|upcoming|past`,
     mirroring the frontend `computeMaintenanceStatus`.
   - `func NextOccurrences(w *MaintenanceWindow, from time.Time, n int) []Occurrence`:
     cap `n` at 100; for `none` return the single window unless past; for recurring,
     walk anchored+clamped occurrences from `start`, skip those whose `EndAt < from`,
     stop at `RecurrenceEnd`, return up to `n` in chronological order.
   - Keep `IsActiveAt` external behavior identical (incident suppression depends on it).

3. **Expose `status` + `nextOccurrences`** (`…/maintenancewindows/service.go`)
   - Add `Status string` and `NextOccurrences []models.Occurrence` to
     `MaintenanceWindowResponse` (additive, read-only).
   - Make `convertWindowToResponse(window, now)` take an injected `now`; call sites pass
     `time.Now().UTC()`. Compute `Status(window, now)` and `NextOccurrences(window, now, 3)`.

4. **MCP recurrence docs** (`server/internal/mcp/tools_maintenance.go`)
   - Replace the RRULE text on `propRecurrence` (create + update) with the enum + anchor
     semantics from the spec; state RRULE is not supported.
   - Mention `status` + `nextOccurrences` in the `get`/`list` tool descriptions.

5. **User docs** (`web/docs/docs/features/maintenance-windows.md`)
   - Clarify anchor semantics + month-end clamping; add `status`/`nextOccurrences` fields.

6. **Tests**
   - New `server/internal/db/models/maintenance_window_test.go`: table-driven
     `IsActiveAt` (none/daily/weekly/monthly incl. the P1 no-drift regression),
     `Status`, `NextOccurrences`, `addMonths`/`daysIn`.
   - `…/maintenancewindows/service_test.go` (new): `convertWindowToResponse` populates
     `status` + capped `nextOccurrences` with a fixed injected `now`.
   - `server/internal/mcp/tools_maintenance_test.go`: update the existing
     "descriptions include examples" test to the new enum docs; assert RRULE is rejected
     by `isValidRecurrence` semantics (service-level) / docs no longer advertise RRULE.
   - Add a monthly month-end case to
     `server/internal/handlers/incidents/maintenance_cache_test.go` if not covered.

7. **QA**: `make build-backend lint-back test`; fix until green; final passing commit.
