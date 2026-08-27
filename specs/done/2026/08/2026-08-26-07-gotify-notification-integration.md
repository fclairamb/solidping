---
model: sonnet
effort: medium
---

# Self-hosters can't push SolidPing alerts to Gotify

## Problem

SolidPing deliberately courts the self-hosted / homelab audience: ntfy, Matrix,
Mattermost, Pushover and Freebox are all first-class integrations. Gotify — one
of the most popular self-hosted push servers, and a staple next to ntfy in that
crowd — is missing. Uptime Kuma, Gatus and Healthchecks all ship it, so its
absence is a visible checkbox gap for exactly the users the rest of the catalog
is built for.

## Proposal

Add a `gotify` connection type following the established ntfy pattern
([server/internal/notifications/ntfy.go](../../server/internal/notifications/ntfy.go)
is the closest template — Gotify is the same "POST to a server URL with a
token" shape).

### Settings

| Key | Secret | Notes |
|---|---|---|
| `server_url` | no | Required, no default — Gotify is always self-hosted. Trim trailing `/`. |
| `app_token` | **yes** | Gotify *application* token. |
| `priority` | no | Optional default priority 0–10 (Gotify scale), default `5`. |

Sending is `POST {server_url}/message` with the token in the `X-Gotify-Key`
header (never as a `?token=` query param — tokens don't belong in URLs) and a
JSON body `{"title", "message", "priority", "extras"}`. Use
`extras["client::notification"].click.url` to deep-link to the incident when
the payload carries an incident URL, mirroring how the ntfy sender enriches its
messages.

Priority mapping: incident created / escalated / reopened use the configured
priority; `incident.resolved` sends a low priority (e.g. `2`) so a recovery
never re-buzzes a phone like a page does.

### Touchpoints (all mechanical, existing patterns)

- `ConnectionTypeGotify ConnectionType = "gotify"` in
  [integration.go:17](../../server/internal/db/models/integration.go) and the
  `CanNotify` branch of `CapabilitiesFor`
  ([integration.go:74](../../server/internal/db/models/integration.go)).
- New `server/internal/notifications/gotify.go` sender + entry in
  `senderFactories`
  ([registry.go:92](../../server/internal/notifications/registry.go)). Default
  sender capabilities (comments and acks on) are correct for push.
- Register `app_token` in `connectionSecretFields`
  ([conn_secrets.go](../../server/internal/crypto/credentials/conn_secrets.go))
  so it is encrypted at rest; `server_url` stays public like the other endpoint
  URLs.
- `validConnectionTypes` in
  [service.go:471](../../server/internal/handlers/integrations/service.go).
- dash0: `case "gotify"` in the per-type settings form
  ([integration-form.tsx:424](../../web/dash0/src/components/integrations/integration-form.tsx)),
  icon in `integration-icon.tsx`, entry in
  [channel-labels.ts](../../web/dash0/src/lib/channel-labels.ts), card on
  `integrations.new.tsx`, and locale keys in **all four** locales
  (`web/dash0/src/locales/{en,fr,de,es}/integrations.json`). Run
  `bun run test:unit` — it is what catches a missing locale key.
- Docs: a Gotify section in
  [notifications.md](../../web/docs/docs/configuration/notifications.md).

### Tests

`gotify_test.go` with an `httptest` server, in the style of
`mattermost_test.go`: asserts the URL join, the `X-Gotify-Key` header (and its
absence from the URL), the priority mapping for created vs resolved, and that a
non-2xx response surfaces as an error. Note ntfy currently ships without a
sender test — do not copy that; Gotify gets one.
