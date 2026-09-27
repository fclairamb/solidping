---
effort: medium
---

# Incidents list: show the full title, the incident kind, and filter by kind

## Problem

On `/orgs/$org/incidents` (`web/dash0/src/routes/orgs/$org/incidents.index.tsx`) two things
make the list hard to read:

1. **The title is cut to ~26 characters.** The Incident cell is `max-w-0`
   (`incidents.index.tsx:317`) inside an auto-layout table, so it gets whatever width is
   left, while Started / Duration / Failures (`:297-303`, `:411-427`) split the rest evenly
   and mostly hold whitespace. The link itself truncates (`:344`). Titles are generated as
   - check outage: `"<slug> is down"` (`server/internal/handlers/incidents/service.go:1745`)
   - degraded: `"<slug> is degraded: 3 failures in the last 10 probes (70.0%)"` or
     `"… 7 of the last 10 probes were slower than 800ms"`
     (`server/internal/handlers/degradedeval/service.go:345`)
   - SLO burn: `"Fast burn: <SLO> error budget burning at 14.4x"`
     (`server/internal/handlers/sloalerts/service.go:387`)

   With a slug like `region-heartbeat-lauterbourg`, the words that say *what happened*
   start around character 30, exactly where the cell truncates. Every row reads
   `region-heartbeat-lauter…`.

2. **The incident kind is invisible.** `IncidentDetail.kind` is already returned by the list
   API (`IncidentResponse.Kind`, `server/internal/handlers/incidents/service.go:2170`;
   `web/dash0/src/api/hooks.ts:685`), but the page never reads it. The status dot only
   encodes active (red, pulsing) vs resolved (green) (`incidents.index.tsx:319-332`), so an
   active degraded incident looks exactly like an outage.

   Both kinds occur on the same check: a degraded incident resolves with
   `resolutionType = "escalated"` when a real outage opens on that check, and the outage's
   `causedByIncidentUid` points back at it (`server/internal/db/models/incident.go:26-31`).

3. **There is no way to list only one kind.** The DB filter already supports it
   (`ListIncidentsFilter.Kinds`, `server/internal/db/models/incident.go:202-207`, empty means
   all), but the HTTP handler never parses a `kind` parameter
   (`server/internal/handlers/incidents/handler.go:63-130`) and `ListIncidentsOptions`
   (`service.go:2134`) has no field for it.

## Decisions

Taken from the proposals mockup (https://claude.ai/artifact/NJ79WPDvn1dnHjmiL4j2YG,
option **B**):

- Layout B: kind chip, full title, no Check column, merged When column.
- Dropping the Check column is accepted: the title already starts with the slug and the
  check's display name moves under the title.
- Add a kind filter to the page, backed by a new `kind` query parameter on the list API.
- Out of scope: a structured `summary` field that would let the list show "check name,
  then reason" (option C in the mockup). That can be a follow-up spec.

## Proposal

### 1. Backend: `kind` filter on `GET /api/v1/orgs/{org}/incidents`

- `parseListIncidentsOptions` (`handler.go:63`) reads `kind`, comma-separated, singular
  name per the REST conventions: `?kind=degraded` or `?kind=check,degraded`.
- Allowed values: `check`, `degraded`, `slo_burn` (the `models.IncidentKind*` constants).
  Any other value answers `400 VALIDATION_ERROR`. Empty or absent means all kinds, which
  is today's behaviour.
- Add `Kinds []string` to `ListIncidentsOptions` and pass it into
  `ListIncidentsFilter.Kinds`. No DB change: both backends already honour `Kinds`.
- Document the parameter in `server/internal/app/openapi/openapi.yaml` (`/incidents` list,
  next to `state`) and regenerate the Go client if it is generated from it
  (`go generate ./pkg/client/...`).
- Handler test (table-driven): absent, single value, two values, unknown value → 400.
  Service/DB test proving the filter actually narrows: seed one incident of each kind,
  assert `kind=degraded` returns exactly the degraded one and `kind=check,slo_burn` the
  other two (the positive control is the unfiltered call returning all three).

