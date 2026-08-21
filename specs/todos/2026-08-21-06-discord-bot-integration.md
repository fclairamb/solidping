---
model: opus
effort: xhigh
---

# Discord is a bare webhook poster — build a proper bot with Slack-level auth + reporting

## Problem

Discord support today is asymmetric and shallow compared to what Slack gets and to
the bar recent channels (WhatsApp, Telegram) set:

- **Auth is done.** "Sign in with Discord" shipped in March
  (`specs/done/2026/03/2026-03-22-discord-oauth.md`):
  [server/internal/handlers/auth/discord.go](server/internal/handlers/auth/discord.go)
  (scopes `identify email guilds`, org resolved from the user's first guild via
  `fetchUserGuilds`/`findOrCreateOrganization`), routes registered in
  [server.go:779](server/internal/app/server.go:779), provider surfaced by
  `ListProviders` ([providers_available.go:89](server/internal/handlers/auth/providers_available.go:89)),
  login button already wired in
  [login.tsx](web/dash0/src/routes/orgs/$org/login.tsx) (`PROVIDER_ICONS`).
- **Reporting is not.** The Discord notifier
  ([discord.go](server/internal/notifications/discord.go), ~225 lines) just POSTs
  each event to a pasted webhook URL. `DiscordSettings`
  ([integration.go:233](server/internal/db/models/integration.go:233)) holds a
  single field, `webhook_url`, and the frontend settings panel is the shared bare
  `UrlPanel` ([integration-form.tsx:347](web/dash0/src/components/integrations/integration-form.tsx:347)).

Everything the Slack integration provides is missing on Discord:

| Capability | Slack | Discord today |
|---|---|---|
| Install flow | OAuth + org-scoped install-URL minting | paste a webhook URL |
| Destination picker | `GET .../slack/destinations` + UI panel | none |
| Thread tracking (fwd + reverse state entries) | yes ([slack.go:41](server/internal/notifications/slack.go:41)) | none — every event is a fresh top-level post |
| Resolve edits the original message | yes (`buildResolvedUpdateMessage`) | no |
| Ack button on the incident message | `acknowledge_incident` block action | no |
| Slash + mention commands | `/check`, `/comment`, `@solidping …` | no |
| Inbound replies → incident comments | yes, gated by `comment_ingestion` | no |
| On-call mentions via identity mapping | `user_integration_identities` | no |
| Uninstall handling | `HandleAppUninstalled` | n/a |
| Request signature verification | signing-secret HMAC | n/a (Discord requires **Ed25519**) |
| Push transport without public HTTP | Socket Mode supervisor | Discord Gateway would be the analog |

A draft spec for exactly this exists —
`specs/done/2026/03/2026-03-22-discord-integration.md` — but was archived
unimplemented (its checklist is all-unchecked, status "Draft"). Its Slack→Discord
concept mapping is still useful; its data model predates the `integrations`
rename, the destinations picker, socket mode, comment ingestion, and identity
mapping, so it must not be followed literally.

One hook already exists: `DiscordOAuthConfig.BotToken`
([config/discord_oauth.go](server/internal/config/discord_oauth.go), systemconfig
key `auth.discord.bot_token`) is plumbed through but unused by the notifier.

## Proposal

Bring Discord to parity with Slack for reporting, mirroring the Slack
architecture piece by piece (the `msteams-bot` integration —
`MSTeamsBotSettings` at
[integration.go:344](server/internal/db/models/integration.go:344) — is the
closest bot-style settings template). Auth needs no new work beyond verifying the
existing flow still matches (`auth.discord.*` systemconfig keys, docs).

### 1. Bot-based integration type (replace webhook-only settings)

- Extend `DiscordSettings` to a bot-style blob: `guild_id`, `guild_name`,
  `channel_id`, `channel_name`, `installed_by_user_id`, `mention_on_call`,
  `comment_ingestion` (same `explicit`/`all` semantics as
  `SlackSettings.CommentIngestion`). Keep `webhook_url` working as a legacy mode —
  existing integrations must not break; the sender picks bot vs webhook by which
  fields are set.
- Bot install flow: Discord OAuth2 `bot` + `applications.commands` scopes with an
  org-scoped install URL (mirror `BuildInstallURLForOrg`,
  [slack/handler.go:137](server/internal/integrations/slack/handler.go:137), and
  `createOrUpdateConnection` in slack/service.go). New package
  `server/internal/integrations/discord/`.
- Destination picker: `GET /api/v1/orgs/:org/channels/:uid/discord/destinations`
  listing the guild's text channels via the bot token (mirror
  [server.go:1728](server/internal/app/server.go:1728)); frontend gets a
  `DiscordDestinationPanel` in
  [integration-form.tsx](web/dash0/src/components/integrations/integration-form.tsx)
  instead of the bare `UrlPanel`.

### 2. Sender parity (threads, edits, buttons)

Rework [notifications/discord.go](server/internal/notifications/discord.go) to
use the bot API when bot settings are present:

- Post `incident.created` as a rich embed with an **Acknowledge button**
  (Discord message components), store forward thread state
  (`incidents/<uid>/discord/thread`) and the reverse key → incident, exactly
  like slack.go:24-41 / `storeReverseThreadInfo`.
- `incident.resolved` / `reopened` / `escalated` / `comment` post into the
  message's thread; resolve also edits the original embed in place (mirror
  `handleResolvedUpdate`). Enforce `requiresExistingThread` semantics.
- On-call mentions: render `<@discord_user_id>` from
  `user_integration_identities` (new provider value `discord`), gated by
  `mention_on_call`. Identity capture can reuse the Discord OAuth identity
  (users who signed in with Discord already have their Discord user ID).

