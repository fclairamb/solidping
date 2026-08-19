---
sidebar_position: 3
title: Notifications
---

# Notifications Configuration

SolidPing supports multiple notification channels to alert you when incidents occur.

## Supported Channels

| Channel | Status | Configuration |
|---------|--------|---------------|
| Slack | Available | OAuth integration |
| Microsoft Teams (bot) | Available | Azure Bot / Bot Framework (two-way) |
| Microsoft Teams (webhook) | Available | Teams Workflow webhook (one-way) |
| Discord | Available | Webhook |
| Email | Available | SMTP |
| Webhooks | Available | HTTP POST |
| Google Chat | Available | Webhook |
| Mattermost | Available | Webhook |
| ntfy | Available | HTTP push |
| Matrix | Available | Client-Server API |
| PagerDuty | Available | Events API v2 |
| Pushover | Available | API integration |
| Web Push | Available | Browser push (VAPID) |
| SMS / Voice | Available | [SMS & Voice](./sms.md) (server-provided by default, per-organization Twilio as an override) |
| WhatsApp | Available | [Meta WhatsApp Business Cloud API](./whatsapp.md) (instance-level) |
| Telegram | Available | [Telegram Bot API](./telegram.md) (instance-level bot, free per message) |

## Email (SMTP)

Configure email notifications via SMTP.

### Environment Variables

```bash
SP_EMAIL_ENABLED=true
SP_EMAIL_HOST=smtp.example.com
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=noreply@example.com
SP_EMAIL_PASSWORD=your-smtp-password
SP_EMAIL_FROM=noreply@example.com
SP_EMAIL_FROMNAME=SolidPing
SP_EMAIL_AUTHTYPE=login
SP_EMAIL_PROTOCOL=starttls
SP_EMAIL_INSECURESKIPVERIFY=false
```

### Configuration File

```yaml
email:
  enabled: true
  host: smtp.example.com
  port: 587
  username: noreply@example.com
  password: your-smtp-password
  from: noreply@example.com
  from_name: SolidPing
  auth_type: login
  protocol: starttls
  insecure_skip_verify: false
```

### Auth Types

| Type | Description |
|------|-------------|
| `plain` | PLAIN authentication |
| `login` | LOGIN authentication (default) |
| `cram-md5` | CRAM-MD5 authentication |

### Protocol Options

| Protocol | Description |
|----------|-------------|
| `none` | No encryption |
| `starttls` | STARTTLS (default) |
| `ssl` | SSL/TLS |

### Provider Examples

#### Gmail

```bash
SP_EMAIL_HOST=smtp.gmail.com
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=your-email@gmail.com
SP_EMAIL_PASSWORD=your-app-password  # Use App Password, not your account password
SP_EMAIL_AUTHTYPE=login
```

:::note Gmail App Password
Gmail requires an App Password when 2FA is enabled. Generate one at https://myaccount.google.com/apppasswords
:::

#### SendGrid

```bash
SP_EMAIL_HOST=smtp.sendgrid.net
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=apikey
SP_EMAIL_PASSWORD=your-sendgrid-api-key
SP_EMAIL_AUTHTYPE=login
```

#### Amazon SES

```bash
SP_EMAIL_HOST=email-smtp.us-east-1.amazonaws.com
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=your-ses-smtp-username
SP_EMAIL_PASSWORD=your-ses-smtp-password
SP_EMAIL_AUTHTYPE=login
```

#### Mailgun

```bash
SP_EMAIL_HOST=smtp.mailgun.org
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=postmaster@your-domain.mailgun.org
SP_EMAIL_PASSWORD=your-mailgun-smtp-password
SP_EMAIL_AUTHTYPE=login
```

## Slack

Slack integration uses OAuth for secure access.

### Environment Variables

```bash
SP_SLACK_APP_ID=A0XXXXXXXXX
SP_SLACK_CLIENT_ID=1234567890.1234567890123
SP_SLACK_CLIENT_SECRET=your-client-secret
SP_SLACK_SIGNING_SECRET=your-signing-secret
```

### Configuration File

```yaml
slack:
  app_id: A0XXXXXXXXX
  client_id: "1234567890.1234567890123"
  client_secret: your-client-secret
  signing_secret: your-signing-secret
```

### Setting Up a Slack App

1. Go to https://api.slack.com/apps
2. Click "Create New App" → "From scratch"
3. Name it "SolidPing" and select your workspace
4. Go to "OAuth & Permissions" and add scopes:
   - `chat:write`
   - `chat:write.public`
   - `channels:read`
