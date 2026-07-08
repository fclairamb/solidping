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

## Implementation Plan

### Ground truth gathered (exact file/line references)

- `server/internal/db/models/event.go`: 18 `EventType` constants total. Full list:
  `check.created/updated/deleted` (3), `incident.created/escalated/escalation_failed/
  resolved/reopened/acknowledged/unacknowledged/snoozed/unsnoozed` (9),
  `status_update.created/updated/deleted` (3), `org.activation.signup_completed/
  first_check_created/first_result_received/first_notification_configured/
  first_incident_paged` (5). `Event` struct has `IncidentUID *string`,
  `CheckUID *string`, `Payload JSONMap` (`map[string]any`).
- `server/internal/handlers/incidents/service.go`:
  - `emitEvent` (line ~1241) is the **single funnel** for created/escalated/
    escalation_failed(no)/resolved/reopened (single + group variants) — it takes
    `incident *models.Incident` (which has `CheckUID string` notnull) and does
    `event.IncidentUID = &incident.UID` but **never sets `event.CheckUID`**.
    Fix once here: add `event.CheckUID = &incident.CheckUID` right after
    `event.IncidentUID = &incident.UID` — enriches created/escalated/resolved/
    reopened (single + group) in one place.
  - `keyCheckUID`/`keyCheckSlug` const block (line 53-65) needs a new
    `keyCheckName = "check_name"`.
  - Payload call sites that already pass `keyCheckUID`/`keyCheckSlug` and need
    `keyCheckName` added: createIncident (~623), resolveIncident (~655),
    reopenIncident (~816), group escalate (~948), createGroupIncident (~1057),
    reopenGroupIncident (~1120), resolveGroupIncident (~1214). The escalate
    (single, ~459) site currently has NO check_uid/slug at all — add all three.
    Manual resolve (~2347-2352) has only `keyCheckUID` — add slug+name.
    All these sites already have `check *models.Check` in scope
    (`check.UID`, `check.Slug *string`, `check.Name *string` — store the
    pointers directly in the JSONMap, same pattern already used for Slug).
  - Ack (~1954), Unack (~2073), Snooze (~2163), Unsnooze (~2256) build the
    `Event` directly via `models.NewEvent` (not through `emitEvent`) and have
    ONLY `incident *models.Incident` in scope (no `check`). Fix: in each, add
    `event.CheckUID = &incident.CheckUID` and fetch
    `check, _ := s.db.GetCheck(ctx, orgUID, incident.CheckUID)` (best-effort,
    matches existing `_ =`-style lookups elsewhere in this file e.g. line 1654)
    then add `check_uid`/`check_slug`/`check_name` to the payload map (guard
    nil check — still set check_uid from `incident.CheckUID` even if the
    lookup fails/check was deleted).
  - `server/internal/jobs/jobtypes/job_escalation_step.go` `emitEscalationFailed`
    (line ~730) already sets `event.CheckUID = &incident.CheckUID` but its
    payload only has `reason`/`detail` — add `check_uid`/`check_slug`/
    `check_name` too. This function doesn't have `*models.Check` in scope —
    add a best-effort `jctx.DBService.GetCheck(ctx, incident.OrganizationUID,
    incident.CheckUID)` lookup, same nil-guard pattern.
