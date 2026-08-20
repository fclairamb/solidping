# Split "Outage" (operational alert) from "Incident" (customer comms)

> **Superseded** (2026-08-19) by
> [`2026-08-19-08-auto-publish-incidents-to-status-pages.md`](../../../todos/2026-08-19-08-auto-publish-incidents-to-status-pages.md),
> which delivers the customer-facing incident + auto-publish via a
> publication overlay on the existing `incidents` table instead of the
> four-phase rename. The rename remains possible later as pure hygiene.

## Context

Today every failure goes through one table — `incidents` — that mixes
two distinct concerns:

- **Operational alert.** Detected automatically from check failures.
  Lives ~minutes to hours. Carries raw probe data, paging state, ack /
  snooze / resolve verbs, escalation cycle pointers. Audience: on-call
  engineers and the alerting subsystem.
- **Customer-facing communication.** Lives the duration of a
  user-visible event, sometimes days. Carries human-edited title /
  description / timeline updates, attaches to status pages, lives at
  a public URL. Audience: subscribers and end users.

Conflating them creates papercuts:

1. **Retention pressure.** Operational alerts churn fast (minor
   blips, transient timeouts). We don't want a year of those polluting
   the customer-facing record. The current single-table model forces
   one retention policy.
2. **Access control collision.** A customer-facing integration
   (status page subscriber feed) reads from the same row that the
   on-call's ack metadata lives on. Hiding internal fields means
   per-field gating instead of clean object boundaries.
3. **Editing semantics.** The on-call wants to *not* edit titles after
   the incident opens — the title is an ops snapshot. The status-page
   editor wants to *edit* titles to reflect customer-readable
   language. Same field, two contradictory pressures.
4. **Integration fan-out.** Hyperping demonstrates that splitting
   them is what enables a clean public webhook (`incidents.created`)
   that's distinct from the internal one (`outages.acknowledged`).
   Today our webhooks fire on the same row regardless.

