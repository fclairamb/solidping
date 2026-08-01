---
model: sonnet
effort: medium
---

# Google Chat / Mattermost senders read `webhookUrl` but the dashboard saves `webhook_url`

## Problem

Notifications for Google Chat and Mattermost integrations created via the
dashboard almost certainly never send: the form and the senders disagree on
the settings key.

- The dash0 form writes `webhook_url` for discord/googlechat/mattermost — the
  shared `UrlPanel` case calls `update("webhook_url", v)`
  ([web/dash0/src/components/integrations/integration-form.tsx:330](web/dash0/src/components/integrations/integration-form.tsx#L330)).
- [server/internal/crypto/credentials/conn_secrets.go:22](server/internal/crypto/credentials/conn_secrets.go#L22)
  documents the same: "the `webhook_url` of Discord / GoogleChat / Mattermost
  stay in the public settings".
- But both senders unmarshal camelCase `webhookUrl`:
  [server/internal/notifications/googlechat.go:70](server/internal/notifications/googlechat.go#L70)
  and [mattermost.go:84](server/internal/notifications/mattermost.go#L84)
  (Mattermost's optional `channel`/`username`/`iconUrl` keys are also camelCase
  and are not exposed by the form at all).

Go's `encoding/json` matches keys case-insensitively but does **not** ignore
underscores, so `webhook_url` never populates `webhookUrl`. The senders then
fail `parseSettings` with "webhook URL not configured" for every
dashboard-created integration. Neither sender has a unit test
(`googlechat_test.go` / `mattermost_test.go` don't exist), which is how this
shipped.

Discord is unaffected: `models.DiscordSettings` uses `json:"webhook_url"`
([server/internal/db/models/integration.go:174](server/internal/db/models/integration.go#L174)).

Related inconsistency found while tracing: the MCP `create_integration` tool's
schema docstring tells callers to pass `{"webhookUrl": "https://..."}` for the
`webhook` type ([server/internal/mcp/tools_integrations.go:53](server/internal/mcp/tools_integrations.go#L53)),
but the webhook sender actually reads `Settings["url"]`
([server/internal/notifications/webhook.go:140](server/internal/notifications/webhook.go#L140))
— settings pass through verbatim, so an MCP caller following the docs creates
a broken webhook integration too.

## Proposal

1. **Prove the bug first**: a failing table-driven test that builds the
   settings map exactly as the dashboard form does
   (`{"webhook_url": "https://..."}`) and runs it through
   `GoogleChatSender.parseSettings` and `MattermostSender.parseSettings`,
   asserting the URL is parsed (it currently errors).

2. **Align on `webhook_url`** — it matches Discord, the conn_secrets.go
   comment, and what every existing dashboard-created row already stores.
   Change the struct tags in `googlechat.go` and `mattermost.go` to
   `json:"webhook_url"` (and Mattermost's `iconUrl` → decide: keep camelCase
   since nothing writes it today, or align to `icon_url` for consistency —
   either way add a test).

3. **Backward compatibility for `webhookUrl` rows**: some rows may exist with
   the camelCase key (e.g. created via raw API calls following the MCP-style
   docs). Accept both on read — after unmarshal, if `WebhookURL` is empty,
   fall back to the `webhookUrl` map key (a small shared helper both senders
   use). Prefer this over a data migration: it is simpler, and the settings
   JSONB has no schema version to hang a migration on. Regression-test both
   spellings for both senders.

4. **Fix the MCP docstring** in
   [server/internal/mcp/tools_integrations.go:53](server/internal/mcp/tools_integrations.go#L53):
   the `webhook` example must be `{"url": "https://..."}` to match
   `webhook.go:140`. While there, check the slack example's `webhookUrl`
   claim against what the Slack sender actually reads and correct if wrong.

5. **Tests**:
   - New `googlechat_test.go` / `mattermost_test.go`: settings round-trip for
     both key spellings, missing-URL error, and an `httptest.Server`-backed
     send asserting the outgoing payload (these senders currently have zero
     coverage).
   - A positive control: the same dashboard-shaped map through
     `DiscordSender.parseSettings` (already-working path) so the test file
     proves the form→sender contract, not just the two fixed types.

## Out of scope

- The Microsoft Teams integration (specs/todos/2026-08-02-01) — it must use
  `webhook_url` from day one and is specced separately.
- Exposing Mattermost's optional `channel`/`username`/`iconUrl` fields in the
  dashboard form.