- `server/internal/activation/activation.go`:
  - Extend `Emit` with a trailing variadic `extra ...models.JSONMap`. Merge
    each map's keys into `event.Payload` after setting `"source"` (so extra
    can't clobber source accidentally — merge order: source first, then
    extra overwrites only if extra explicitly sets "source", which no caller
    will do). Two reserved keys popped from the merged extra (not written to
    Payload) to set row-level UIDs: `_incident_uid` and `_check_uid` (both
    `string`) — if present and non-empty, set `event.IncidentUID`/
    `event.CheckUID` on the row before insert. Using reserved keys instead of
    a second variadic keeps the signature change to one new trailing param,
    so all 6 existing call sites (auth/service.go:2484, checks/service.go:1086,
    integrations/service.go:401 & 652, workers/service.go:298,
    job_notification.go:169) keep compiling unchanged — they just don't pass
    the new arg.
  - `integrations/service.go:401` (channel creation): pass
    `models.JSONMap{"channel_uid": conn.UID, "channel_name": conn.Name,
    "channel_type": string(conn.Type)}`. `conn` is in scope (`*models.Integration`,
    `Name string` notnull, `Type ConnectionType`, `UID string`).
  - `integrations/service.go:652` (Freebox pairing): same shape, `conn` also
    in scope there.
  - `job_notification.go:169` (first_incident_paged): pass
    `models.JSONMap{"_incident_uid": incident.UID, "_check_uid": check.UID,
    "check_name": check.Name, "check_slug": check.Slug}`. `incident` and
    `check` both already loaded in `Run` (lines 113, 119).
- No migration / backfill — confirmed no existing spec convention requires one
  for payload-shape-only changes; historical rows keep old payloads as-is.

### Backend implementation steps

1. `server/internal/db/models/event.go` — no changes needed (list confirmed
   complete/accurate already).