Hyperping is the only competitor that ships this split — it's their
strongest design idea. See
[`wiki/competitors/hyperping/alerting.md §Outages-vs-Incidents`](../../docs/competitors/hyperping/alerting.md#outages-vs-incidents--the-two-table-model)
and
[`wiki/research/alerting-patterns.md`](../../docs/research/alerting-patterns.md).

## Goal

Two distinct objects:

- **Outage** — the operational alert. Renames today's `incidents`
  table to `outages`. Mostly the same shape: `state` (active /
  acknowledged / snoozed / resolved), `startedAt`, `acknowledgedAt`,
  `resolvedAt`, `pagingSuppressed`, `causedByIncidentUid` (now
  `causedByOutageUid`), check / region / group references, raw
  metadata.
- **Incident** — the customer-facing object. New, smaller table.
  Carries `title`, `description`, `state` (investigating / identified
  / monitoring / resolved), an ordered `updates[]` array with
  per-update `body` + `timestamp`, attaches to status pages and
  status-page resources, lives at a public URL.

An outage may have **zero, one, or many** linked incidents. An
incident may aggregate **one or many** outages (e.g. a region-wide
outage on the cloud provider that affects four of our checks rolls up
into one customer-facing incident). The relationship is N:M via a
join table `outage_incidents`.

Notifications fork:

- Channel notifications (Slack, email-as-channel, webhook, etc.) fire
  on **outage** lifecycle (`outage.opened`, `outage.acknowledged`,
  `outage.resolved`). On-call engineers consume these.
- Status-page subscriber notifications and the public outgoing
  webhook fire on **incident** lifecycle (`incident.published`,
  `incident.updated`, `incident.resolved`). Subscribers consume
  these.

A new incident is created either:

1. Manually by an operator (current dashboard workflow for status
   pages).
2. Automatically from an outage when (a) the operator clicks
   "Publish to status page" on the outage, or (b) the check has the
   `autoPublishIncident: true` setting, in which case the incident is
   created at outage-open time with a templated title.

## Approach

This is a multi-PR, multi-week effort. Sequencing matters because the
existing `incidents` table holds running production data and the
existing `incidents.*` API is consumed by the dashboard, MCP, CLI,
and external integrations.

### Phase 1: rename incidents → outages (additive, fully back-compat)

PR-1 introduces the *terminology* without splitting the data. The
table is renamed to `outages`, all Go types/packages rename to
`Outage`, the routes get `/outages` paths added next to `/incidents`
(both serve the same handlers, identical responses).

The `Incident` type and `/incidents` paths *stay alive as aliases* —
they read and write the same `outages` table. External clients on
the old API keep working.

PR-1 changes:
- Migration: `ALTER TABLE incidents RENAME TO outages`. Foreign-key
  columns named `incident_uid` rename to `outage_uid` where they
  reference this table (events.incident_uid → events.outage_uid,
  jobs config field `incidentUid` → `outageUid`). Bun ORM tags
  updated.
- Backend: `server/internal/handlers/incidents/` →
  `server/internal/handlers/outages/`. Type `Incident` → `Outage`.
  Methods, error codes, all rename mechanically.
- API: routes register both `/orgs/:org/outages*` (canonical) and
  `/orgs/:org/incidents*` (alias) pointing at the same handlers.
  Response shapes unchanged for both paths.
- Frontend: `useIncidents` / `useIncident` etc. keep their TS names
  but now hit `/outages` URLs. `Incident` TS type aliased to a new
  `Outage` type alias for one release.
- MCP: tool names like `list_incidents` keep working; new
  `list_outages` etc. surface as the canonical names. Both call
  the same handlers.
- Docs: add deprecation note pointing at `/outages`.

This phase is **purely a rename**. No new behavior, no schema changes
beyond the table name. Reverts cleanly.

### Phase 2: introduce the new Incident type for customer comms

PR-2 introduces the new `incidents` table — the customer-facing one.
Distinct from the renamed `outages` table.

New tables:

```sql
CREATE TABLE customer_incidents (
    uid VARCHAR(36) PRIMARY KEY,
    organization_uid VARCHAR(36) NOT NULL,
    slug TEXT NOT NULL,                 -- URL-friendly per-org unique
    title TEXT NOT NULL,                -- customer-readable, editable
    state TEXT NOT NULL DEFAULT 'investigating',  -- investigating | identified | monitoring | resolved
    severity TEXT,                       -- minor | major | critical | maintenance (optional, status-page-driven)
    started_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP NULL,
    is_published BOOLEAN NOT NULL DEFAULT false,
    auto_published_from_outage_uid VARCHAR(36) NULL,  -- back-pointer
    created_at, updated_at, deleted_at TIMESTAMP
);

CREATE TABLE customer_incident_updates (
    uid VARCHAR(36) PRIMARY KEY,
    incident_uid VARCHAR(36) NOT NULL,
    type TEXT NOT NULL,                  -- investigating | identified | update | monitoring | resolved
    body TEXT NOT NULL,
    posted_at TIMESTAMP NOT NULL,
    posted_by_uid VARCHAR(36),
    created_at TIMESTAMP NOT NULL DEFAULT NOW
);

CREATE TABLE outage_incident_links (
    outage_uid VARCHAR(36),
    incident_uid VARCHAR(36),
    PRIMARY KEY (outage_uid, incident_uid)
);
```

(Names use `customer_incidents` to disambiguate from the alias path
`/api/v1/orgs/:org/incidents` that still points at `outages`. The
public-facing API path for the new type is
`/api/v1/orgs/:org/status-incidents` or `/incidents/v2` —
disambiguation handled in PR-3.)

The new type:
- `CustomerIncident` Go type.
- Service in `server/internal/handlers/customerincidents/` (or merge
  into `statuspages/` since they're so closely tied).
- Updates are append-only; never modify a posted update, only post a
  new one (matches Hyperping and BetterStack).

### Phase 3: split the API surface and decide the canonical name

PR-3 makes the cut: the new `/incidents` path serves the customer
type; `/outages` serves the operational type; the alias `/incidents`
that pointed at `/outages` (added in PR-1) is removed.

This is the breaking-change edge. Schedule it for a major version
bump or a major release-note.

API after PR-3:

| Path | Object | Audience |
|---|---|---|
| `/api/v1/orgs/:org/outages*` | `Outage` (operational) | On-call, MCP, internal |
| `/api/v1/orgs/:org/incidents*` | `CustomerIncident` (public-facing) | Status-page editors |
| `/status-pages/:org[/:slug]` | reads `customer_incidents` | Public subscribers |

External webhooks fork:

- `outage.opened`, `outage.acknowledged`, `outage.snoozed`,
  `outage.resolved` → fired by the incident-service, consumed by
  channels.
- `incident.published`, `incident.updated`, `incident.resolved` →
  fired by the status-page-incident-service, consumed by the public
  webhook + subscriber email/slack distribution.

### Phase 4: auto-publish from outage

PR-4 adds the optional auto-publish path. A check (or a check group)
gains an `autoPublishIncident: true` setting. When an outage opens
on such a check and the outage is *not* paging-suppressed, a
`CustomerIncident` is created automatically with a templated title
("API is experiencing issues") and a single "investigating" update.
The link in `outage_incident_links` lets the dashboard show "this
outage is published as Incident #N".

Operators can also manually link / unlink outages from incidents
via `POST /incidents/:uid/outages/:outageUid` and the matching
DELETE.

## Files affected

The blast radius is wide; the per-PR file lists are extensive and
mostly mechanical. Summary:

### Phase 1 (rename)
- `server/internal/db/migrations/NNN_rename_incidents_to_outages.{up,down}.sql`
- `server/internal/db/models/incident.go` → `outage.go` with rename
- `server/internal/handlers/incidents/` → `outages/`
- `server/internal/jobs/jobtypes/job_notification.go`,
  `job_escalation_step.go` — config-key rename
  (`incidentUid` → `outageUid`) but keep accepting the old key for
  backward compat for a release
- All test fixtures
- `server/internal/app/server.go` — register both route groups
- `web/dash0/` — hooks, components, routes (stay on `Incident` type
  alias for one release, hit `/outages` URL)
- `server/internal/mcp/tools_incidents.go` →
  `tools_outages.go` (with old tool names aliased)
- `wiki/api-specification.md` — both paths documented; `/incidents`
  flagged deprecated

### Phase 2 (new type)
- New migration files
- `server/internal/db/models/customer_incident.go`,
  `customer_incident_update.go`
- `server/internal/handlers/customerincidents/{service,handler}.go`
  (or extend `statuspages/`)
- `server/internal/db/{sqlite,postgres}/customer_incident.go`
- API routes mounted at the chosen path (TBD in PR-3)
- Frontend: new type, hooks, route(s) for status-page-side incident
  editor
- Public webhook subsystem additions

### Phase 3 (cutover)
- Drop the alias `/incidents` paths (the ones added in PR-1 that
  pointed at outages)
- The new `/incidents` paths now serve `CustomerIncident`
- MCP: rename tools cleanly (`list_incidents` now means
  customer-facing; old behavior moves to `list_outages` only)
- Frontend type rename (`Incident` → `Outage` for what was the
  operational type; new `Incident` for the customer type)
- Docs major-version note

### Phase 4 (auto-publish)
- `Check.autoPublishIncident` boolean
- Outage-open hook fires the auto-publish if set
- Title template per check type (admin-editable in a system param)
- Frontend toggle in the check form
- Tests covering: outage opens → incident appears with the right
  link; outage acknowledges → no incident change; manual incident
  resolve doesn't touch the outage state

## Out of scope

- **Migrating existing data into customer-incidents.** Existing
  status-page entries that today use the conflated `incidents` table
  stay on the old shape; they're effectively grandfathered as outages
  with `isPublic = true`. The cutover only affects new incidents
  created post-PR-3.
- **Localized incident updates.** Hyperping localizes to ru/nl/pl/se/etc.
  Out of scope for v1; English only. Add the localization layer in a
  follow-up.
- **Subscriber notification delivery for incidents.** Spec'd
  separately (the existing "status-page subscribers" tier-2 roadmap
  item).
- **AI-suggested incident titles or copy.** Manual only.
- **Severity vocabulary alignment with the operational severity
  primitive** (spec
  [`2026-05-08-03-severity-primitive-for-escalation-steps.md`](2026-05-08-03-severity-primitive-for-escalation-steps.md)).
  These are different concepts (`Severity` for escalation = which
  channels fire; `severity` on a CustomerIncident = severity badge on
  the public page). Keep them in distinct vocabularies.

## Verification

Per-phase, since they ship across releases.

### Phase 1 acceptance
- All existing `/api/v1/orgs/:org/incidents*` calls return identical
  responses to `/api/v1/orgs/:org/outages*`.
- Existing dashboard works unchanged.
- Existing webhooks fire on the same triggers with the same payload
  shapes (TS field name `incidentUid` kept for one release alongside
  the new `outageUid` for back-compat in webhook payloads).
- Full test suite green.

### Phase 2 acceptance
- A `CustomerIncident` can be created via the new endpoint.
- Posting updates appends to `customer_incident_updates` rows.
- The status-page render at `/status-pages/:org/:slug` reads the new
  table when the page is migrated; otherwise reads the legacy
  `incidents` table (back-compat).

### Phase 3 acceptance
- The breaking change is documented and dated.
- An outage and a customer-incident with the same UID are clearly
  different rows in different tables.
- `outage.resolved` does not auto-resolve a linked customer-incident
  (that's a Phase 4 question).

### Phase 4 acceptance
- A check with `autoPublishIncident=true` opens an outage; a
  customer-incident appears within 5 seconds with the templated
  title.
- The incident timeline shows one initial "investigating" update.
- The link `outage_incident_links` joins the two.

## Implementation Plan

Each phase is its own multi-PR effort; this spec covers the *design*.
Follow-up sub-specs land per phase as they reach implementation.

1. **Phase 1 — rename**: own sub-spec
   `2026-05-NN-rename-incidents-to-outages.md`. Single big rename
   with API alias for back-compat. ~1 week.
2. **Phase 2 — new type**: own sub-spec
   `2026-05-NN-customer-incident-type.md`. Tables, model, service,
   internal API. ~2 weeks.
3. **Phase 3 — API cutover**: own sub-spec
   `2026-05-NN-incidents-api-cutover.md`. Breaking change; major
   release. ~1 week + customer comms.
4. **Phase 4 — auto-publish**: own sub-spec
   `2026-05-NN-auto-publish-incident-from-outage.md`. The
   per-check toggle + title templates. ~1 week.

Don't start Phase 2 until Phase 1 is in production and stable;
don't start Phase 3 until external clients have had a release cycle
to consume the new `/outages` paths; don't start Phase 4 until
Phase 3 has settled.

This spec stays in `specs/todos/` until all four phases ship and
the audit confirms each one is clean.
