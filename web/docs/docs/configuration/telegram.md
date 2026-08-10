---
sidebar_position: 9
title: Telegram
---

# Telegram Alerts

SolidPing can page on-call engineers over **Telegram**, using the official Bot
API directly. It is the only major push channel that costs **nothing per
message**: no Twilio balance, no template approval, no messaging tier.

Telegram sits alongside SMS, voice and WhatsApp as a **direct channel**: a user
connects their own chat, and severities route to it with the `telegram` channel
token.

## How it works

- **One bot per installation.** The credentials are instance-level, exactly like
  SMTP — not per organization. A SaaS deployment supplies one set; a self-hoster
  supplies their own. No code path differs.
- **Users connect, they do not verify.** A user clicks **Connect Telegram** under
  **Account → Notifications**, which opens `t.me/<yourbot>?start=<token>` and
  they press **Start**. That is it — there is no code to type. Pressing Start
  proves the user controls the chat and *is* the opt-in.
- **Opt-out works from inside Telegram.** `/stop` (or `/unlink`) in the chat, or
  simply blocking the bot, removes the contact. Users never have to go back to
  the dashboard to make the alerts stop.
- **Alerts thread per incident.** The first alert for an incident anchors a
  thread; later alerts reply to it, and the original is edited to ✅ when the
  incident resolves. Threading is a nicety: if it cannot be done, the alert is
  still delivered as a standalone message.

## Step 1 — Create the bots with @BotFather

:::caution You need TWO bots: one for dev, one for production
A Telegram bot can hold exactly **one** webhook URL. Pointing your production
bot at a staging host silently breaks production alerting, and there is no way
to have both at once. The dev/prod split is a platform constraint, not a
convention — create `YourApp` and `YourApp (dev)` up front.
:::