### 2. Frontend: kind chip

- New shared component, e.g. `web/dash0/src/components/shared/incident-kind-chip.tsx`:
  - `check` → **Down**, red, lucide `CircleArrowDown`
  - `degraded` → **Degraded**, amber, `Activity`
  - `slo_burn` → **SLO burn**, violet, `Flame`
  - Active incident: tinted fill + border. Resolved: transparent fill, neutral border, icon
    and label keep the kind colour. So kind and state read independently.
  - Unknown/missing kind renders as `check` (every pre-existing row is `check`).
- Add it to `web/dash0/src/routes/orgs/$org/design-reference.tsx` with all six
  kind × state combinations.
- Carries `data-testid="incident-kind-chip"` and the row carries
  `data-incident-kind={kind}`.

### 3. Frontend: row layout (`incidents.index.tsx`)

Columns become **Incident · When · Failures**:

- **Incident** cell, no `max-w-0`:
  - Line 1: kind chip (replaces the status dot), `#n`, then the existing badges (snoozed,
    acked, relapse, flapping, rolled-up). Below `sm`, the relative start time sits at the
    right end of this line.
  - Line 2: the title, full text, wrapping. No `truncate`, no line clamp.
  - Line 3 (`sm` and up): the check display name as a muted link to the check
    (`checkName || checkSlug`, same order spec 2026-08-28-11 fixed).
- **When** (`w-px whitespace-nowrap`): `<TimeAgo>` start time (hidden below `sm`, since it
  moves to line 1), then either `ongoing · 8m` in the kind colour (active) or
  `✓ lasted 48m` in muted green (resolved). Reuses `IncidentDuration`.
- **Failures** (`md` and up, `w-px`, right-aligned): unchanged content.
- **Escalation**: a resolved row with `resolutionType === "escalated"` shows an
  `escalated` badge. When another row on the loaded page has `causedByIncidentUid` equal to
  this row's uid, the badge reads `escalated → #98` and links to that incident.
- `GroupHeaderRow` (`:120`) `colSpan` goes from 5 to 3.
- Must stay fully usable on phone (see the Phone view in the mockup).

### 4. Frontend: kind filter

- New `Select` next to the state filter: All kinds / Down / Degraded / SLO burn. Same
  pattern as the state filter (`:202-231`), `data-testid="incidents-kind-filter"`.
- URL search param `kind` in `validateSearch` (`:52`): one of `check | degraded |
  slo_burn`, undefined when absent so a clean URL stays clean. Changing it keeps the other
  search params (same `search: (prev) => …` pattern as the state filter).
- `useIncidents` (`hooks.ts:~2129`) gains `kind?: string`, sent as `kind`, and included in
  the query key so switching refetches.
- The empty state (`:447`) gets a kind-aware message when the filter is set
  ("No degraded incidents").

### 5. Locales

New keys in all six dash0 locales (`web/dash0/src/locales/*/incidents.json`): kind labels,
kind filter labels, `when.ongoing`, `when.lasted`, `escalated`, `escalatedTo`, the
kind-aware empty state. `bun run test:unit` must stay green.

### 6. Tests

- Update `e2e/incidents-list-check-name.spec.ts`: the check display name is still visible
  in the row (now under the title) and still wins over the slug.
- New E2E (mocked list, same style as `incidents-list-check-name.spec.ts`): one active
  `check`, one resolved `degraded` with `resolutionType: "escalated"` whose uid is the
  `causedByIncidentUid` of the active one, one resolved `slo_burn`. Assert:
  - each row's `data-incident-kind` and chip label;
  - a 90-character title is fully visible (element not clipped: `scrollWidth <=
    clientWidth` on the title element), with a control row using a short title;
  - the escalated row shows `escalated → #<n>` linking to the outage;
  - picking "Degraded" in the kind filter puts `kind=degraded` in the URL and in the
    request query string.
- Existing `incident-group-header.spec.ts` and `incidents.spec.ts` keep passing.
- Mobile check at 375px: no horizontal scroll on the page, chip + number + title + time
  visible on every row.