5. Go to "Basic Information" to get your credentials
6. Install the app to your workspace

### Slash commands

| Command | What it does |
|---|---|
| `/check <url>` | Creates an HTTP check for the URL. |
| `/comment [#42] <text>` | Adds a comment to an incident's timeline. |

`/comment` is the **explicit** way to add a comment from Slack. A slash command
posts nothing visible in the channel, so SolidPing answers with an ephemeral
confirmation and then fans the comment out to every channel attached to the
failing check — including the incident's own Slack thread, so the channel that
typed it still sees it.

Slack's payload for a slash command does **not** include `thread_ts`, so the
command cannot tell which thread it was typed in. The incident is resolved from
the channel instead:

1. An explicit `#42` always wins.
2. Otherwise, if the channel has exactly **one** active incident thread, that is
   the target.
3. Otherwise you get an ephemeral error listing the candidates — SolidPing never
   guesses which incident a note belongs to.

### Capturing thread replies (`comment_ingestion`)

Each Slack integration has a **"Capture every thread reply as a comment"**
toggle on its edit page, stored as `comment_ingestion`:

| Value | Behavior |
|---|---|
| `explicit` (**default**) | Only `/comment` creates an incident comment. Triage chatter in the thread — "lunch?", "who is on call?" — stays chatter. |
| `all` | Every human reply in a tracked incident thread is saved to the incident timeline. |