Talk to [@BotFather](https://t.me/BotFather) and run:

| Command | What to set |
|---|---|
| `/newbot` | Name and username. **Save the token** — it is shown once. |
| `/setabouttext` | Short blurb shown on the bot's profile. |
| `/setdescription` | What users see before they press Start. |
| `/setuserpic` | Your logo. |
| `/setcommands` | Exactly the two commands below — nothing more. |
| `/setprivacy` | **Enable** (the bot only sees commands addressed to it). |
| `/setjoingroups` | **Enable** — see the note below. |
| `/setinline` | **Disable** (SolidPing implements no inline queries). |

For `/setcommands`, paste exactly:

```text
start - Connect this chat to your SolidPing account
stop - Stop receiving SolidPing alerts here
```

:::note Do not advertise commands that answer nothing
It is tempting to add `status` or `mute`. SolidPing does not implement them in
this version, and a bot whose command menu offers a command that silently does
nothing is worse than one with a short menu.
:::

`/setjoingroups` stays **enabled** even though this version only supports
per-user direct messages. Group routing is a plausible later addition, and
leaving it on means adding it would be additive rather than a breaking
re-registration of the bot.

## Step 2 — Configure SolidPing

All settings are environment variables (or the equivalent `telegram:` block in
the config file). **`SP_TELEGRAM_BOT_TOKEN` and `SP_TELEGRAM_WEBHOOK_SECRET`
are secrets**: keep them in your secret store (SSM, Docker secrets, Kubernetes
secrets), never in a committed config file. SolidPing never logs them, never
returns them from an API, and never sends them to a browser.

| Setting | Environment variable | Secret | Notes |
|---|---|---|---|
| Kill switch | `SP_TELEGRAM_ENABLED` | no | Only ever turns the feature *off*; it never turns it on by itself. |
| Bot token | `SP_TELEGRAM_BOT_TOKEN` | **yes** | `123456789:AA…` from @BotFather. |
| Bot username | `SP_TELEGRAM_BOT_USERNAME` | no | e.g. `solidping_bot`. Public by nature — the browser needs it to build the connect link. |
| Webhook secret | `SP_TELEGRAM_WEBHOOK_SECRET` | **yes** | High-entropy, **at least 32 bytes**. It is the *only* authenticity gate on the webhook. |
| API base URL | `SP_TELEGRAM_BASE_URL` | no | Overrides `https://api.telegram.org`. For an egress proxy or a test fake. |

```bash
SP_TELEGRAM_ENABLED=true
SP_TELEGRAM_BOT_TOKEN=123456789:AAExampleTokenReplaceMe   # SECRET
SP_TELEGRAM_BOT_USERNAME=solidping_bot
SP_TELEGRAM_WEBHOOK_SECRET=$(openssl rand -hex 32)        # SECRET
```

```yaml
telegram:
  enabled: true
  bot_token: "123456789:AAExampleTokenReplaceMe"
  bot_username: solidping_bot
  webhook_secret: replace-with-32-random-bytes
```

The feature is **off unless all three of** `enabled`, `bot_token` and
`bot_username` are set. The username is part of the rule on purpose: without it
the dashboard cannot build a connect link, so the feature would be half-on —
sends would work but nobody could ever connect a chat.

:::tip The bot username is checked at boot
On startup SolidPing calls `getMe` and compares the answer to
`SP_TELEGRAM_BOT_USERNAME`. A mismatch is a **loud warning, not a crash** — a
stale username produces connect links pointing at the wrong bot, which is
otherwise a silent and utterly baffling failure ("the link opens a bot that does
nothing").
:::

## Step 3 — Register the webhook

Inbound updates (the connect flow, `/stop`, block notifications) arrive at:

```text
https://<your-solidping-host>/api/v1/integrations/telegram/webhook
```

**SolidPing registers this for you at startup** whenever your configured server
base URL differs from what Telegram currently has, so a deploy to a new hostname
self-heals. To do it by hand:

```bash
curl -sS "https://api.telegram.org/bot$SP_TELEGRAM_BOT_TOKEN/setWebhook" -d "url=https://solidping.io/api/v1/integrations/telegram/webhook" -d "secret_token=$SP_TELEGRAM_WEBHOOK_SECRET" -d 'allowed_updates=["message","my_chat_member"]'
```

Check what is currently registered:

```bash
curl -sS "https://api.telegram.org/bot$SP_TELEGRAM_BOT_TOKEN/getWebhookInfo"
```

The route only exists when Telegram is configured — an installation without
credentials exposes no Telegram endpoint at all.

### Webhook security

Every `POST` must carry an `X-Telegram-Bot-Api-Secret-Token` header matching
`SP_TELEGRAM_WEBHOOK_SECRET`. SolidPing compares it in constant time **before
parsing the body**, and answers `403` with no detail on a missing or mismatched
value.

:::danger Telegram does not sign its payloads
Unlike Meta's WhatsApp webhook, there is no HMAC over the body — **the shared
secret is the only line of defense** on an endpoint that can create paging
contacts. Use at least 32 random bytes, keep it in your secret store, and rotate
it with the bot token.
:::

## Step 4 — Users connect their chats

Each user goes to **Account → Notifications**, clicks **Connect Telegram**,
and presses **Start** in the Telegram chat that opens.

- The connect link is **single-use** and expires after **15 minutes**. Replaying
  one does nothing; the dashboard offers a fresh link once it lapses.
- The bot replies confirming the link and **naming the organization**, so a user
  who connected the wrong account notices immediately.
- Connecting the same chat again updates the existing contact instead of adding
  a duplicate.

:::note You cannot type a chat id
Telegram contacts cannot be created through the generic
`POST /notification-contacts` API — that request is rejected. There is no
verification round-trip that could catch a wrong chat id, so accepting one from
a request body would let any user page a stranger.
:::

## Step 5 — Route severities to Telegram

Telegram follows the same routing rule as email and SMS:

| Escalation step | Telegram fires? |
|---|---|
| No severity attached | **Yes** — every channel the user connected is used |
| Severity listing `telegram` | **Yes** |
| Severity listing other channels but not `telegram` | **No** |

So an escalation with no severity reaches a connected chat, and adding the
`telegram` token to a severity is how you opt *specific* severities in once you
start scoping them.

:::note Why not explicit-token-only, like voice?
`voice` and `whatsapp` fire **only** on an explicit token, because each of those
deliveries costs money and interrupts hard — nobody should discover a surprise
phone call. Telegram is free and lands in the same place the user already reads
alerts, so connecting the chat is treated as the opt-in, exactly as adding an
email address is.
:::

## Quotas and runaway protection

Telegram messages are free, so — unlike SMS, voice and WhatsApp — there is
**no monthly quota and no usage counter** for this channel. Metering a free
channel would be metering for its own sake.

One limit still applies:

| Limit | Default | Purpose |
|---|---|---|
| `SP_ENTITLEMENTS_TELEGRAM_RUNAWAY_PER_HOUR` | `60` | Per-organization hourly guard against a flapping check or a broken dispatch loop |

Telegram's own limits (roughly 1 message/second per chat and 30/second overall)
are absorbed by the job queue; when Telegram returns `429`, SolidPing honors the
`retry_after` it supplies rather than a generic backoff.

Exceeding the guard skips the send and records it in the notification history;
it never fails the escalation step.

## Troubleshooting

| Symptom in the delivery history | Meaning | Fix |
|---|---|---|
| *The Telegram bot was blocked by the user* | The user blocked the bot or deleted the chat | The contact is automatically marked **Reconnect needed**; the user reconnects from Account → Notifications |
| *The Telegram chat no longer exists* | The chat id no longer resolves | Same as above — the contact needs reconnecting |
| *Telegram bot credentials are invalid* | The bot token is wrong or was revoked | Re-issue it with `/revoke` then `/token` in @BotFather and update `SP_TELEGRAM_BOT_TOKEN` |
| *Rate limited by Telegram* | Telegram throttled the send | Transient; the next escalation repeat honors Telegram's own `retry_after` |
| *Telegram is not configured on this instance* | The kill switch is off, or a credential is missing | Check `SP_TELEGRAM_ENABLED`, `SP_TELEGRAM_BOT_TOKEN`, `SP_TELEGRAM_BOT_USERNAME` |
| *Hourly Telegram runaway guard reached* | Too many sends for one org in one hour | Usually a flapping check; raise `SP_ENTITLEMENTS_TELEGRAM_RUNAWAY_PER_HOUR` only once you know why |
| Nothing happens after pressing Start | The webhook is not reaching you | `getWebhookInfo` shows `last_error_message`; check the URL, TLS, and that the secret matches |
| Alerts stop after a hostname change | The old webhook URL is still registered | Restart SolidPing (it re-registers) or re-run the `setWebhook` call above |

### Rotating the bot token

`/revoke` in @BotFather invalidates the current token and issues a new one.
Update `SP_TELEGRAM_BOT_TOKEN`, restart, and confirm the boot log says
`Telegram bot ready`. Existing connected chats are unaffected — the chat ids
belong to the bot's identity, not to the token.

## Limits of the current version

- **Webhook only.** SolidPing does not support long-polling (`getUpdates`), so a
  self-hosted install with no publicly reachable HTTPS URL cannot use Telegram
  today. This is a deliberate cut, not an oversight — but it is a real gap for
  an OSS product, and long-polling is a candidate for a follow-up.
- **Direct messages only.** No group or channel routing yet (`/setjoingroups`
  stays enabled so adding it later is additive). Using a connect link inside a
  group is refused with a note to open a direct chat instead — and the link is
  *not* consumed, so the same one still works in a DM.
- **No bring-your-own bot per organization** — credentials are instance-level.
- **No interactive buttons or commands beyond `/start` and `/stop`.** You cannot
  acknowledge an incident by replying (use the SMS ack link or the dashboard).
- Status-page subscribers cannot subscribe over Telegram.