### 3. Interactions endpoint + commands

- `POST /api/v1/integrations/discord/interactions` — Discord's single callback
  for buttons and slash commands. **Ed25519 signature verification is
  mandatory** (`X-Signature-Ed25519` / `X-Signature-Timestamp`; Discord
  deactivates the endpoint if verification fails its probes). Structure as a
  `VerifyMiddleware` like [slack/verify.go](server/internal/integrations/slack/verify.go);
  needs a new `auth.discord.public_key` systemconfig key.
- Ack button → mirror `AcknowledgeIncidentFromSlack`
  ([interactions.go:244](server/internal/integrations/slack/interactions.go:244)):
  acknowledge, rewrite the embed, reply in thread.
- Slash commands `/solidping check <url>`, `/solidping comment …`, plus the
  management set from `mention_commands.go` (checks add/list/remove, incidents,
  config default-channel, help) — route through a transport-agnostic
  `DispatchCommand` seam as Slack does, so a later Gateway transport reuses it.
- Inbound thread replies → incident comments via the reverse thread map, honoring
  `comment_ingestion`. Message-content delivery is the deciding factor for
  transport (see open questions).

### 4. Config, docs, tests

- Systemconfig: `auth.discord.public_key` (+ env `SP_DISCORD_PUBLIC_KEY`); reuse
  existing `auth.discord.bot_token`, `client_id`, `client_secret`. Server-admin
  UI page alongside [server.slack.tsx](web/dash0/src/routes/orgs/$org/server.slack.tsx).
- Docs: rewrite the Discord section of
  [notifications.md:387](web/docs/docs/configuration/notifications.md:387)
  (capability matrix row changes from "Webhook" to bot), add a
  `wiki/discord/README.md` with app setup + required bot permissions (mirror
  `wiki/slack/README.md`); refresh
  [notifications-and-escalation.md](wiki/features/notifications-and-escalation.md)
  and [integrations.md](wiki/api-specification/integrations.md).
- Tests at the level of the Slack suite: sender table-tests (created/resolved
  thread + edit, buttons, mentions), Ed25519 verify middleware (valid/invalid/
  stale timestamp), interaction ack flow, command dispatch, legacy-webhook
  regression, destinations handler. Frontend E2E for the new settings panel.

### Open questions

1. **Transport for inbound events**: Discord has no HTTP event subscription for
   plain messages — reading thread replies (comment ingestion) and mention
   commands requires a **Gateway (WebSocket) connection** with the privileged
   `MESSAGE_CONTENT` intent. Buttons and slash commands work over the HTTP
   interactions endpoint alone. Option A: ship interactions-endpoint-only first
   (ack + slash commands, no comment ingestion) and add a Gateway supervisor
   (mirror `SlackSocketSupervisor`, [socketmode.go](server/internal/integrations/slack/socketmode.go))
   in a follow-up. Option B: build the Gateway now. Recommend A — it delivers
   most of the parity table without the privileged-intent review Discord
   imposes at 100+ guilds.
2. **Org resolution on bot install**: Slack maps team→org via
   `organization_providers`; Discord auth already maps guild→org
   (`findOrCreateOrganization`, discord_service.go). The bot install must land
   in the *same* mapping so auth and reporting agree on which org a guild is.
3. **Threads vs channel replies**: Discord auto-archives threads (default
   1 day–1 week). Decide whether follow-ups create a real thread on the incident
   message (preferred, mirrors Slack) and whether the sender must un-archive
   before posting late events.
4. Whether a per-user Discord DM contact (WhatsApp-style
   `user_notification_routes` entry) is in scope — the description says
   "same level of features as WhatsApp", which this spec reads as
   feature-completeness parity for the org integration, not a personal DM
   channel. Flag for the implementer to confirm; DMs via the bot are cheap once
   the bot exists.

## Resolved open questions

Answered by the maintainer on 2026-08-21. These are directives, not options —
implement them as written.

1. **Transport for inbound events** — *"Option A: interactions-endpoint-only
   first … Option B: build the Gateway now."*
   **Decision: Option B — build the Gateway now.** Ship the Gateway (WebSocket)
   supervisor in this spec, mirroring `SlackSocketSupervisor`
   ([socketmode.go](../../server/internal/integrations/slack/socketmode.go)),
   alongside the HTTP interactions endpoint. That means the privileged
   `MESSAGE_CONTENT` intent is in scope, and so is comment ingestion from thread
   replies — do not defer either to a follow-up. Note in the docs that Discord
   gates `MESSAGE_CONTENT` behind review at 100+ guilds, so the operator-facing
   setup guide must say what to request and when.

2. **Org resolution on bot install** — the bot install MUST land in the *same*
   `organization_providers` mapping that Discord auth already writes via
   `findOrCreateOrganization` (`discord_service.go`), so auth and reporting agree
   on which org a guild belongs to. Do not introduce a second guild→org mapping.
   Cover the agreement with a test that installs the bot for a guild that auth
   already mapped and asserts one mapping, not two.

3. **Threads vs channel replies** — **Decision: real threads, with un-archive.**
   Follow-ups create a real thread on the incident message, mirroring Slack. The
   sender must un-archive the thread before posting a late event, since Discord
   auto-archives after 1 day–1 week. Test the late-follow-up path explicitly: a
   thread that Discord has archived must still receive the resolve message.

4. **Per-user Discord DM contact** — **Decision: out of scope.** This spec
   delivers feature-completeness parity for the *org* integration only. Do NOT
   add a WhatsApp-style `user_notification_routes` DM entry; a personal DM route
   is a cheap follow-up once the bot exists, and belongs in its own spec.
