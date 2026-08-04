---
model: sonnet
effort: high
---

# Per-status-page availability color thresholds (+ small-bucket calibration)

## Problem

The green/amber/red coloring of status-page availability bars is hardcoded
server-side in `availabilityToStatus`
(`server/internal/handlers/statuspages/service.go:1685`): ≥ 99.9 → `up`
(green), ≥ 99.0 → `degraded` (amber), else `down` (red). It is applied at both
call sites — daily buckets (`service.go:1616` in `buildAvailabilityData`) and
hourly buckets (`service.go:1559` in `buildHourlyAvailabilityData`). The
status0 frontend just renders whatever status the API returns
(`web/status0/src/components/shared/availability-bar.tsx:10-15` via
`statusStyle`), so this is purely a backend decision today.

Two distinct problems:

1. **One-size-fits-all thresholds.** A page for a paid API with a 99.9% SLA
   and a page for an internal batch tool judge themselves by the same bar.
   The status page is exactly the granularity where "what did we promise this
   audience" lives, and `status_pages` already carries per-page display
   settings (`ShowAvailability`, `ShowResponseTime`, `HistoryDays`,
   `CustomCSS` — `server/internal/db/models/status_page.go:53`), but not
   thresholds.

2. **Small-bucket miscalibration.** The same percentage thresholds are
   applied to buckets of very different sample counts:
   - Hourly view, 1-minute checks: one failed sample = 59/60 = **98.33%**,
     which is below both thresholds — a single failed minute renders the hour
     **red**, skipping amber entirely.
   - Daily view, 5-minute checks: one blip = 287/288 = 99.65% → the whole
     day goes amber.

   So bar harshness currently depends more on check frequency and bucket
   unit than on actual incident severity.

Note: the same 99.9/99 pair is duplicated in badges
(`server/internal/handlers/badges/service.go:512` and `:787`). Badges are
per-check and have no status-page context — they are **explicitly out of
scope** here (see Decisions).

## Proposal

### 1. Schema

Add a single generic customization column to `status_pages`:

- `settings` — `jsonb NOT NULL DEFAULT '{}'` (Postgres) / TEXT-with-JSON
  (SQLite), decoded on the Bun model into a **typed struct**, not a
  free-form map, so keys stay discoverable and validation lives in one
  place:

```go
type StatusPageSettings struct {
    Availability *AvailabilitySettings `json:"availability,omitempty"`
}

type AvailabilitySettings struct {
    ThresholdUp       *float64 `json:"thresholdUp,omitempty"`       // green floor; nil = 99.9
    ThresholdDegraded *float64 `json:"thresholdDegraded,omitempty"` // amber floor; nil = 99.0
}
```

This column is the home for future per-page customization knobs (legend
options, timezone/date format, chart tweaks…) without a two-dialect
migration each time. Existing display columns (`show_availability`,
`history_days`, …) are **not** migrated into it in this spec.

Migrations for both dialects (`server/internal/db/postgres/migrations/`,
`server/internal/db/sqlite/migrations/`), following the current consolidated
per-release migration convention.

### 2. API

- Expose a `settings` object mirroring the storage shape on the admin
  create/PATCH/GET status-page endpoints
  (`server/internal/handlers/statuspages/handler.go`, DTOs in `service.go`
  near the existing `showAvailability` fields):
  `{"settings": {"availability": {"thresholdUp": 99.5, "thresholdDegraded": 98}}}`.
- PATCH semantics — **no deep merge**: `settings` absent = unchanged; when
  present, each top-level section provided (e.g. `availability`) replaces
  that section wholly, and an explicit `null` section resets it to
  defaults. Sections not mentioned are untouched.
- Decode into the typed struct with unknown keys rejected
  (`DisallowUnknownFields`) → `VALIDATION_ERROR`, so typos don't silently
  persist.
- Validation on the **effective** values (submitted value, or default when
  nil): `0 < thresholdDegraded < thresholdUp <= 100`, else
  `VALIDATION_ERROR`.
- Public status-page payload: include the **resolved effective** thresholds
  (never null — public consumers shouldn't need to know the defaults) so
  the frontend can later render a legend/target line. Display data only —
  no frontend behavior change required in this spec.

### 3. Status computation

- Parameterize `availabilityToStatus(pct, upThreshold, degradedThreshold)`
  and resolve the page's effective thresholds once per request from
  `page.Settings.Availability` (nil-safe: missing struct or nil fields fall
  back to 99.9/99.0), threading them into `buildAvailabilityData` and
  `buildHourlyAvailabilityData` (both already take per-page flags, same
  plumbing pattern as `showAvailability`).

### 4. Small-bucket calibration guard

A bucket with exactly **one** failed sample renders at worst `degraded`,
never `down` — red requires ≥ 2 failed samples (or pct below the red line
with ≥ 2 failures). Failure count is derivable from the existing
`uptimebar.BucketStats` (`Total` minus successful). Apply the rule uniformly
to hourly and daily buckets: it fixes the hourly "one failed minute = red
hour" cliff and is imperceptible on large buckets where a single sample
can't cross the thresholds anyway. Percentage thresholds otherwise apply
unchanged.

### 5. Dashboard UI

Add an "Availability thresholds" pair of numeric inputs to the status-page
edit form
(`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.edit.tsx`,
near the existing `showAvailability` toggle) with helper text stating the
defaults; empty = default. The form reads/writes `settings.availability`
(the nested payload shape stays an API detail — the UI is just two numeric
inputs). Follow the design-reference conventions
(`web/dash0/src/routes/orgs/$org/design-reference.tsx`). Not needed on the
create form — defaults are right for a new page.

### 6. Tests

Go (table-driven, `testify/require`, `t.Parallel()`):

- `availabilityToStatus` with custom thresholds: a pct that is green under a
  lax page and amber/red under a strict page (positive control: defaults
  unchanged when `settings` is `{}` or `availability` absent).
- PATCH semantics: `degraded >= up` rejected; unknown `settings` keys
  rejected; explicit `"availability": null` resets to defaults; a PATCH not
  mentioning `settings` leaves it untouched.
- Calibration guard: hourly bucket with 1 failure out of 60 → `degraded`;
  2 failures → `down` (positive control proving the guard doesn't mask real
  reds).
- Public payload exposes effective thresholds.

Playwright (dash0): settings form round-trip — set thresholds, save,
reload, values persist.

## Decisions

- **Column-vs-`settings` policy** (record for future specs): attributes that
  are queried, joined, indexed, or constrained (`slug`, `enabled`,
  `visibility`, `custom_domain`…) stay dedicated columns; display-only
  customization goes into the `settings` JSONB. Migrating today's display
  columns (`show_availability`, `history_days`, …) into `settings` is a
  possible follow-up, deliberately out of scope here.
- **Badges stay on the global 99.9/99 defaults.** They are check-scoped and
  can't inherit page settings without inventing a check→page mapping. At
  most, point their duplicated constants at a shared default — do not wire
  page thresholds into them.
- **Per-page, not per-resource or per-org.** Per-org is too coarse for the
  motivating use case; per-resource is config burden with no demand yet.
- The `up`/`degraded`/`down` wire vocabulary is unchanged — status0 needs no
  changes for the core feature.

## Open questions

- Should the public page render the thresholds anywhere (legend or tooltip,
  e.g. "target ≥ 99.9%")? The payload will carry them; UI can be a
  follow-up.