2. `server/internal/handlers/incidents/service.go`:
   - Add `keyCheckName = "check_name"` to the const block.
   - `emitEvent`: add `event.CheckUID = &incident.CheckUID`.
   - Add `keyCheckName: check.Name` to the 7 payload sites listed above that
     already have `check` in scope; add full `check_uid/slug/name` triplet to
     the single-escalate site (~459, needs `check.UID`/`check.Slug` added too
     — `check` is in scope there as it's a method param).
   - Ack/Unack/Snooze/Unsnooze: fetch check via `s.db.GetCheck`, set
     `event.CheckUID`, add check_uid/slug/name to payload.
3. `server/internal/jobs/jobtypes/job_escalation_step.go`:
   `emitEscalationFailed` — best-effort `GetCheck` lookup, add check payload
   fields.
4. `server/internal/activation/activation.go`:
   - Change `Emit` signature to accept `extra ...models.JSONMap`; implement
     merge + reserved-key UID extraction as described above.
5. `server/internal/handlers/integrations/service.go` — both `Emit` call
   sites: pass the channel extra payload.
6. `server/internal/jobs/jobtypes/job_notification.go` — pass incident/check
   extra payload + reserved UID keys.
7. Run `make fmt` (gofmt/goimports) after each file group; commit.

### Backend tests

- `server/internal/handlers/incidents/service_test.go` (or a new
  `service_check_enrichment_test.go` if the existing file is oriented around
  pure functions only — check first): table-driven test that drives a check
  through create → escalate → reopen → resolve (and ack/unack/snooze/unsnooze
  where those have their own test entry points) via the service's public
  methods against the in-memory sqlite `db.Service` (mirrors
  `activation_test.go`'s `sqlite.New(ctx, sqlite.Config{InMemory: true})`
  pattern), then asserts via `ListEvents` that every incident.* event row has
  non-nil `CheckUID` equal to the check's UID, and payload `check_uid`/
  `check_slug`/`check_name` all present and correct.
- `server/internal/activation/activation_test.go`: add
  `TestEmitWithExtraPayloadAndRefs` — call `Emit` with an `extra` map
  containing `_incident_uid`, `_check_uid`, and a domain field (e.g.
  `channel_name`), assert the stored event has `IncidentUID`/`CheckUID` set
  and `Payload["channel_name"]` present, `Payload` does NOT contain the
  reserved `_incident_uid`/`_check_uid` keys themselves.
- `server/internal/handlers/integrations/service_test.go`: extend/add a test
  asserting `first_notification_configured` event payload has `channel_uid`/
  `channel_name`/`channel_type` after `CreateIntegration`.
- Test for `first_incident_paged` payload enrichment: check
  `server/internal/jobs/jobtypes/job_notification_test.go` for an existing
  harness driving `NotificationJobRun.Run`; extend it to assert the emitted
  activation event's `IncidentUID`/`CheckUID`/payload.

### Frontend implementation steps

1. `web/dash0/src/components/dashboard/event-display.tsx`:
   - `getEventCheckName`: add fallback to `payload.check_slug` when
     `check_name` absent (currently only checks `check_name`). Rename
     internal logic but keep the exported name (both callers — dashboard and
     events.tsx — already use it) so the three-tier fallback
     (`check_name` → `check_slug` → caller supplies generic `links.check`)
     lives in one place.
   - Add `getEventIcon` case for `eventType.startsWith("org.activation.")` —
     distinct icon (e.g. `Rocket` or `PartyPopper` from lucide-react; check
     the design-reference page for an icon already used for
     "milestone/activation" semantics first, else pick a sensible distinct
     one) before the generic `Calendar` fallback.
   - Add a small shared helper `getEventIncidentLink(event, org, t)` (or
     inline at call sites — two call sites only) is optional; likely simplest
     to keep both link renders inline at each of the two call sites
     (dashboard + events.tsx) since they already both import from this file
     and the JSX differs slightly (Badge-wrapped table cell vs. plain li).
2. `web/dash0/src/components/dashboard/dashboard-page.tsx` `RecentActivityList`:
   - Render incident link (`event.incidentUid` → `/orgs/$org/incidents/$incidentUid`,
     label `t("links.incident")` from the `events` namespace) ALONGSIDE the
     check link, not instead of the description. New layout: when
     `event.incidentUid` is set, always show the incident link; when
     `event.checkUid` is set, show the check link right after it (same line
     or stacked — match existing Tailwind spacing conventions on this list
     item); only fall back to the plain `description` text when neither UID
     is present.
   - Compute check link label via the updated `getEventCheckName` (now
     falls back to `check_slug`); if that returns undefined (fully historical
     event, no name AND no slug) fall back to `tEvents("links.check")`.
   - Special-case `org.activation.first_notification_configured`: when
     `event.payload?.channel_name` is a non-empty string, render the
     description as "You have set up your first notification channel:" (i18n,
     see below) followed by the channel name; if `event.payload?.channel_uid`
     is also present, wrap the channel name in a `Link` to
     `/orgs/$org/integrations/$integrationUid`. Otherwise render the existing
     generic description unchanged (`descriptions.
     org.activation.first_notification_configured`).
3. `web/dash0/src/routes/orgs/$org/events.tsx`:
   - Already renders both incident + check links and already calls
     `getEventCheckName(event) ?? t("links.check")` — once
     `getEventCheckName` gains the `check_slug` fallback in step 1, this page
     needs NO further code change for the check-name fallback chain. Verify
     with a quick read after the shared-helper change lands; only touch this
     file if the ack/unack/snooze/unsnooze event types need to be added to the
     local `EventType`/`eventTypeValues` filter union (they currently aren't
     in the dropdown filter list — out of scope for this spec unless trivial;
     note as an observation, not a blocker, if left alone. Given the spec's
     acceptance criteria don't mention the filter dropdown, leave it
     untouched to avoid scope creep).
   - Apply the same "first_notification_configured shows channel name +
     link" treatment here too? Re-read spec: it only calls out
     `RecentActivityList` (dashboard) explicitly, and separately says the
     `/events` page "benefits from the same fallbacks (named check links via
     check_name/check_slug)" — that's the check-name fallback only, which is
     already inherited from the shared helper. Channel-name-in-description
     is a dashboard-card-specific requirement per the spec's wording
     ("description line" implies the dashboard's compact description slot);
     `/events` page doesn't render a description line at all today (no
     `descriptions.*` usage), so this is naturally out of scope there.
