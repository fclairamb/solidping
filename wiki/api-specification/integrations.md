# Integrations & Channels

Manage notification integrations (Slack, Discord, email, webhook, Freebox, …)
at the organization level, attach them to individual checks, and handle the
inbound endpoints each provider calls.

> **Naming alignment.** The canonical name for these endpoints is
> **integration** (the umbrella entity — Slack, webhook, email, Freebox).
> **/channels** is kept as a path alias (it is the prior name; "channel"
> survives only as the notify-capable *role*) — the routes are registered
> twice, under both prefixes, and return identical responses.
> **/connections** was the original legacy name and is **removed**.

## Org-level integrations

### GET /api/v1/orgs/:org/integrations (alias: /api/v1/orgs/:org/channels)
List all integrations. Auth: required

### POST /api/v1/orgs/:org/integrations (alias: /api/v1/orgs/:org/channels)
Create a new integration. Auth: required

### GET /api/v1/orgs/:org/integrations/:uid (alias: /api/v1/orgs/:org/channels/:uid)
Get an integration. Auth: required

### PATCH /api/v1/orgs/:org/integrations/:uid (alias: /api/v1/orgs/:org/channels/:uid)
Update an integration. Auth: required

### DELETE /api/v1/orgs/:org/integrations/:uid (alias: /api/v1/orgs/:org/channels/:uid)
Delete an integration. Auth: required

### POST /api/v1/orgs/:org/integrations/:uid/rotate-secret
Rotate the integration's shared secret (e.g. a webhook signing key). The old
secret stops working immediately. Auth: required

### POST /api/v1/orgs/:org/integrations/:uid/test
Send a test notification through the integration. Auth: required

## Check notify channels

Manage the notify-capable integrations ("channels") attached to individual
checks. Canonical path is `/integrations`; `/channels` is the alias for the
notify role. Both return identical responses.

### GET /api/v1/orgs/:org/checks/:check/integrations (alias: /channels)
List all notify channels for a check. Auth: required

### PUT /api/v1/orgs/:org/checks/:check/integrations (alias: /channels)
Set (replace) all notify channels for a check. Auth: required

### POST /api/v1/orgs/:org/checks/:check/integrations/:connection (alias: /channels)
Add a notify channel to a check. Auth: required

### DELETE /api/v1/orgs/:org/checks/:check/integrations/:connection (alias: /channels)
Remove a notify channel from a check. Auth: required

### GET /api/v1/orgs/:org/checks/:check/integrations/:connection (alias: /channels)
Get channel-specific settings for a check. Auth: required

### PATCH /api/v1/orgs/:org/checks/:check/integrations/:connection (alias: /channels)
Update channel-specific settings for a check. Auth: required

## Slack

Inbound endpoints for the Slack app, plus the install helpers.

### GET /api/v1/integrations/slack/oauth
Slack OAuth callback handler. Auth: public (Slack flow)

### POST /api/v1/integrations/slack/events
Slack Events API webhook. Auth: Slack signature verification

### POST /api/v1/integrations/slack/command
Slack slash command handler. Auth: Slack signature verification

### POST /api/v1/integrations/slack/interaction
Slack interactive component handler. Auth: Slack signature verification

### GET /api/v1/integrations/slack/install
Entry point for the "Add to Slack" flow — redirects to Slack's authorize URL.
Auth: public

### GET /api/v1/integrations/slack/socket/status
Report whether the Slack socket-mode connection is up (deployments that use
socket mode instead of public webhooks). Auth: public

### POST /api/v1/orgs/:org/integrations/slack/install-url
Build an org-scoped Slack install URL (carries the org in the OAuth state).
Auth: required

### GET /api/v1/orgs/:org/channels/:uid/slack/destinations
List the Slack channels/DMs the connected workspace can post to, for the
destination picker. Auth: required

## Microsoft Teams (bot)

Inbound and setup endpoints for the two-way Teams bot (connection type
`msteams-bot`). Distinct from the one-way `msteams` Teams Workflow webhook,
which needs no endpoints at all.

Note: `msteams-bot` connections cannot be created through
`POST /orgs/:org/integrations` — that returns a validation error, like Slack.
They are created by the link-code flow below.

### POST /api/v1/integrations/msteams/messages
Bot Framework messaging endpoint — Microsoft posts every activity here.
Auth: Bot Connector JWT, verified against Microsoft's JWKS (issuer, audience =
app ID, validity window, `serviceurl` claim, optional tenant allow-list).
Returns 503 while `msteams.enabled` is false.

Destination selections (the connection's `channel_id` and the per-check
`conversation_id` override) are validated against the connection's captured
`destinations` on every write and again at send time — a Teams conversation ID
is discoverable, so naming one is not authorization to post there.

### POST /api/v1/orgs/:org/integrations/msteams/link-code
Mint a one-time code that binds a Microsoft 365 tenant to this organization.
The org comes from the verified route context, and the tenant is written
server-side only when the code is quoted back from a signature-verified Bot
Framework activity (`@SolidPing link <code>`) — a tenant id is never accepted
from the client. Auth: required

### GET /api/v1/orgs/:org/integrations/msteams/status
Report whether the Teams bot is enabled and configured on this instance, the
messaging endpoint Microsoft must be able to reach, the Entra app id, the
single-tenant pin, and how many tenants have a live install. Auth: required
(these fields identify the deployment and its customers, so they are not
served anonymously).

### GET /api/v1/orgs/:org/integrations/msteams/manifest.zip
Download the generated Teams app package (manifest.json + icons) pre-filled
with this instance's app ID and public URL. Auth: required

### GET /api/v1/orgs/:org/channels/:uid/msteams/destinations
List the conversation references captured when the bot was added to Teams
channels, for the destination picker. Unlike Slack this reads stored state —
a Teams bot cannot enumerate channels it was never added to. Auth: required

## Telegram

Inbound endpoint for the instance-level Telegram bot. Telegram is a **direct
channel**, not a connection type: there is no `telegram` integration row to
create, only one instance bot (`SP_TELEGRAM_*`) and per-user connected chats.

### POST /api/v1/integrations/telegram/webhook
Bot API webhook — Telegram posts `message` and `my_chat_member` updates here.
Auth: the `X-Telegram-Bot-Api-Secret-Token` header only, compared constant-time
against `SP_TELEGRAM_WEBHOOK_SECRET` **before the body is parsed**. Telegram
does not sign its payloads, so that header is the only line of defense; a
missing or mismatched value is a bare `403` with no body detail. The body is
capped at 1 MiB before the check.

Registered only when `telegram.Active()`. Once authenticated the handler
**always** answers `200`, including for update types it does not implement —
Telegram retries any non-2xx forever.

Handled updates: `/start <token>` (redeems a single-use connect token and
creates the verified contact), `/stop` and `/unlink` (delete every contact for
the chat), and `my_chat_member` reporting the bot was blocked or kicked (same
deletion). Everything else is info-logged and acknowledged.

## Freebox

### POST /api/v1/orgs/:org/integrations/freebox/pair
Start pairing with a Freebox — the user must then physically authorize the app
on the box. Auth: required

### GET /api/v1/orgs/:org/integrations/freebox/pair/:uid/status
Poll the pairing state until it is granted (or refused). Auth: required

### GET /api/v1/orgs/:org/integrations/freebox/:uid/lan-hosts
List the LAN hosts the paired Freebox knows about. This is where Freebox LAN
listing lives — it is **not** part of the discovery API. Auth: required.
Errors: `409 FREEBOX_NOT_GRANTED` when the integration is not paired,
`404 NOT_FOUND` when there is no such Freebox integration.
