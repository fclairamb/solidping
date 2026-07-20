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
