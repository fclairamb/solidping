---
model: sonnet
effort: medium
---

# Add a Microsoft Teams notification integration

## Problem

SolidPing can notify Slack, Discord, Google Chat, Mattermost, ntfy, Opsgenie,
Pushover, Twilio, email, webhooks and web push — but not Microsoft Teams, which
is the dominant chat tool in a large share of the organizations we target.
Users in Teams-based companies currently have to fall back to email or a
generic webhook (whose payload Teams does not understand), so incidents don't
reach the channel where the on-call team actually lives.

## Proposal

Add a new `msteams` notification connection type, mirroring the existing
webhook-card senders (Google Chat / Mattermost are the closest analogs).

### Delivery mechanism — Workflows webhook + Adaptive Card

Microsoft retired the legacy Office 365 Connectors ("Incoming Webhook" with
MessageCard payloads); the supported path is a **Teams Workflow (Power
Automate)** with the "When a Teams webhook request is received" trigger. The
user creates the workflow in Teams (Workflows app → "Post to a channel when a
webhook request is received"), copies the resulting
`https://…logic.azure.com…/workflows/…` URL, and pastes it into SolidPing.

The sender POSTs the modern envelope:

```json
{
  "type": "message",
  "attachments": [{
    "contentType": "application/vnd.microsoft.card.adaptive",
    "content": { "type": "AdaptiveCard", "version": "1.4", "body": [ ... ] }
  }]
}
```

Do **not** implement the legacy MessageCard format — connector URLs are dead.
Treat any 2xx (Workflows typically answers `202 Accepted`) as success, ≥300 as
failure, same as `googlechat.go`.

### Card content

Follow the event-type mapping used by `GoogleChatSender.buildMessage`
([server/internal/notifications/googlechat.go:160](server/internal/notifications/googlechat.go#L160)):

- `incident.created` → **[DOWN] <check>**, accent color Attention (red)
- `incident.resolved` → **[RECOVERED] <check>** + "Recovered after <duration>", accent Good (green)
- `incident.escalated` → **[ESCALATED] <check>** + failure count, accent Attention
- `incident.reopened` → **[REOPENED] <check> (relapse #N)**, accent Attention
- default → **[UPDATE] <check>**

Body: a title `TextBlock` (size Large, weight Bolder, `color`
Attention/Good), a `FactSet` with Monitor (name + type), Cause
(`getFailureReason`), Failure Count — or Duration for resolved (reuse
`formatDuration` / `getCheckName` helpers from the package). Set
`"msteams": {"width": "Full"}` on the card so the FactSet isn't squeezed.

### Registration checklist (tracer: `opsgenie` / `googlechat`)

Backend:
- [server/internal/db/models/integration.go:20](server/internal/db/models/integration.go#L20) —
  add `ConnectionTypeMSTeams ConnectionType = "msteams"`; register it in
  `CapabilitiesFor` as `CanNotify: true`.
- `server/internal/notifications/msteams.go` — new `MSTeamsSender` modeled on
  `googlechat.go` (30 s timeout, `User-Agent: SolidPing`, sentinel errors for
  missing URL / failed webhook).
- [server/internal/notifications/registry.go](server/internal/notifications/registry.go) —
  add the `ConnectionTypeMSTeams` case.
- [server/internal/crypto/credentials/conn_secrets.go](server/internal/crypto/credentials/conn_secrets.go) —
  no entry needed: the webhook URL stays in public `settings` like Discord /
  Google Chat / Mattermost (documented in the comment there); extend that
  comment to mention msteams.
- [server/internal/handlers/severities/service.go:51](server/internal/handlers/severities/service.go#L51) —
  add `"msteams": true` to the notify-capable set.
- [server/internal/mcp/tools_integrations.go](server/internal/mcp/tools_integrations.go) —
  add `msteams` to `create_integration`.

Frontend (dash0):
- [web/dash0/src/api/hooks.ts:3313](web/dash0/src/api/hooks.ts#L3313) —
  add `"msteams"` to the `ConnectionType` union and the capability map (`NOTIFY`).
- [web/dash0/src/routes/orgs/$org/integrations.new.tsx:50](web/dash0/src/routes/orgs/$org/integrations.new.tsx#L50) —
  add to the type list.
- [web/dash0/src/components/integrations/integration-form.tsx:330](web/dash0/src/components/integrations/integration-form.tsx#L330) —
  add `case "msteams"` to the existing discord/googlechat/mattermost
  webhook-URL group. Add a short help hint telling the user to create a Teams
  Workflow ("Post to a channel when a webhook request is received") and paste
  the workflow URL — this is the #1 setup confusion with Teams.
- [web/dash0/src/components/integrations/integration-icon.tsx](web/dash0/src/components/integrations/integration-icon.tsx) —
  Teams icon.
- `web/dash0/src/locales/{en,fr,de,es}/integrations.json` — labels/hints.

### Settings key — decision

Use `webhook_url` end-to-end (frontend form key, Go struct tag), matching
Discord ([server/internal/db/models/integration.go:174](server/internal/db/models/integration.go#L174))
and the public-key rationale in `conn_secrets.go`. **Note:** the frontend
writes `webhook_url` for googlechat/mattermost too, but their senders
unmarshal `webhookUrl`
([server/internal/notifications/googlechat.go:70](server/internal/notifications/googlechat.go#L70),
[mattermost.go:84](server/internal/notifications/mattermost.go#L84)) — an
apparent pre-existing mismatch. Do not replicate it; msteams must use one key
(`webhook_url`) on both sides, verified by a round-trip test that builds the
settings map the way the dashboard form does.

### Tests

- `server/internal/notifications/msteams_test.go` — table-driven: one case per
  event type asserting the Adaptive Card envelope (type/attachments shape,
  title, accent color, facts), missing-URL error, non-2xx error including a
  `202 Accepted` success case; use `httptest.Server` like the webhook tests.
- Settings round-trip test using the exact key the frontend form writes.
- A dash0 E2E or component-level check that the msteams type appears in the
  new-integration flow and persists its URL (mirror existing coverage for
  googlechat/mattermost if any; keep it light if none exists).

### Out of scope

- Interactive Teams bot / OAuth app (the Slack-style two-way integration) —
  webhook-only for now.
- Legacy Office 365 Connector (MessageCard) support.
- Fixing the googlechat/mattermost `webhook_url`/`webhookUrl` mismatch —
  flagged separately.
