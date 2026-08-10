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

**A bot token is all you need.** Everything else SolidPing works out for
itself on its first boot and remembers.

| Setting | Environment variable | Required | Secret | Notes |
|---|---|---|---|---|
| Bot token | `SP_TELEGRAM_BOT_TOKEN` | **yes** | **yes** | `123456789:AA…` from @BotFather. It *is* the bot's identity. |
| Kill switch | `SP_TELEGRAM_ENABLED` | no | no | **Leave unset.** Unset = auto (on iff a token is present); `false` = off whatever else is configured; `true` = explicitly on, but still needs a token. |
| Bot username | `SP_TELEGRAM_BOT_USERNAME` | no | no | Derived from `getMe` and persisted. Set it only to skip that call or under declarative/GitOps config. |
| Webhook secret | `SP_TELEGRAM_WEBHOOK_SECRET` | no | **yes** | Generated (32 random bytes, base64url) and persisted when unset. Set it by hand only when something else must know it too. |
| API base URL | `SP_TELEGRAM_BASE_URL` | no | no | Overrides `https://api.telegram.org`. For an egress proxy or a test fake. |

So the whole configuration is:

```bash
SP_TELEGRAM_BOT_TOKEN=123456789:AAExampleTokenReplaceMe   # SECRET
```

or, in the config file:

```yaml
telegram:
  bot_token: "123456789:AAExampleTokenReplaceMe"
```

**`SP_TELEGRAM_BOT_TOKEN` and `SP_TELEGRAM_WEBHOOK_SECRET` are secrets**: keep
them in your secret store (SSM, Docker secrets, Kubernetes secrets), never in a
committed config file. SolidPing never logs them, never returns them from an
API, and never sends them to a browser.

### What SolidPing derives, and where it keeps it

On the first boot with a token, **before any route is served**, SolidPing
resolves the two remaining values and stores them as system parameters:

| Parameter | Secret | Where it comes from |
|---|---|---|
| `telegram.bot_username` | no | One `getMe` call, bounded at 3 seconds. Only ever made when neither the environment nor the database knows the username — after the first boot, startup makes no network call at all. |
| `telegram.webhook_secret` | **yes** | 32 bytes of `crypto/rand`, base64url-encoded. |

Resolution order for both is **environment → stored parameter → derived**. An
explicitly configured value always wins and the stored parameter is left
untouched, so an operator running GitOps config, or fronting the webhook with a
proxy that must know the secret, is never fought by the server.

Creation is atomic, so several API pods booting at once converge on **one**
secret rather than each generating its own.

:::note First boot with only a token
If Telegram is unreachable during that first `getMe`, startup still completes
(within the 3-second bound) and everything except the *connect* surface works:
the webhook route is live and alerts to already-connected chats still go out.
The **Connect Telegram** button stays hidden until a later boot resolves the
username — no operator action needed.
:::

:::tip An explicitly configured username is still checked at boot
When you *do* set `SP_TELEGRAM_BOT_USERNAME`, SolidPing calls `getMe` and
compares. A mismatch is a **loud warning, not a crash** — a stale username
produces connect links pointing at the wrong bot, which is otherwise a silent
and utterly baffling failure ("the link opens a bot that does nothing"). A
derived username has nothing to disagree with, so it warns about nothing.
:::

## Step 3 — Register the webhook

Inbound updates (the connect flow, `/stop`, block notifications) arrive at:

```text
https://<your-solidping-host>/api/v1/integrations/telegram/webhook
```

**SolidPing registers this for you on every startup**, so a deploy to a new
hostname — or a rotated webhook secret — self-heals. The registration is
unconditional on purpose: Telegram's `getWebhookInfo` returns the registered
`url` but **never** the `secret_token`, so a URL comparison could not detect a
secret that changed at a constant URL. `setWebhook` is idempotent, so this costs
one API call per boot.

To do it by hand:

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
| *Telegram is not configured on this instance* | No bot token, or `SP_TELEGRAM_ENABLED=false` | Check `SP_TELEGRAM_BOT_TOKEN`; make sure `SP_TELEGRAM_ENABLED` is unset or `true` |
| *Hourly Telegram runaway guard reached* | Too many sends for one org in one hour | Usually a flapping check; raise `SP_ENTITLEMENTS_TELEGRAM_RUNAWAY_PER_HOUR` only once you know why |
| The **Connect Telegram** button never appears | The bot username is not resolved yet — the first `getMe` failed | Check outbound access to `api.telegram.org` and restart; or set `SP_TELEGRAM_BOT_USERNAME` explicitly |
| Nothing happens after pressing Start | The webhook is not reaching you | `getWebhookInfo` shows `last_error_message`; check the URL, TLS, and that the secret matches |
| Alerts stop after a hostname change | The old webhook URL is still registered | Restart SolidPing (it re-registers) or re-run the `setWebhook` call above |

### Rotating the bot token

`/revoke` in @BotFather invalidates the current token and issues a new one.
Update `SP_TELEGRAM_BOT_TOKEN`, restart, and confirm the boot log says
`Telegram bot ready`. Existing connected chats are unaffected — the chat ids
belong to the bot's identity, not to the token.

### Rotating the webhook secret

If the secret is **derived** (the default), delete the system parameter as a
super-admin and restart:

```bash
curl -sS -X DELETE -H "Authorization: Bearer $TOKEN" \
  'https://<your-solidping-host>/api/v1/system/parameters/telegram.webhook_secret'
```

The next boot generates a fresh one, persists it, and pushes it to Telegram with
`setWebhook` — which now happens unconditionally, so the new secret actually
lands. (Before that fix, a secret changing at a constant URL was never
re-registered and Telegram kept echoing the old one, 403'ing every update.)

If the secret is **explicitly configured**, change `SP_TELEGRAM_WEBHOOK_SECRET`
and restart; the stored parameter, if any, is left untouched.

### Re-deriving the bot username

Delete the `telegram.bot_username` parameter the same way and restart — the next
boot re-fetches it with `getMe`.

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
