---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Incident insights page: top failing checks, time to acknowledge, time to resolve

## Problem

The dashboard lists incidents one by one but answers none of the questions a team asks after a month of on-call:

- Which checks fail the most ?
- How long does it take us to acknowledge an incident ?
- How long do incidents last, and how many recover on their own ?

The data is already on the `incidents` row (`server/internal/db/models/incident.go:87-105`): `started_at`, `acknowledged_at`, `resolved_at`, `resolution_type`, `relapse_count`, `last_reopened_at`. Nothing aggregates it. No migration is needed.

## Proposal

### 1. Pure computation package

New package `server/internal/incidentinsights`, pure functions over `[]*models.Incident` (no DB access), so the uptime report can reuse it (spec `2026-11-13-02`).

`Compute(incidents, windowStart, windowEnd, now) Insights` returns:

- **`summary`**
  - `incidentCount`
  - `acknowledgedCount` and `ackRate` (0 to 1)
  - `timeToAcknowledge`: `{ medianSeconds, p90Seconds, sampleCount }`
  - `timeToResolve`: same shape, over resolved incidents
  - `byResolution`: one entry for `auto`, one for `manual`, one for `other`, each with `count`, `medianSeconds`, `p90Seconds`
- **`checks`**: one row per check, with `checkUid`, `incidentCount`, `downtimeSeconds`, `relapseCount` (sum), `lastIncidentAt`

Rules:

- **Time to acknowledge** = `acknowledged_at` minus the incident's current onset. The onset is `last_reopened_at` when it is after `started_at`, else `started_at`. Reopening clears `acknowledged_at` (`server/internal/handlers/incidents/service.go:1769-1773`), so the surviving ack belongs to the last episode. Same rule as `incidentClockFloor` (`service.go:1173`). Only incidents with a non-null `acknowledged_at` are in the sample.
- **Ack rate** = acknowledged incidents / all incidents in the window. It is always returned next to the ack time, because most incidents recover before anyone acks and the ack time alone flatters.
- **Time to resolve** = `resolved_at` minus `started_at`, resolved incidents only.
- **Resolution split**: `auto` = `ResolutionTypeAuto`, `manual` = `ResolutionTypeManual`, `other` = every other value (`incident.go:23-44`: expired, escalated, disabled, check_deleted, check_disabled). `other` is counted but left out of the overall `timeToResolve` median, since those closures say nothing about recovery.
- **Median and p90**, never the mean (one 6 hour outage wrecks a mean). Nearest-rank on the sorted sample. An empty sample returns `sampleCount: 0` and null durations, not zero.
- **Downtime per check**: reuse `availability.IncidentBlock` (`server/internal/handlers/availability/service.go:332`), which clips an incident to the window and counts an open one up to `now`.
- No per-person breakdown. `acknowledged_by` is not read.

### 2. API endpoint

`GET /api/v1/orgs/:org/incidents/insights?period=7d|30d|90d` (default `30d`), registered next to the other incident routes in `server/internal/app/server.go:1594`, before `/:uid`.

- Same read permission as `ListIncidents`.
- Loads incidents with `ListIncidentsFilter` (`incident.go:210`): `OrganizationUID`, `Since` = window start, `Kinds` = check outages only (no SLO burn alerts, no degraded incidents).
- Rolled-up (paging-suppressed) incidents count in the checks table and in time to resolve. They are left out of the acknowledge sample and of the ack rate, since nobody was paged.
- Page through the list. Stop at 5,000 incidents and return `truncated: true`.
- Checks table: top 20 by `incidentCount`, ties broken by `downtimeSeconds`. Each row carries the check name and slug so the page needs no second request.
- Unknown `period` answers 400 `VALIDATION_ERROR`.
- Add the endpoint to `server/internal/app/openapi/openapi.yaml`.

### 3. Dashboard page

New route `web/dash0/src/routes/orgs/$org/incidents.insights.tsx`, reached from an "Insights" button in the header of the incidents list (`web/dash0/src/routes/orgs/$org/incidents.index.tsx`).

- Period selector (7, 30, 90 days), stored in the URL search params.
- **Top failing checks** first: a table with check (link to the check page), incidents, downtime, relapses, last incident. Sortable client-side on the three numeric columns. Each incident count links to the incidents list filtered on that check.
- **Response times** below: three cards.
  - Time to acknowledge: median, p90, and "12 of 40 acknowledged".
  - Time to resolve: median, p90.
  - Resolution: count and median for auto-recovered and for manually resolved, count only for other.
- A card with an empty sample shows a dash and the reason ("No acknowledged incident in this period"), never `0s`.
- Empty state when the period has no incident.
- Truncation notice when `truncated` is true.
- Reuse the primitives of the design reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`). Usable on mobile: the table scrolls or stacks, no fixed widths.
- `data-testid` on the period selector, each card, each table row and the empty state.
- Durations are formatted with the helper the incident pages already use.

### 4. Docs and changelog

- A short section in the incidents page of `web/docs/docs/` describing the three numbers and how each is computed.
- A `CHANGELOG.md` entry (conventions: `wiki/conventions/changelog.md`).

## Tests

- `server/internal/incidentinsights/insights_test.go`, table-driven:
  - median and p90 on odd, even and single-element samples
  - empty input: zero counts, null durations
  - no acknowledged incident: `ackRate` 0, `timeToAcknowledge.sampleCount` 0
  - reopened incident: ack time is measured from `last_reopened_at`, not `started_at`
  - open incident: counted, absent from the resolve sample, downtime runs to `now`
  - incident started before the window: downtime clipped to the window
  - resolution split: `expired` and `check_deleted` land in `other` and stay out of the overall median
  - checks rows: sums of incidents, downtime and relapses per check, order and tie-break
- `server/internal/handlers/incidents/`: handler test for the endpoint
  - 401 without a token
  - 400 `VALIDATION_ERROR` on `period=1y`
  - incidents of another org are not counted
  - an SLO burn incident is not counted (positive control: the same test counts a check outage)
  - a rolled-up incident counts in the checks table and not in the ack rate
- `web/dash0/e2e/incident-insights.spec.ts`:
  - the page shows the failing check and its incident count after an incident is created
  - switching the period updates the URL and the numbers
  - an org with no incident shows the empty state
  - a mobile viewport has no horizontal page scroll

## To verify

- The exact name of the kind constant for check outages (`IncidentKindCheck` ?) near `incident.go:71`.
- How a group incident (`check_group_uid` set, `incident.go:108`) should be attributed: its `check_uid`, or each member check. Default to `check_uid`.
- Whether the incidents list already accepts a check filter in its URL search params, for the count links.
- Whether dash0 strings are translated (see `locale_pages_test.go`), and add the keys if so.