4. i18n — `web/dash0/src/locales/en/events.json`:
   - Add missing `types.*` keys (verified against the full 18-constant list,
     not just the spec's example list): `incident.escalation_failed`,
     `incident.reopened`, `incident.unacknowledged`, `incident.snoozed`,
     `incident.unsnoozed`, `status_update.created`, `status_update.updated`,
     `status_update.deleted`, `org.activation.signup_completed`,
     `org.activation.first_result_received`,
     `org.activation.first_incident_paged`. (`incident.opened` is a pre-existing
     key with NO matching backend constant — leave it alone, out of scope /
     possibly dead, not a gap to fix here.)
   - Add `descriptions.*` for the two activation entries above (matching the
     established sentence style) plus `org.activation.first_incident_paged`
     ("Your first incident notification was delivered.").
   - Update `descriptions."org.activation.first_notification_configured"`
     text stays as the fallback sentence (unchanged) — the channel-name
     variant is composed in JSX, not stored as a template string in i18n
     (no `Trans`/interpolation pattern currently used in this codebase for
     event descriptions; introducing one would be disproportionate to this
     spec).
   - New label text (English, adjust to house style): `incident.escalation_failed`
     = "Escalation Failed", `incident.reopened` = "Incident Reopened",
     `incident.unacknowledged` = "Incident Unacknowledged",
     `incident.snoozed` = "Incident Snoozed", `incident.unsnoozed` =
     "Incident Unsnoozed", `status_update.created` = "Status Update Posted",
     `status_update.updated` = "Status Update Edited", `status_update.deleted`
     = "Status Update Removed", `org.activation.signup_completed` =
     "Signup Completed", `org.activation.first_result_received` =
     "First Result Received", `org.activation.first_incident_paged` =
     "First Incident Paged".
5. Mirror the same new keys into `de`/`es`/`fr` `events.json` — this repo's
   established convention (verified: all 4 locales currently have 100% key
   parity) is all-locales-must-have-keys, so translate rather than leave
   English-only. Use reasonable native translations consistent with each
   locale's existing style for this domain (reuse "Incident"/"Vorfall"/
   "Incidente" per-locale word already established for the noun).
6. Design reference: check whether `design-reference.tsx` documents event
   rendering / activation icon patterns; if it has an "events" or "activity
   feed" showcase section, add the new icon/link pattern there per the
   CLAUDE.md mandate. If it doesn't already showcase this component, skip —
   don't introduce a new catalog section disproportionate to this spec.

### Frontend tests (Playwright E2E)

- Extend `web/dash0/e2e/dashboard.spec.ts`:
  - New test: seed a check + drive it to failure via API (existing helper
    pattern in this file, e.g. `mockDashboard` / direct API calls already
    used at line ~227) so an `incident.created` event exists with
    `incidentUid` + `checkUid` populated; assert the Recent Activity row
    renders BOTH a link to `/orgs/{org}/incidents/{uid}` and a link to
    `/orgs/{org}/checks/{uid}`, with the check link text equal to the
    check's name.
  - New test: create a notification channel via API, assert the
    `first_notification_configured` row's description includes the channel
    name and the channel name is a link to
    `/orgs/{org}/integrations/{channelUid}`.
  - Keep using `page.getByTestId` / `toHaveAttribute("href", ...)` per the
    existing file's conventions; reuse the `authenticatedPage` fixture.
- If local `:4000` devloop isn't in `SP_RUNMODE=test` (test org 401s), author
  the tests fully but do not attempt to force a run — report authored/not-run
  honestly rather than skipping.

### Verification order

1. `make fmt`
2. `make build-backend lint-back test` (Go)
3. `make build-dash0`
4. `cd web/dash0 && bun run lint` (no NEW errors in touched files)
5. `bun run test-dash` / Playwright, best-effort locally
6. Self-review against all 9 acceptance criteria + full 18-constant i18n
   coverage before merge.
