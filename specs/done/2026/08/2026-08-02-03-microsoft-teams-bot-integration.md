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

---

## Implementation Plan

### Decisions on the two open questions

**1. Command trigger word.** The manifest's `name.short` is `SolidPing` for both
the SaaS and the self-hosted manifest (the generator only substitutes the
instance URL and app ID, never the name). But the parser does **not** depend on
the display name: Teams delivers the mention as an `<at>…</at>` entity in
`activity.text` plus a matching `entities[]` entry of type `mention`. The parser
strips **any** leading `<at>…</at>` run (and any `&nbsp;` Teams inserts after it)
before tokenizing, so the trigger word is display-name-independent and an
operator who renames the app in their own manifest keeps working commands. That
also covers the `@SolidPing` vs `@SolidPing (dev)` self-hosted rename case.

**2. `config default-channel` scoping.** Teams channels are per-team and there is
no cross-team `#channel` reference syntax (Slack's `<#C123|name>` has no Teams
equivalent). Decision: `config default-channel` takes **no channel argument** —
it always sets the **conversation the command was issued in** as the connection's
default notification destination, i.e. it is implicitly scoped to the team the
command came from. If an argument is given it must be a destination id already
known to the connection (one of the captured conversation references); anything
else is rejected with a message pointing at the dashboard destination picker.
The default is stored as `channel_id` / `channel_name` / `team_id` /
`service_url` on the connection settings — one default per connection, exactly
like Slack's single `ChannelID`.

### Phase 1 — data model, config, registration scaffolding

- `server/internal/db/models/integration.go`
  - `ConnectionTypeMSTeamsBot ConnectionType = "msteams-bot"` (CanNotify via the
    existing default branch of `CapabilitiesFor`).
  - `MSTeamsBotSettings` + `ToJSONMap` / `MSTeamsBotSettingsFromJSONMap`,
    snake_case JSON keys mirroring `SlackSettings`: `tenant_id`, `tenant_name`,
    `bot_id`, `app_id`, `service_url`, `channel_id`, `channel_name`, `team_id`,
    `installed_by_user_id`, `uninstalled_at`, `destinations` (list of
    `MSTeamsDestination{id,name,team_id,team_name,service_url,type}`).
- `server/internal/config/config.go` — `MSTeamsConfig{Enabled, AppID, AppSecret,
  TenantID}` mirroring `SlackConfig`; default `MSTeams: MSTeamsConfig{Enabled:
  false}`.
- `server/internal/systemconfig/systemconfig.go` — `msteams.enabled`,
  `msteams.app_id`, `msteams.app_secret` (Secret: true), `msteams.tenant_id`
  keys with `SP_MSTEAMS_*` env vars (koanf cannot reach snake_case segments, so
  the systemconfig entry is what binds them).
- `server/internal/crypto/credentials/conn_secrets.go` — register
  `msteams-bot: {"app_secret"}` so a per-connection secret override is encrypted
  at rest even though the normal path keeps credentials in system config.
- `server/internal/handlers/integrations/service.go` `validConnectionTypes`,
  `server/internal/handlers/severities/service.go` `allowedChannels`,
  `server/internal/mcp/tools_integrations.go` type lists.

### Phase 2 — JWT verification + messaging endpoint skeleton

New package `server/internal/integrations/msteams/`:

- `verify.go` — `Verifier`:
  - fetches Microsoft's Bot Framework OpenID metadata document
    (`https://login.botframework.com/v1/.well-known/openidconfiguration`,
    overridable for tests), reads `jwks_uri`, and holds a
    `oidc.NewRemoteKeySet` (already a dependency via the OIDC connector) so kid
    rotation/caching is handled for us.
  - `VerifyRequest(ctx, authorizationHeader, serviceURL)`: `Bearer` prefix,
    signature via the JWKS, `iss` against the metadata `issuer`, `aud` == the
    configured app ID, `exp`/`nbf` with a small clock skew, and the Bot
    Framework `serviceurl` claim against the activity's `serviceUrl` when the
    claim is present. Returns `*BotClaims{TenantID, AppID, ServiceURL}`.
  - optional tenant allow-list: when `cfg.MSTeams.TenantID` is set (self-hosted
    single-tenant), a token whose `tid` differs is rejected.
  - `VerifyMiddleware` in the `httpx` shape used by Slack's `VerifyMiddleware`
    — reads and restores the body so the handler can decode the activity.
- `types.go` — Bot Framework activity types: `Activity`, `ConversationAccount`,
  `ChannelAccount`, `ChannelData` (team/tenant), `Entity` (mention),
  `Attachment`, `ConversationReference`, plus the Adaptive Card structs used for
  replies.
- `handler.go` — `POST /api/v1/integrations/msteams/messages`, returns 503 when
  `msteams.enabled` is false, 200 otherwise (Bot Framework retries on non-2xx).

### Phase 3 — install / uninstall activity handling + destinations

- `activities.go` — `DispatchActivity` routing on `activity.type`:
  `installationUpdate` (`add` / `remove`), `conversationUpdate`
  (`membersAdded` containing the bot → capture the conversation reference),
  `message` → mention command.
