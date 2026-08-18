---
model: sonnet
effort: medium
---

# Matrix notification integration

## Problem

SolidPing has no way to deliver incident notifications into a [Matrix](https://matrix.org)
room. Matrix is the standard chat protocol of the self-hosted ecosystem (Element, Synapse,
Conduit, beeper), and the tools SolidPing competes with (Uptime Kuma, Gatus,
Healthchecks.io) all ship a Matrix notifier. For users who run their own infrastructure —
exactly SolidPing's audience — Matrix is often *the* team channel, and today they have to
route through a generic webhook plus a bridge to get alerts there.

There is currently zero Matrix support anywhere in the repo (no sender, no frontend type,
no docs).

## Proposal

Add `matrix` as an **org-level integration type** — the Slack/Discord/ntfy path, not the
Telegram-style instance-level direct channel. It is a near-clone of the ntfy sender
(`server/internal/notifications/ntfy.go`, ~200 lines): a stateless HTTP sender with three
settings, no OAuth, no webhook, no migration.

### Connection type

`matrix`

### Settings (JSONB, camelCase like ntfy's `NtfySettings`)

```json
{
  "homeserverUrl": "https://matrix.org",
  "accessToken": "syt_...",
  "roomId": "!abcdef:matrix.org"
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `homeserverUrl` | yes | Base URL of the homeserver's client-server API (e.g. `https://matrix.org`). Trailing slash tolerated. |
| `accessToken` | yes | Access token of a bot/dedicated user. **Secret** — must be listed in `connectionSecretFields` (`server/internal/crypto/credentials/conn_secrets.go:28`) so it lands in the encrypted `settings_private` envelope, never plaintext JSONB. |
| `roomId` | yes | Room ID (`!room:server`) or alias (`#room:server`). The bot must already be invited to and have joined the room. |

### Sending

Matrix Client-Server API v3, plain HTTP with a bearer token:

```
PUT {homeserverUrl}/_matrix/client/v3/rooms/{roomId}/send/m.room.message/{txnId}
Authorization: Bearer {accessToken}
```

- `txnId` must be unique per access token — use a fresh UUID per send.
- If `roomId` starts with `#`, resolve the alias first via
  `GET /_matrix/client/v3/directory/room/{alias}` (URL-encode the alias), then send to the
  resolved `!` ID. Resolve on every send — no caching, aliases rarely move and the extra
  GET is cheap.
- Body is an `m.room.message` event with both plain and HTML variants:

```json
{
  "msgtype": "m.text",
  "body": "[DOWN] api-health-check\nCause: connection timeout after 10s",
  "format": "org.matrix.custom.html",
  "formatted_body": "<strong>[DOWN] api-health-check</strong><br/>Cause: connection timeout after 10s"
}
```

Event → message mapping mirrors ntfy: `incident.created` → `[DOWN]`, `resolved` →
`[RECOVERED]`, `escalated` → `[ESCALATED]`, `reopened` → `[REOPENED]`, each with the check
name, cause/duration lines, and a link to the incident in the dashboard in the HTML
variant. HTML must be built with proper escaping of check names/causes (use
`html.EscapeString`), never `fmt.Sprintf` into markup.

### Error handling

| Response | Behavior |
|----------|----------|
| 200 | Success |
| 401 (`M_UNKNOWN_TOKEN`) | Permanent — bad/expired access token |
| 403 (`M_FORBIDDEN`) | Permanent — bot not in the room; surface a clear message ("invite the bot to the room") |
| 404 (`M_NOT_FOUND`) | Permanent — unknown room/alias |
| 429 (`M_LIMIT_EXCEEDED`) | Retryable — honor `retry_after_ms` from the body if present |
| 5xx | Retryable |

### Registration checklist (backend)

- Implement `Sender` (`server/internal/notifications/sender.go:63`) in a new
  `server/internal/notifications/matrix.go`.
- Register in `senderFactories()` (`server/internal/notifications/registry.go:83`).
- Add the `ConnectionType` const (`server/internal/db/models/integration.go:16`) — the
  capabilities `default:` branch already returns `{CanNotify: true}`, no edit needed
  there, but `models/integration_test.go` asserts capabilities per type and needs a row.
- Add to `validConnectionTypes` (`server/internal/handlers/integrations/service.go:460`).
- Add `accessToken` to `connectionSecretFields` (`conn_secrets.go:28`).
- No DB migration (type is a text column), no OpenAPI enum change, test-send endpoint
  (`POST .../integrations/:uid/test`) works via the registry for free.

### Frontend (dash0)

The usual five touch points plus locales — follow the design reference and the ntfy
form as the model:

- `ConnectionType` union + capabilities map: `web/dash0/src/api/hooks.ts:3603`.
- Icon + label: `web/dash0/src/components/integrations/integration-icon.tsx:24` (the
  `ICONS` record is an exhaustive `Record<ConnectionType, …>`; a generic
  `MessageSquare`-style lucide icon is fine, no brand asset needed).
- Settings form `case "matrix"` in
  `web/dash0/src/components/integrations/integration-form.tsx` (ntfy's case at `:413` is
  the template): homeserver URL text input (placeholder `https://matrix.org`), access
  token password input, room ID text input with help text "Room ID (!room:server) or
  alias (#room:server). Invite the bot to the room first."
- New-integration picker: `web/dash0/src/routes/orgs/$org/integrations.new.tsx:42`.
- Channel labels: `web/dash0/src/lib/channel-labels.ts:20`.
- Locales: `web/dash0/src/locales/{en,fr,de,es}/{common,integrations}.json`.

### Docs

- Add a Matrix section + availability-table row to
  `web/docs/docs/configuration/notifications.md`, covering: creating a bot user, getting
  an access token (Element → Settings → Help & About → Advanced, or
  `POST /_matrix/client/v3/login`), inviting the bot, finding the room ID.

### Testing

- Go table-driven tests beside the sender (mock HTTP server): request path/txnId
  uniqueness, alias resolution, auth header, HTML escaping, error classification
  (401/403/429-with-retry_after_ms/5xx), per-event formatting.
- Registry test row (`notifications/registry_test.go`) + capabilities row
  (`models/integration_test.go`).
- Playwright E2E modeled on `web/dash0/e2e/channels-webhook.spec.ts`: create a Matrix
  integration through the form, assert it lists with icon/label, edit and delete it.

### Out of scope

- Auto-joining the room on `M_FORBIDDEN` (nice-to-have later; for now the test-send
  surfaces the error with remediation text).
- End-to-end encrypted rooms (would require a crypto SDK; document as unsupported).
- Signal — tracked separately if wanted.
