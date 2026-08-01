---
model: opus
effort: high
---

# Microsoft Teams bot integration (Slack-grade, two-way)

## Problem

The planned `msteams` webhook integration
(specs/todos/2026-08-02-01-microsoft-teams-integration.md) is one-way: cards
into a channel, nothing back. Slack users get much more from
[server/internal/integrations/slack/](server/internal/integrations/slack/):
install once, pick destination channels from the dashboard, @mention the bot
to manage checks and list incidents, get threaded incident updates, and have
the connection survive channel changes. Teams-based organizations — a large
share of our target market — should get the same experience.

This is a **separate integration** from the webhook one: its own connection
type, its own setup flow. The webhook type stays for users who want zero-infra
notifications.

## Proposal

New connection type `msteams-bot`, implemented as a new package
`server/internal/integrations/msteams/` modeled file-for-file on the Slack
package where the concepts transfer. Bot Framework (Azure Bot) is the
underlying platform.

### Slack → Teams capability map

| Slack piece | Teams equivalent |
|---|---|
| App OAuth install (`service_oauth_test.go` flow) | Entra ID app + Azure Bot resource. Org installs the SolidPing Teams app (manifest zip) into a team; the resulting `installationUpdate`/`conversationUpdate` activity carries tenant ID + conversation references, which establish the org↔tenant connection. |
| Socket Mode **or** HTTPS events ([config.go:571](server/internal/config/config.go#L571) — mutually exclusive transports) | **HTTPS only.** Bot Framework has no Socket-Mode equivalent; a public messaging endpoint is mandatory (`POST /api/v1/integrations/msteams/messages`). See "Self-hosted constraint" below. |
| `verify.go` (signing-secret verification) | JWT validation of incoming Bot Connector tokens against Microsoft's OpenID/JWKS metadata, plus audience (= app ID) and optional tenant allow-list checks. |
| Destinations (`service_destinations_test.go`) — channels to notify | Conversation references captured when the bot is added to a team/channel; stored per org, listed in the dashboard as pickable destinations. |
| `mention_commands.go` — `@solidping help`, `checks add\|list\|rm`, `incidents list`, `config default-channel` | Same command set. Parse Teams `message` activities (strip the `<at>` mention entity before parsing — reuse the grammar from `parser.go`), reply in-conversation via the Bot Connector reply API. |
| `notifications/slack.go` sender + reverse-thread updates | New sender in `server/internal/notifications/`: proactive Adaptive Card via Bot Connector (`continueConversation`), **update the same card** on escalate/resolve (activity update API), and post resolution as a reply to the original card so channels get Slack-thread-like grouping. |
| `service_uninstall_test.go` | Handle `installationUpdate` with `action: remove` → mark the connection uninstalled, stop routing to its destinations. |
| Slack DM adapter (`usernotifications.SlackDMSenderAdapter`, [server/internal/app/server.go:954](server/internal/app/server.go#L954)) | Personal-scope proactive DMs. **Phase 2** — requires personal app install + per-user conversation bootstrap; don't block the channel bot on it. |

### Credentials & tenancy model

- **SaaS**: one multi-tenant Entra ID app + Azure Bot owned by SolidPing;
  per-org connection is keyed by the installing tenant ID captured at install
  time. App ID/secret live in system config, not per-org settings.
- **Self-hosted**: the operator registers their own Entra app + Azure Bot and
  configures `MSTeamsConfig{Enabled, AppID, AppSecret, TenantID}` — mirror
  `SlackConfig` ([server/internal/config/config.go:563](server/internal/config/config.go#L563)).
  The app secret is a secret: register it in
  [conn_secrets.go](server/internal/crypto/credentials/conn_secrets.go) if any
  of it lands in per-connection settings.
- Serve a **generated app-manifest zip** from the server (endpoint that fills
  in the instance's public URL and app ID) so setup is paste-free; document
  custom-app upload ("sideloading") since store publication is out of scope.

### Self-hosted constraint — decide and document

Bot Framework requires Microsoft's servers to reach the messaging endpoint
over public HTTPS. Self-hosted instances behind a firewall cannot use the bot
(unlike Slack Socket Mode, which dials out). Decision: ship anyway, gate
behind `msteams.enabled` config (default false, like `SlackConfig{Enabled:
false}`, [config.go:934](server/internal/config/config.go#L934)), and state
the public-URL requirement prominently in docs and in the dashboard setup
page. Do not attempt a relay/polling workaround in this spec.

### Registration checklist

- `server/internal/db/models/integration.go` — `ConnectionTypeMSTeamsBot
  "msteams-bot"`, `CanNotify: true`; settings hold tenant ID + team/channel
  conversation references (shape analogous to `SlackSettings`).
- `server/internal/notifications/` — bot sender + registry case; severities
  set in [handlers/severities/service.go:51](server/internal/handlers/severities/service.go#L51).
- Routes wired in [server/internal/app/server.go](server/internal/app/server.go)
  next to the Slack wiring (~line 1211): messaging endpoint, destinations
  listing, connection status, manifest download.
- dash0: integration setup page (install instructions + manifest download +
  connection status + destinations picker — mirror the Slack integration UI),
  `ConnectionType` union + capability map in `api/hooks.ts`, type list in
  `integrations.new.tsx`, icon, four locales.
- MCP `tools_integrations.go`: expose the type for listing; creation happens
  via the install flow, not `create_integration` — mirror however Slack is
  handled there.

### Tests

Mirror the Slack package's test surface: command parser (mention stripping,
each subcommand), JWT verification against a fake JWKS (`httptest`), install
event → connection + destination creation, uninstall event, proactive send +
card update against a fake Bot Connector, routing (`service_routing`-style),
and sender registry round-trip. E2E: dashboard shows the setup page and lists
destinations for a seeded connection.

### Open questions

- Command trigger word: `@SolidPing` (app name in manifest) — confirm the
  display name works for both SaaS and self-hosted manifests.
- Whether `config default-channel` maps cleanly when Teams channels are
  per-team — may become `config default-channel` scoped to the team the
  command was issued in.

### Out of scope

- Teams store publication / Microsoft certification (custom app upload only).
- Personal DMs / user-level notifications (phase 2, noted above).
- Sign-in with Microsoft for dashboard auth (separate concern; OIDC covers it).
- The `msteams` webhook integration (spec 2026-08-02-01) — unchanged,
  remains the zero-infra option.
