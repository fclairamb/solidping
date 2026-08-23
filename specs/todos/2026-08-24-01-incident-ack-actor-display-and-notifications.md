---
model: opus
effort: high
---

# Incident acknowledgment shows no actor in the dashboard and notifies nobody

## Problem

Two related gaps around incident acknowledgment:

**1. The dashboard never says *who* acknowledged.** The incident detail page
(`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:860`, timeline at
`:1050-1055`) shows an "Acked" badge and an "Acknowledged" timeline entry with a
timestamp only. The `acknowledgedBy` field exists in the generated API types
(`web/dash0/src/api/hooks.ts:492`) but is never rendered — and even if it were, it
is a bare user UID with no resolution to a name/email
(`server/internal/handlers/incidents/service.go:2158-2159`, `:2239-2240`).

The attribution problem runs deeper than display:

- `incidents.acknowledged_by` is a FK to `users.uid`
  (`server/internal/db/postgres/migrations/001_v0_1_0.up.sql:521-522`), so it is
  only populated when the acker maps to a SolidPing user: dash0 UI
  (`server/internal/handlers/incidents/handler.go:259`, actor from JWT at `:192`),
  a magic link whose email matches a user (`service.go:2710`), or a linked
  Telegram chat (`service.go:2949`).
- Slack (`server/internal/integrations/slack/interactions.go:220` →
  `service.go:2918`), Discord (`integrations/discord/interactions.go:235` →
  `service.go:3058`) and phone DTMF acks
  (`server/internal/handlers/twiliocb/handler.go:304`) leave the column **NULL**.
  Their identity survives only in the `incident.acknowledged` event payload
  (`service.go:2834-2884`: `via`, `slack_user_id`/`slack_username`,
  `discord_user_id`/`discord_username`, `acknowledged_by_email`,
  `acknowledged_by_phone`, `acknowledged_by_telegram`, `note`).
- The dashboard event feed can't recover it either: its actor helpers read
  camelCase keys (`web/dash0/src/components/dashboard/event-display.tsx:365-368`)
  which don't match the snake_case keys the ack event writes.
- `acknowledgedBy` is absent from the OpenAPI spec — `IncidentDetail` declares
  `acknowledgedAt` only (`server/internal/app/openapi/openapi.yaml:9920`).

**2. Acking is silent to everyone except the channel it happened in.**
`EventTypeIncidentAcknowledged` is explicitly in the "no notifications" branch
(`service.go:1858`, comment at `:1905-1907`). Acking cancels pending
escalation/notification jobs (`service.go:2818` → `cancelPendingNotifications`)
and publishes realtime updates for the dashboard (`service.go:2816`), but sends
no outbound message. Only the originating surface reacts (e.g. Slack rewrites its
own message in place, `interactions.go:423`). Everyone who was paged about the
incident has no way to know a teammate already took it — the exact situation
acks exist for. There is a resolution-notice precedent to mirror:
`queueResolutionNotice` (`service.go:1927`,
`server/internal/jobs/jobtypes/job_incident_resolution_notice.go`).

## Proposal

### A. Attribution — expose and display the acker

1. **Backend**: enrich the incident API response with a resolved acker. Keep
   `acknowledgedBy` (user UID) but add a display-oriented shape, e.g.
   `acknowledgedByActor: { name, via }` derived from the user record when
   `acknowledged_by` is set, and from the `incident.acknowledged` event payload
   (`slack_username`, `discord_username`, `acknowledged_by_email`,
   `acknowledged_by_phone`, …) when it is not. Document both `acknowledgedBy`
   and the new field in `openapi.yaml` (today even `acknowledgedBy` is missing).
2. **Dashboard**: render "Acknowledged by {name} via {channel}" on the incident
   detail header/badge area and in the timeline entry (`incidents.$incidentUid.tsx:1050`).
   Fix `event-display.tsx` actor extraction to read the snake_case keys the ack
   event actually writes, so the events tab attributes the ack too.

### B. Ack notification — tell everyone who received the incident

3. On a successful ack transition (only on the real transition — the idempotent
   early-return at `service.go:2781` must not re-notify), queue an
   acknowledgment notice mirroring the resolution-notice mechanism: a new job
   type modeled on `job_incident_resolution_notice.go`, fanned out to the same
   destinations that received the open notification, with per-channel formatting
   in `server/internal/notifications/` ("✅ Acknowledged by {actor} via
   {channel}" + incident link).
4. Ordering matters: the ack already cancels pending jobs
   (`cancelPendingNotifications`, `service.go:2818`) — enqueue the ack notice
   *after* that cancellation so it isn't swept, and make sure unack
   (`service.go:3407`) doesn't leave a stale notice in flight.
5. Channels that already self-update in place (the Slack/Discord message whose
   button was pressed) should not get a redundant duplicate — skip the
   originating message's channel-destination or accept the duplicate
   deliberately; decide and test either way.

### Open questions

- Should the ack notice be per-destination opt-out (like some competitors) or
  always-on? Default: always-on, no new config surface in this spec.
- Unack: notify again, or stay silent? Default: silent (rare operator action),
  but the incident row/API must reflect the cleared state (already handled by
  `ClearAcknowledgedAt/By`).

### Resolved open questions

These transcribe the `Default:` decisions already recorded in the section above.
They are directives, not suggestions — implement exactly this.

- **Should the ack notice be per-destination opt-out (like some competitors) or
  always-on?**
  **Decision:** always-on. Do **not** add any new configuration surface in this
  spec — no per-destination opt-out setting, no new column, no new API field,
  no new UI toggle.
- **Unack: notify again, or stay silent?**
  **Decision:** silent — unack sends no notification of its own. The incident
  row and the API response must still reflect the cleared state (already
  handled by `ClearAcknowledgedAt/By`), and unack must not leave a
  previously-queued ack notice in flight.
- **Should the originating Slack/Discord channel receive a duplicate notice?**
  (Proposal item 5 explicitly delegates this.)
  **Decision:** implementer's call — pick one of the two options, implement it
  deliberately, and cover the chosen behavior with a test.

### Testing

- Table-driven service tests: each ack origin (web, magic link, Slack, Discord,
  Telegram, phone) yields a correct resolved actor in the API response.
- Notification tests: ack on an incident with multiple destinations enqueues
  exactly one notice per destination, none when already acked (idempotency), and
  the notice survives `cancelPendingNotifications`.
- Playwright: incident detail shows "Acknowledged by …" after acking in dash0.
