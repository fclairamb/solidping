---
model: sonnet
effort: medium
---

# Zulip is the missing chat integration — and its topics fit incidents better than any channel we have

## Problem

Chat coverage today is Slack, Discord, MS Teams, Google Chat, Mattermost and
Matrix. Zulip is the notable gap: it is the chat tool of choice for many
open-source orgs and self-hosters (the audience ntfy/Matrix/Mattermost already
target), and its threading model — every message lives in a named **topic**
inside a stream — maps one-to-one onto "one thread per incident", which is the
interaction the Slack sender works hard to emulate with reverse threads.

## Proposal

Add a `zulip` connection type modeled on the Mattermost sender
([mattermost.go](../../server/internal/notifications/mattermost.go)), but
posting through Zulip's bot API rather than an incoming webhook, so all
lifecycle events of one incident land in one topic.

### Settings

| Key | Secret | Notes |
|---|---|---|
| `site_url` | no | e.g. `https://acme.zulipchat.com` or a self-hosted realm URL. |
| `bot_email` | no | The bot's email identity. |
| `api_key` | **yes** | The bot's API key. |
| `stream` | no | Stream (channel) name to post into. |

Sending is `POST {site_url}/api/v1/messages` with HTTP Basic auth
(`bot_email:api_key`) and a **form-encoded** body: `type=stream`,
`to=<stream>`, `topic=<topic>`, `content=<markdown>`.

⚠️ Zulip's API is form-encoded, not JSON — the same trap we hit with Slack's
list APIs, where a too-lenient fake server masked JSON bodies being silently
ignored. The test fake must reject a JSON body outright.

**Topic per incident**: derive the topic from the incident, e.g.
`<check name> (#<incident short ref>)`, and use the *same* topic string for
created / escalated / comment / acknowledged / resolved events so Zulip threads
the whole incident automatically. This derivation is the one real design point
of the spec; keep it deterministic from the payload (no stored state), and
truncate to Zulip's 60-character topic limit.

Content is Zulip Markdown — reuse the message-building helpers the Mattermost
sender uses rather than formatting from scratch.

### Touchpoints (same checklist as every channel)

- `ConnectionTypeZulip ConnectionType = "zulip"` in
  [integration.go:17](../../server/internal/db/models/integration.go) and the
  `CanNotify` branch of `CapabilitiesFor`
  ([integration.go:74](../../server/internal/db/models/integration.go)).
- New `server/internal/notifications/zulip.go` + entry in `senderFactories`
  ([registry.go:92](../../server/internal/notifications/registry.go)); default
  sender capabilities (comments and acks on) are right for a chat channel.
- Register `api_key` in `connectionSecretFields`
  ([conn_secrets.go](../../server/internal/crypto/credentials/conn_secrets.go));
  `site_url`, `bot_email` and `stream` stay public so the edit form can render
  them.
- `validConnectionTypes` in
  [service.go:471](../../server/internal/handlers/integrations/service.go).
- dash0: `case "zulip"` in
  [integration-form.tsx:424](../../web/dash0/src/components/integrations/integration-form.tsx)
  (site URL, bot email, API key, stream), icon in `integration-icon.tsx`, entry
  in [channel-labels.ts](../../web/dash0/src/lib/channel-labels.ts), card on
  `integrations.new.tsx`, locale keys in **all four** locales
  (`web/dash0/src/locales/{en,fr,de,es}/integrations.json`), verified by
  `bun run test:unit`.
- Docs: a Zulip section in
  [notifications.md](../../web/docs/docs/configuration/notifications.md),
  covering bot creation and where to find the API key.

### Tests

`zulip_test.go` with an `httptest` server asserting: Basic auth credentials,
form-encoded body (JSON body → test failure), stream/topic routing, that two
events of the same incident produce the identical topic string, the 60-char
topic truncation, and error surfacing on non-2xx / `"result": "error"`
responses.

### Out of scope

Two-way interaction (ack/comment from Zulip via an outgoing bot) — worth a
follow-up spec once the one-way channel exists; nothing here should preclude
it.
