# Discord bot — operator setup

SolidPing's Discord integration has two modes and both are live at once:

- **Bot mode** — an OAuth install into a guild, with threads, an Acknowledge
  button, on-call mentions, slash/mention commands and inbound comments. This is
  the Slack-parity mode.
- **Legacy webhook mode** — a pasted webhook URL, one-way. Every Discord
  integration created before the bot existed is in this mode and keeps working
  untouched: no migration, no re-install, and no bot configuration needed on the
  instance.

Which mode an integration is in is a property of its **data**, not a separate
connection type: `models.DiscordSettings` carries both `webhook_url` and the bot
fields, and `DiscordSettings.UsesBot()` (guild **and** channel set) is what the
sender branches on. That is deliberate — it is what makes "the bot rework cannot
break the webhook path" a structural property rather than a promise.

## 1. Create the Discord application

In the [Discord Developer Portal](https://discord.com/developers/applications):

1. **New Application** → name it SolidPing.
2. **General Information** → copy the **Public Key**.
3. **OAuth2** → copy the **Client ID** and **Client Secret**.
4. **Bot** → **Reset Token** and copy the **Bot Token**.
5. **Bot → Privileged Gateway Intents** → enable **MESSAGE CONTENT INTENT**
   (see [§4](#4-the-message_content-intent)).
6. **General Information → Interactions Endpoint URL** →
   `https://<your-instance>/api/v1/integrations/discord/interactions`.
   Discord validates the URL immediately by sending signed probes, so set the
   public key in SolidPing *first* or the save will fail.

## 2. Configure SolidPing

Under **Server → Discord** in the dashboard, or by environment variable:

| Setting | System parameter | Env var | Secret |
|---|---|---|---|
| Enabled | `auth.discord.enabled` | `SP_DISCORD_ENABLED` | no |
| Client ID | `auth.discord.client_id` | `SP_DISCORD_CLIENT_ID` | no |
| Client secret | `auth.discord.client_secret` | `SP_DISCORD_CLIENT_SECRET` | yes |
| Bot token | `auth.discord.bot_token` | `SP_DISCORD_BOT_TOKEN` | yes |
| Public key | `auth.discord.public_key` | `SP_DISCORD_PUBLIC_KEY` | no |
| Gateway enabled | `auth.discord.gateway_enabled` | `SP_DISCORD_GATEWAY_ENABLED` | no |

The public key is deliberately **not** secret: it is a public verification key
Discord prints on the application's own settings page, and an operator needs to
read it back to confirm it matches.

## 3. Bot permissions requested at install

The install URL asks for exactly what the bot uses, and nothing else
(`discord.botPermissions`):

| Permission | Why |
|---|---|
| View Channel | read the destination channel at all |
| Send Messages | post the incident message |
| Embed Links | render the incident card as an embed |
| Read Message History | required to reply to / thread off a message |
| Create Public Threads | open the incident thread |
| Send Messages in Threads | post the follow-ups into it |
| **Manage Threads** | **un-archive** a thread Discord auto-archived |

Manage Threads is the non-obvious one. Discord auto-archives a thread after its
inactivity window (1 day to 1 week) and rejects posts into an archived thread.
The incident whose thread has gone quiet for a week is exactly the incident
whose "resolved" notice matters most, so the sender un-archives before every
late follow-up. Without this permission a long incident resolves silently.

Scopes: `bot applications.commands identify`. `identify` only names the human
who performed the install (`installed_by_user_id`).

## 4. The `MESSAGE_CONTENT` intent

**Request it early.** Discord grants `MESSAGE_CONTENT` freely while an
application is in fewer than **100 guilds**, and gates it behind a manual
review above that threshold. An application that grows past 100 guilds without
having requested it loses inbound message content until the review completes.

What depends on it:

| Feature | Transport | Needs `MESSAGE_CONTENT` |
|---|---|---|
| Incident alerts, thread follow-ups, resolve edits | REST (outbound) | no |
| Acknowledge / Escalate buttons | HTTPS interactions endpoint | no |
| `/solidping …` slash commands | HTTPS interactions endpoint | no |
| `@SolidPing …` mention commands | Gateway | **yes** |
| Thread replies → incident comments | Gateway | **yes** |

The failure mode is quiet, which is why it is worth calling out: without the
intent the bot connects, the status page says **Connected**, and every inbound
message arrives with an empty `content`. The Gateway logs
`Discord message has no content — is the MESSAGE_CONTENT intent enabled?` when
it sees this on a message inside a tracked incident thread.

## 5. Transports

Discord splits inbound traffic across two transports, and SolidPing needs both.
This is **not** the Slack Socket-Mode situation, where one transport replaces
the other:

- **HTTPS interactions endpoint** (`POST /api/v1/integrations/discord/interactions`)
  — buttons and slash commands. Requires the instance to be reachable from the
  public internet.
- **Gateway** (outgoing WebSocket, `discord.GatewaySupervisor`) — everything a
  human types. Requires no inbound connectivity, so a firewalled instance can
  have commands-by-mention and comment ingestion even without a public
  interactions endpoint.

Both funnel into the same `discord.DispatchCommand`, so the command set cannot
drift between them.

Gateway status is exposed at `GET /api/v1/integrations/discord/gateway/status`
and rendered on **Server → Discord**. It never contains the bot token.

### Signature verification

`discord.VerifyMiddleware` verifies `X-Signature-Ed25519` over
`timestamp + body` against the configured public key, and additionally rejects a
timestamp more than five minutes away from now (Discord does not require the
freshness bound; a signature without one is a bearer token that never expires).

It deliberately diverges from the Slack middleware in one way: **with no public
key configured it rejects everything** rather than falling through. Two reasons,
both decisive — the endpoint acknowledges and escalates incidents, and Discord
deactivates an endpoint that answers its invalid-signature probes with anything
other than 401.

## 6. Org resolution

The bot install writes into the **same** `organization_providers` row that
"Sign in with Discord" uses — `(provider_type = 'discord', provider_id = <guild
id>)`. `discord.Service.linkGuildToOrg` never creates a second row for a guild
that already has one, so auth and reporting can never disagree about which org a
guild belongs to. A guild already mapped to another org keeps that mapping; the
installing org still gets its own integration row, exactly like Slack's
two-orgs-one-workspace case.

## 7. Comment ingestion

Per integration, `settings.comment_ingestion`:

| Value | Behavior |
|---|---|
| `explicit` (default, and the meaning of an absent key) | Plain thread replies are ignored; only the `comment` command creates a comment. |
| `all` | Every human reply under a tracked incident thread is ingested. |

The mode is read per inbound message and **fails closed**: an unreadable
connection or unparseable settings are treated as `explicit`, because guessing
the other way writes private triage chatter into a permanent, fanned-out
incident timeline.

Only replies in a **tracked** thread are considered — the reverse mapping
`discord/threads/<guild>/<thread>` that the sender writes when it opens the
incident's thread. Bot posts, webhook posts and system notices are ignored, and
each message id is deduped so a Gateway RESUME replay cannot double-post.

## 8. Slash command registration

SolidPing does not register the application commands for you. Register a single
`solidping` command with the sub-commands listed in the docs
(`web/docs/docs/configuration/notifications.md`), or rely on mention commands,
which need no registration at all.

## Troubleshooting

| Symptom | Cause |
|---|---|
| Discord refuses to save the interactions URL | Public key not set in SolidPing, or set to a different application's key |
| Buttons do nothing, endpoint shows as deactivated in Discord | Signature verification failed a probe — check the public key |
| Bot connects but ignores every message | `MESSAGE_CONTENT` intent not enabled |
| Thread replies ignored, commands work | `comment_ingestion` is `explicit` (the default) |
| Late resolve messages missing | Manage Threads permission not granted — the thread archived and cannot be re-opened |
| Alerts stop after moving the channel | The stored `channel_id` no longer exists; re-pick a destination |
