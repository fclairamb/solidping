# Recent activity: incident/check links, activation event rendering, notification channel name

## Problem

The "Recent activity" card on the org dashboard (`/dash0/orgs/$org`) shows events
with too little information to act on:

1. **Incident Created / Acknowledged / Resolved rows have no links.** The row is
   just a label and a relative time — the user cannot jump to the incident or to
   the affected check. Two combined causes:
   - `RecentActivityList` (`web/dash0/src/components/dashboard/dashboard-page.tsx`)
     only ever renders a **check** link, and only when `payload.check_name` is
     present. It never renders an incident link, even though `event.incidentUid`
     is populated for all `incident.*` events (the full `/orgs/$org/events` page
     does render both links).
   - Incident event payloads never contain `check_name`: `incident.created` /
     `incident.resolved` capture `check_uid` + `check_slug` only
     (`server/internal/handlers/incidents/service.go`), and
     `incident.acknowledged` captures neither (no `check_uid` in payload and no
     `CheckUID` on the event row) — so even the check-link path never fires.

2. **`org.activation.first_incident_paged` renders as a raw event type string.**
   `getEventLabel` falls back to the raw type when the i18n key is missing, and
   `types."org.activation.first_incident_paged"` is absent from
   `web/dash0/src/locales/en/events.json`. Several other types are also missing
   labels (`incident.unacknowledged`, `incident.reopened`, `incident.snoozed`,
   `incident.unsnoozed`, `incident.escalation_failed`, `status_update.*`,
   `org.activation.signup_completed`, `org.activation.first_result_received`).

3. **"First Notification Configured" doesn't say which notification channel.**
   `activation.Emit` (`server/internal/activation/activation.go`) hardcodes the
   payload to `{source}`; the emitters in
   `server/internal/handlers/integrations/service.go` (channel creation, Freebox
   pairing) have the created `Connection` in hand but can't pass its name/type/uid
   along.

## Proposal

### Backend — enrich event emission

- **All `incident.*` events carry the check.** Set `CheckUID` on the event row
  and include `check_uid`, `check_slug`, `check_name` in the payload for every
  incident lifecycle event — in particular `incident.acknowledged` (currently has
  none of them) and `incident.created` / `incident.resolved` (add `check_name`).
  Apply the same to the remaining lifecycle events (unacknowledged, escalated,
  reopened, snoozed, unsnoozed, escalation_failed) so rendering is uniform.
- **`activation.Emit` accepts extra context.** Extend the signature with an
  optional extra payload (e.g. variadic `models.JSONMap`) and optional related
  UIDs, merged into the standard `{source}` payload:
  - `org.activation.first_notification_configured`: payload gains
    `channel_uid`, `channel_name`, `channel_type` from the created `Connection`.
  - `org.activation.first_incident_paged`: set `IncidentUID`/`CheckUID` on the
    event and include `check_name`, so the milestone links to the incident that
    triggered the first page (emitter: `server/internal/handlers/incidents/service.go`).
- **No backfill.** Historical events keep their old payloads; the frontend must
  degrade gracefully when the new fields are absent (see fallbacks below).

### Frontend — richer rows (dashboard `RecentActivityList` + `/events` page)

- **Shared link rendering** in `web/dash0/src/components/dashboard/event-display.tsx`:
  - When `event.incidentUid` is set, render a link to
    `/orgs/$org/incidents/$incidentUid` (reuse the existing `links.incident`
    label).
  - When `event.checkUid` is set, render a link to `/orgs/$org/checks/$checkUid`
    named by `payload.check_name`, falling back to `payload.check_slug`, then to
    the generic `links.check` label — so pre-existing incident events (which only
    have `check_slug`) still get a named check link without backfill.
  - Dashboard incident rows therefore show both links (incident + check);
    check rows keep their current single check link.
- **`org.activation.first_incident_paged` gets a proper rendering:** i18n label
  (e.g. "First Incident Paged") + description (e.g. "Your first incident
  notification was delivered."), a fitting icon in `getEventIcon` for
  `org.activation.*` milestones (distinct from the plain Calendar default), and
  the incident/check links once the backend enrichment lands.
- **"First Notification Configured" shows the channel:** when
  `payload.channel_name` is present, the description line becomes
  "You have set up your first notification channel: **{channelName}**", with the
  channel name linking to `/orgs/$org/integrations/$integrationUid` when
  `payload.channel_uid` is present. Falls back to the current generic sentence
  for historical events.
- **Fill the i18n gaps:** add `types.*` (and `descriptions.*` where useful) for
  every backend `EventType` constant currently missing from `events.json`, so no
  event ever renders as a raw dotted type string.

## Acceptance Criteria

- [ ] Dashboard Recent activity: `incident.created` / `incident.acknowledged` /
      `incident.resolved` rows link to both the incident detail page and the
      check detail page.
- [ ] `incident.acknowledged` events are stored with `CheckUID` and
      `check_uid`/`check_slug`/`check_name` payload; all incident lifecycle
      events include `check_name`.
- [ ] `org.activation.first_incident_paged` renders with a human-readable label,
      description, and icon — never the raw type string — and links to the
      first incident for newly emitted events.
- [ ] `org.activation.first_notification_configured` events store
      `channel_uid`/`channel_name`/`channel_type`; the row shows the channel
      name and links to the integration edit page.
- [ ] Historical events (without the new payload fields) still render without
      errors: incident events fall back to `check_slug` for the check link
      name; activation events fall back to the generic description.
- [ ] Every `EventType` constant in `server/internal/db/models/event.go` has an
      i18n label in `events.json`.
- [ ] Go tests assert the enriched payloads/UIDs on emitted events
      (incident lifecycle + both activation milestones).
- [ ] Playwright E2E covers the dashboard Recent activity links (incident +
      check) and the notification-channel name rendering.
- [ ] The `/orgs/$org/events` page benefits from the same fallbacks (named check
      links via `check_name`/`check_slug`).