An integration created before this setting existed carries no value and is
therefore treated as `explicit`, so no workspace keeps over-capturing after an
upgrade. Bot-authored messages (including SolidPing's own thread replies) are
never ingested in either mode.

### Notification Format

Slack notifications include:
- Service name and URL
- Status change (Up → Down, Down → Up)
- Error details (for failures)
- Direct link to the check in SolidPing

## Microsoft Teams

There are **two** Teams integrations, and they are independent — pick either, or
use both:

| Integration type | What it does | What it needs |
|---|---|---|
| `msteams` | One-way: posts Adaptive Cards into a channel | A Teams Workflow URL. No server configuration, works behind a firewall. |
| `msteams-bot` | Two-way: alerts **plus** `@SolidPing` commands, and incident cards that update in place | An Entra ID app + Azure Bot, and a **publicly reachable HTTPS endpoint**. |

### Microsoft Teams (webhook) — the zero-infra option

1. In Teams, open **Workflows** → "Post to a channel when a webhook request is
   received"
2. Finish the wizard and copy the workflow URL it gives you
3. Paste it into a `msteams` integration in SolidPing

The legacy "Incoming Webhook" Office 365 connector is retired by Microsoft and
will not work.

### Microsoft Teams (bot) — the Slack-grade option

:::warning Public HTTPS endpoint required
The Bot Framework has **no Socket-Mode equivalent**: Microsoft's servers push
activities to your instance, they never dial out from it. Your SolidPing
instance must therefore be reachable from the public internet over HTTPS at:

```
https://<your-base-url>/api/v1/integrations/msteams/messages
```

A self-hosted instance behind a firewall or on a private network **cannot use
the Teams bot**. Use the `msteams` webhook integration instead — it only makes
outbound requests.
:::

#### 1. Register the Entra app and Azure Bot

1. In the [Azure portal](https://portal.azure.com), create an **Azure Bot**
   resource (multi-tenant for a shared deployment, single-tenant for your own).
2. Note the **Microsoft App ID** and create a **client secret**.
3. Set the bot's **messaging endpoint** to
   `https://<your-base-url>/api/v1/integrations/msteams/messages`.
4. Enable the **Microsoft Teams** channel on the bot.

#### 2. Configure SolidPing

```bash
SP_MSTEAMS_ENABLED=true
SP_MSTEAMS_APP_ID=00000000-0000-0000-0000-000000000000
SP_MSTEAMS_APP_SECRET=your-client-secret
# Optional: restrict inbound activities to a single Microsoft 365 tenant.
SP_MSTEAMS_TENANT_ID=11111111-1111-1111-1111-111111111111
```

```yaml
msteams:
  enabled: true
  app_id: 00000000-0000-0000-0000-000000000000
  app_secret: your-client-secret
  tenant_id: ""   # empty = multi-tenant
```

`msteams.enabled` defaults to **false** — the bot stays off until you turn it
on, precisely because of the public-endpoint requirement above.

#### 3. Install the app in Teams and link your tenant

1. In SolidPing, create a **Microsoft Teams (bot)** integration and open it.
2. Click **Download Teams app package** — the zip is generated with your
   instance's app ID and URL already filled in, so nothing has to be edited.
3. In Teams: **Apps → Manage your apps → Upload a custom app**, pick the zip,
   and add SolidPing to a team.
4. Back in SolidPing, click **Connect Microsoft Teams**. You get a one-time
   link code.
5. In a Teams channel the bot was added to, send:

   ```
   @SolidPing link ABCDE-FGHIJ
   ```

   The bot confirms in the channel, and the integration is connected.

Every channel you add the bot to becomes a selectable notification destination;
the first one becomes the default.

:::note Why a link code instead of a tenant ID field
A Microsoft 365 tenant ID is a semi-public identifier, not a secret, so simply
typing one into a form proves nothing — anyone could claim any tenant, and then
receive that tenant's channel names and post into its channels. Bot Framework
has no OAuth redirect that could carry your SolidPing organization through the
install the way Slack's does, so the link code fills that gap: SolidPing issues
it to a signed-in admin of one organization, and it can only be redeemed from
inside a real, Microsoft-signed message. Quoting the code back is what proves
both sides are the same actor.

The code is single-use and expires after 30 minutes. A tenant can be linked to
exactly one SolidPing organization. The command must be run in a team channel,
not a private chat, so the link is visible to your team.
:::

:::caution Who can run the link command
The link code proves that whoever redeems it holds a code issued to a signed-in
SolidPing admin — it does **not** prove that the person redeeming it is an
owner of the Teams team. Microsoft's Bot Connector does not tell a bot the
sender's role, and determining it would require Microsoft Graph permissions and
admin consent that this integration deliberately does not request. In practice
that means **any member of a team the bot has been added to could redeem a
leaked code**.

Treat a link code like a password: generate it when you are ready to use it,
paste it once, and let it expire otherwise.

If a tenant ends up linked to the wrong organization, the tenant's own admin can
recover without SolidPing support: **remove the SolidPing app from the tenant in
Teams**, then reinstall it and link again with a code from the correct
organization. Uninstalling releases the tenant's claim — it is the one lever the
party who actually controls the tenant always holds. (An admin of the holding
organization can also simply delete the integration.)
:::

Publishing to the Teams store is out of scope — custom app upload
("sideloading") is the supported path, which your Teams admin may need to allow
in the Teams admin center.

#### What the bot can do

- Posts an Adaptive Card when an incident opens, **updates that same card** when
  the incident escalates or resolves, and replies under it so a channel shows
  one incident as one grouped conversation.
- Answers `@SolidPing` commands in a channel:
  - `@SolidPing help`
  - `@SolidPing checks add <url>` / `checks list` / `checks rm <slug>`
  - `@SolidPing results -check <slug>`
  - `@SolidPing incidents list [-check <slug>]`
  - `@SolidPing config default-channel` — makes the current channel the default
    notification target (Teams has no cross-team channel reference, so the
    command is scoped to the team it is issued in)
  - `@SolidPing link <code>` — connects this Microsoft 365 tenant to a
    SolidPing organization (see step 5 above)
- Stops routing when the app is removed from the tenant, and resumes on
  reinstall without losing the org's notification wiring.

Personal-scope direct messages are not supported yet.

#### Security

Every inbound activity is authenticated before anything is acted on. The Bot
Connector JWT is verified against Microsoft's published JWKS, and the issuer,
audience (your app ID), signing algorithm, validity window and `serviceurl`
binding are all checked — the `serviceurl` check is mandatory, so a captured
token cannot be replayed with a different body to redirect SolidPing's outbound
calls.

Microsoft's Connector-to-Bot token carries no tenant claim, so tenant identity
comes from the activity body — which is trustworthy precisely because the
request carrying it passed that signature check. Ownership of a tenant is a
separate question, and is what the link code establishes. When
`msteams.tenant_id` is set, activities from any other tenant are rejected.

Notification destinations are held to the same standard. A Teams conversation ID
is discoverable (it appears in "Get link to channel" URLs), so SolidPing only
accepts a destination the bot has actually been added to — enforced when the
channel is picked in the dashboard, when a per-check override is set, and again
at send time. Naming a conversation ID you happen to know is not enough to make
SolidPing post there.

#### Known limitations

- **Managed-identity bots are not supported.** Outbound tokens are minted with
  a client secret, against the multi-tenant endpoint by default or your own
  tenant's endpoint when `msteams.tenant_id` is set. A bot registered with a
  user-assigned managed identity takes its token from the instance metadata
  endpoint instead, which is a different credential model.
- **Teams store publication is out of scope** — custom app upload only.
- **The link command cannot verify the sender is a team owner** — see the
  caution above for the reasoning and the recovery path.

## Discord

Discord notifications use webhooks.

### Setting Up Discord Webhooks

1. In your Discord server, go to Server Settings → Integrations
2. Click "Webhooks" → "New Webhook"
3. Name it "SolidPing" and select the channel
4. Copy the Webhook URL
5. Add the webhook URL in SolidPing's integration settings

### Webhook URL Format

```
https://discord.com/api/webhooks/{webhook.id}/{webhook.token}
```

## Google Chat

Google Chat notifications use incoming webhooks.

### Setting Up Google Chat Webhooks

1. In Google Chat, open the space where you want notifications
2. Click the space name → "Apps & integrations" → "Manage webhooks"
3. Click "Add webhook", name it "SolidPing", and copy the URL
4. Add the webhook URL in SolidPing's integration settings

## Mattermost

Mattermost notifications use incoming webhooks, similar to Slack.

### Setting Up Mattermost Webhooks

1. In Mattermost, go to Main Menu → Integrations → Incoming Webhooks
2. Click "Add Incoming Webhook"
3. Select the channel and fill in the details
4. Copy the webhook URL
5. Add the webhook URL in SolidPing's integration settings

## ntfy

[ntfy](https://ntfy.sh) is a simple HTTP-based pub/sub notification service. SolidPing can push notifications to any ntfy topic.

### Configuration

Add a ntfy connection in SolidPing with:
- **Server URL**: `https://ntfy.sh` (or your self-hosted instance)
- **Topic**: Your topic name (e.g., `solidping-alerts`)

### Example

```
# Subscribe to notifications
ntfy subscribe solidping-alerts
```

## Matrix

[Matrix](https://matrix.org) is an open, decentralized chat protocol — the standard for
self-hosted chat (Element, Synapse, Conduit, beeper). SolidPing sends notifications as a
dedicated bot user posting into a room, via the Matrix Client-Server API — no bridge or
generic webhook needed.

### Configuration

Add a Matrix connection in SolidPing with:
- **Homeserver URL**: the base URL of the bot account's homeserver (e.g. `https://matrix.org`,
  or your self-hosted Synapse/Conduit instance). A trailing slash is fine.
- **Access token**: the bot/dedicated user's access token. Treated as a secret — stored
  encrypted, never shown again after you save it.
- **Room**: the room ID (`!abcdef:matrix.org`) or alias (`#alerts:matrix.org`) to post into.
  The bot account must already be invited to and have joined the room — SolidPing does not
  auto-join.

### Creating a bot user and access token

1. Register a dedicated account for SolidPing on your homeserver (don't reuse a personal
   account — the access token can post as that user indefinitely).
2. Get an access token for that account, either:
   - In [Element](https://element.io): sign in as the bot user, then go to
     **Settings → Help & About → Advanced → Access Token**, or
   - Via the API: `POST /_matrix/client/v3/login` with the bot's credentials, which returns
     an `access_token` in the response.
3. Find the room ID: in Element, open the room, go to **Room settings → Advanced**, and copy
   the internal room ID (starts with `!`). A room alias (starts with `#`) also works —
   SolidPing resolves it to the current room ID on every send.
4. Invite the bot account to the room and accept the invite as the bot (join it) — SolidPing
   only posts into rooms the bot is already a member of.

### Limitations

- End-to-end encrypted rooms are not supported (posting into one would require a full Matrix
  crypto SDK). Use an unencrypted room for alerts.
- SolidPing does not auto-join a room on invite — join it manually as the bot account first.

## PagerDuty

PagerDuty integration for incident management and on-call alerting, via the
**Events API v2** only — no OAuth, no REST API v2, no schedule import.

### Configuration

Add a PagerDuty connection in SolidPing with:
- **Integration key**: The routing key of a PagerDuty Events API v2 integration

### Setting Up PagerDuty

1. In PagerDuty, open the service that should receive SolidPing's alerts
2. Go to **Integrations** → **Add integration**
3. Choose **Events API v2**
4. Copy the generated integration key
5. Add it in SolidPing's integration settings

### Behavior

- An incident opening (or reopening) sends a `trigger` event; resolving it
  sends a `resolve` event. Both carry the incident's UID as the `dedup_key`,
  so they correlate to **one** PagerDuty incident across its whole lifecycle.
- A comment or an escalation sends **nothing** to PagerDuty: the Events API
  v2 has no note/annotation concept, and reusing the `dedup_key` on a
  `trigger` would re-open an already-resolved incident.
- Severity (`critical`/`error`/`warning`/`info`) is derived from the result
  that triggered or is currently failing the incident.

## Pushover

Pushover delivers real-time notifications to your mobile devices and desktop.

### Configuration

Add a Pushover connection in SolidPing with:
- **User Key**: Your Pushover user key
- **API Token**: Your Pushover application API token

### Setting Up Pushover

1. Create an account at https://pushover.net
2. Create a new Application/API Token for SolidPing
3. Add your user key and API token in SolidPing's integration settings

## Web Push

Web Push delivers browser notifications straight to a user's device — no third-party
service required. It is typically used as an on-call/escalation contact so an operator
gets paged even when the dashboard tab is closed.

Web Push relies on [VAPID](https://datatracker.ietf.org/doc/html/rfc8292) keys that the
server holds. Once configured, each user opts in from their notification settings, and
the browser registers a push subscription.

### Environment Variables

```bash
SP_WEBPUSH_ENABLED=true
SP_WEBPUSH_VAPID_PUBLIC_KEY=your-vapid-public-key
SP_WEBPUSH_VAPID_PRIVATE_KEY=your-vapid-private-key
SP_WEBPUSH_SUBJECT=mailto:admin@example.com   # contact URI advertised to push services
```

### Configuration File

```yaml
webpush:
  enabled: true
  vapid_public_key: your-vapid-public-key
  vapid_private_key: your-vapid-private-key
  subject: mailto:admin@example.com
```

:::note Generating VAPID keys
Generate a VAPID key pair with any web-push tooling (e.g. `npx web-push generate-vapid-keys`).
Keep the private key secret.
:::

## Webhooks

Generic webhooks send HTTP POST requests to any URL.

### Payload Format

```json
{
  "event": "incident.created",
  "timestamp": "2024-01-15T10:30:00Z",
  "check": {
    "uid": "550e8400-e29b-41d4-a716-446655440000",
    "name": "API Health Check",
    "url": "https://api.example.com/health"
  },
  "incident": {
    "uid": "550e8400-e29b-41d4-a716-446655440001",
    "status": "active",
    "started_at": "2024-01-15T10:30:00Z"
  },
  "result": {
    "status": "down",
    "duration_ms": 5000,
    "error": "Connection timeout"
  }
}
```

### Event Types

| Event | Description |
|-------|-------------|
| `incident.created` | New incident (check started failing) |
| `incident.escalated` | Incident reached escalation threshold |
| `incident.resolved` | Incident resolved (check recovered) |

### Webhook Configuration

In the SolidPing dashboard:
1. Go to Settings → Integrations
2. Add a new Webhook connection
3. Configure:
   - **URL**: Your webhook endpoint
   - **Method**: POST (default)
   - **Headers**: Custom headers (e.g., Authorization)
   - **Secret**: HMAC secret for signature verification

### Signature Verification

Webhooks include an `X-Signature-256` header containing an HMAC-SHA256 signature:

```python
import hmac
import hashlib

def verify_webhook(payload, signature, secret):
    expected = hmac.new(
        secret.encode(),
        payload.encode(),
        hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(f"sha256={expected}", signature)
```

## Incident Thresholds

Configure when notifications are sent per check:

| Setting | Default | Description |
|---------|---------|-------------|
| `incident_threshold` | 1 | Failures before creating incident |
| `escalation_threshold` | 3 | Failures before escalation |
| `recovery_threshold` | 1 | Successes before resolving |

### Example

```
incident_threshold: 2    # Notify after 2 consecutive failures
escalation_threshold: 5  # Escalate after 5 consecutive failures
recovery_threshold: 2    # Resolve after 2 consecutive successes
```

:::tip On-call rotations
For rotation schedules and multi-step paging (page the next person if nobody acknowledges), see [On-Call & Escalation](/features/on-call).
:::

## Testing Notifications

Test your notification setup:

1. Create a check for a known-failing endpoint
2. Wait for the incident to trigger
3. Verify notifications are received
4. Fix the endpoint and verify resolution notification

Or use the API:

```bash
# Send test notification (requires auth)
curl -X POST http://localhost:4000/api/v1/orgs/default/connections/{uid}/test \
  -H "Authorization: Bearer $TOKEN"
```