- `service.go`:
  - `GetConnectionByTenantID` (home-org provider lookup then deterministic
    oldest-connection fallback, mirroring Slack's `GetConnectionByTeamID`).
  - `HandleInstall` — create-or-update the per-org connection keyed by
    `tenant_id`, capture `service_url`, `bot_id`, installer, and register the
    conversation as a destination (first one also becomes the default).
  - `HandleUninstall` — `installationUpdate action:remove` marks every
    connection for the tenant uninstalled (`uninstalled_at`, `enabled=false`,
    destinations cleared) so routing stops; matches Slack's fan-out semantics
    but keeps the row so the dashboard can show "uninstalled" instead of
    silently losing the integration.
  - `EnsureDestination` / `ListDestinations` / `SetDefaultDestination`.
  - `CreateCheckWithOptions`, `CountInstalledTenants`.

### Phase 4 — mention-command parsing + reply

- `parser.go` — `ParseMentionText` reusing the Slack grammar (tokenizer, flags,
  subcommands) after stripping `<at>…</at>` entities, `&nbsp;`, and any HTML
  Teams wraps the text in.
- `mention_commands.go` — `help`, `checks add|list|rm`, `results`,
  `incidents list`, `config default-channel`, replying in-conversation via the
  Bot Connector reply API with an Adaptive Card (text fallback in `text`).

### Phase 5 — Bot Connector client + proactive sender with card updates

- `client.go` — Bot Connector client:
  - `client_credentials` token against
    `https://login.microsoftonline.com/botframework.com/oauth2/v2.0/token`
    (scope `https://api.botframework.com/.default`), cached with expiry.
  - `SendToConversation` (`POST {serviceUrl}/v3/conversations/{id}/activities`),
    `ReplyToActivity` (`POST …/activities/{activityId}`), `UpdateActivity`
    (`PUT …/activities/{activityId}`). Base URLs injectable for tests.
- `server/internal/notifications/msteamsbot.go` — `MSTeamsBotSender`:
  - state key `incidents/<uid>/msteams/thread` → `{conversation_id,
    activity_id, service_url}`.
  - created/escalated: post (or, when state exists, **update in place**) the
    Adaptive Card.
  - resolved/reopened: update the original card to the resolved styling **and**
    post a reply with `replyToId` = the original activity id, giving the
    Slack-thread-like grouping the spec asks for. No state → skip (same
    defense-in-depth guard as `SlackSender`).
  - registry case in `notifications/registry.go`.

### Phase 6 — app-manifest zip generator

- `manifest.go` + `GET /api/v1/integrations/msteams/manifest.zip` — builds a
  Teams app package in memory: `manifest.json` (schema 1.16) filled with the
  instance's app ID, `validDomains` from `server.base_url`, bot `botId`,
  `messagingExtension`-free, `bots[].scopes: ["team","groupChat"]`, plus
  `color.png` (192×192) and `outline.png` (32×32) icons embedded in the binary.

### Phase 7 — dashboard (dash0)

- `api/hooks.ts` — `"msteams-bot"` in the `ConnectionType` union and
  `CAPABILITIES`; `MSTeamsDestination` / `useMSTeamsBotDestinations` /
  `msTeamsManifestUrl`.
- `components/integrations/integration-icon.tsx` — icon + label.
- `routes/orgs/$org/integrations.new.tsx` — add to `ALL_TYPES`.
- `components/integrations/integration-form.tsx` — `MSTeamsBotPanel`: connection
  status (tenant name / not-connected), the **public-HTTPS-endpoint
  requirement** callout, "Download Teams app package" button, and the
  destinations picker over the captured conversation references. Built from the
  primitives in `design-reference.tsx` (Card/Button/Label/Alert), mobile-first.
- Four locales (`en`, `fr`, `es`, `de`).

### Phase 8 — tests (written alongside each phase)

- `parser_test.go` — mention stripping (`<at>SolidPing</at>`, `&nbsp;`, HTML) and
  every subcommand.
- `verify_test.go` — fake JWKS + metadata (`httptest` + `go-jose`): accepts a
  well-formed token; rejects wrong audience, wrong issuer, expired, unknown key,
  wrong tenant, missing/garbled header.
- `service_install_test.go` / `service_uninstall_test.go` /
  `service_destinations_test.go` / `service_routing_test.go`.
- `client_test.go` — token acquisition + send/reply/update against a fake Bot
  Connector.
- `manifest_test.go` — zip contents and substituted values.
- `notifications/msteamsbot_test.go` — create → escalate (update in place) →
  resolve (update + reply), plus `registry_test.go` round-trip.
- E2E `web/dash0/e2e/` — setup page renders and lists destinations for a seeded
  connection.

**Known limit:** nothing in this repo can supply a real Entra/Azure Bot
credential, so the outbound Bot Connector calls and the JWKS verification are
exercised against `httptest` fakes only; a live Microsoft round-trip is not
reachable from CI.
