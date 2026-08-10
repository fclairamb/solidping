---
model: opus
effort: high
---

# Telegram alert channel via an instance-owned bot

## Problem

Telegram is a first-class alerting destination for the self-hosting/devops
audience SolidPing targets, and it is the only major push channel that costs
nothing per message — no Twilio balance, no Meta template approval, no
messaging tier. Today the repo has no Telegram support at all: only an idea
sketch (`specs/ideas/2026-03-22-telegram-notifications.md`), and that sketch
describes the **wrong model** for our deployment.

That idea proposes a BYO-bot org connection: every org creates its own bot via
@BotFather and pastes a token. That works, but it pushes a five-minute
BotFather detour onto every single user before they can receive their first
alert, and it means SolidPing can never send anything until the user has done
it. WhatsApp already established the better pattern here
(`specs/done/2026/08/2026-08-02-09-whatsapp-alert-channel.md`): **one
instance-level identity, users just connect**. Telegram fits that pattern even
better than WhatsApp does, because the connect step needs no verification code
round-trip — pressing *Start* in Telegram is itself the proof of reachability
and the opt-in.

The two bots are already being provisioned (2026-08-10): **`SolidPing`** for
prod and **`SolidPing (dev)`** for dev. A Telegram bot can hold only one
webhook URL, so the dev/prod split is a hard requirement of the platform, not a
convenience.

## Proposal

A `telegram` **direct channel** — a synthetic severity channel token and a
per-user contact type, exactly like `whatsapp`/`sms`, *not* an org-level
connection type — delivered through a new
`server/internal/integrations/telegram/` package talking to
`https://api.telegram.org`.

The shape deliberately mirrors the WhatsApp feature so there is one mental
model for instance-level direct channels, with three substantive differences
called out below: the **connect flow replaces verification**, there is **no
per-message quota**, and **blocked-by-user is a first-class state** that must
disable the contact.

### Instance-level credentials

`TelegramConfig{Enabled, BotToken, BotUsername, WebhookSecret, BaseURL}` in
`server/internal/config/config.go`, mirroring `WhatsAppConfig`
([config.go:692](server/internal/config/config.go#L692)), default
`Enabled: false`.

| Field | Env | Secret | Notes |
|---|---|---|---|
| `Enabled` | `SP_TELEGRAM_ENABLED` | no | Kill switch only; never turns the feature *on* by itself. |
| `BotToken` | `SP_TELEGRAM_BOT_TOKEN` | **yes** | `123456789:AA…` from @BotFather. Never logged, never in the public config. |
| `BotUsername` | `SP_TELEGRAM_BOT_USERNAME` | no | e.g. `solidping_bot`. Public by nature — the browser needs it to build the deep link. |
| `WebhookSecret` | `SP_TELEGRAM_WEBHOOK_SECRET` | **yes** | High-entropy (≥32 bytes); it is the *only* authenticity gate on the webhook. |
| `BaseURL` | `SP_TELEGRAM_BASE_URL` | no | Overrides `https://api.telegram.org`; the `httptest` seam. |

- `Active()` = `Enabled && BotToken != "" && BotUsername != ""`. The username is
  part of the enablement rule, not optional: without it the dashboard cannot
  build a connect link, so the feature would be half-on.
- Multi-word koanf keys (`bot_token`, `bot_username`, `webhook_secret`,
  `base_url`) need the manual `SP_*` env reader — the same quirk as
  `rate_limiting`/`whatsapp`. Register the names in
  `server/internal/config/envvars.go` (`manualReaderPlatformEnvVars`) and add a
  `TelegramEnvVarNames()` alongside `WhatsAppEnvVarNames()`
  ([config.go:1802](server/internal/config/config.go#L1802)).
- Dev and prod each get their **own bot and their own token** — one bot cannot
  serve two webhooks. Local dev points at the dev bot via `.env`; credentials
  are kept out of git as `telegram_bot_solidping_{dev,prod}.priv.json`, matching
  the existing `discord_app_client_solidping_*.priv.json` convention (`*.priv*`
  is already gitignored, [.gitignore:7](.gitignore#L7)).

### Public capability flag

`publicconfig` ([handler.go](server/internal/handlers/publicconfig/handler.go))
gains `TelegramPublicConfig{Enabled bool, BotUsername string}` on `Response`,
following the `WhatsAppPublicConfig` precedent. `Enabled` is the resolved
`Active()` rule, not the raw kill switch. `BotUsername` is emitted **only when
enabled** (`omitempty`) — it is not a secret (anyone can find the bot in
Telegram), but an operator must be able to see at a glance that an unconfigured
instance emits nothing.

### Client package — `server/internal/integrations/telegram/`

`client.go`:

- `NewClientFromConfig(cfg)` / `NewClientWithBaseURL(...)` (httptest seam),
  mirroring the WhatsApp client's constructor pair
  ([whatsapp/client.go](server/internal/integrations/whatsapp/client.go)).
- `SendMessage(ctx, *Message) (messageID int64, err error)` →
  `POST /bot{token}/sendMessage`, with `parse_mode: "HTML"`,
  `link_preview_options: {is_disabled: true}` (a link preview on every alert is
  noise), optional `reply_to_message_id`, optional `message_thread_id`.
- `EditMessageText(ctx, chatID, messageID, html)` →
  `POST /bot{token}/editMessageText`, used to mark the original alert resolved.
- `GetMe(ctx)` → `POST /bot{token}/getMe`, used by a startup sanity check and to
  confirm the configured `BotUsername` actually matches the token. A mismatch is
  a **loud warning at boot, not a crash** — a stale username produces connect
  links pointing at the wrong bot, which is otherwise a silent, baffling
  failure.
- `SetWebhook(ctx, url, secret)` → `POST /bot{token}/setWebhook` with
  `allowed_updates: ["message","my_chat_member"]`. Called at boot when webhook
  mode is active and the registered URL differs, so a deploy to a new hostname
  self-heals instead of requiring a manual curl.
- **HTML escaping is mandatory and centralized**: every interpolated value
  (check name, incident title, org slug) goes through one `escapeHTML` helper
  before it reaches a template. Telegram rejects the whole message on malformed
  entities, so an unescaped `&` in a check name would silently kill alerting for
  that check. This is the single most likely production bug in the feature and
  must be tested directly.

Typed errors classified from the API response
(`ok:false`, `error_code`, `description`, `parameters.retry_after`):

| Error | Trigger | Classification |
|---|---|---|
| `ErrUnauthorized` | 401 — bad/revoked bot token | Permanent, instance-wide. Log loudly. |
| `ErrBotBlocked` | 403 `bot was blocked by the user` / `user is deactivated` | Permanent, **per contact** — see below. |
| `ErrChatNotFound` | 400 `chat not found` | Permanent per contact. |
| `ErrRateLimited` | 429, carries `RetryAfter` | Transient. |
| `ErrRequestFailed` | 5xx / transport | Transient fallback. |

Plus `FailureReason(err) string` returning a stable human string for the
delivery-history audit row, mirroring `whatsapp.FailureReason`.

### Connect flow — replaces the verification round-trip

This is the main structural departure from WhatsApp. There is **no code to
type**, and correspondingly **no use of the `verify`/`verify/confirm`
endpoints**. `telegram` must NOT be added to
`models.VerifiableContactTypes()` ([user_contact.go:27](server/internal/db/models/user_contact.go#L27)).

1. Dashboard calls a new `POST /api/v1/orgs/:org/users/me/telegram/link`.
   The server mints a single-use, high-entropy token (URL-safe, ≥128 bits) and
   stores it via `SetStateEntry` (org-scoped, key `telegram_link:<token>`,
   value `{userUID, orgUID}`, **TTL 15 minutes**) — the same mechanism Discord
   OAuth already uses for its state parameter
   ([discord_service.go:154](server/internal/handlers/auth/discord_service.go#L154)).
   Response: `{"url": "https://t.me/<botUsername>?start=<token>", "expiresAt": …}`.
2. The user opens the link and presses **Start**. Telegram delivers
   `/start <token>` to the webhook.
3. The webhook resolves the token via `GetStateEntry`, **deletes it
   immediately** (single use — `DeleteStateEntry` returns whether a live entry
   was removed, so a replayed token is a no-op, not a second contact), and
   creates a `UserContact{Type: "telegram", Value: <chat_id>, VerifiedAt: now}`.
   Pressing Start *is* the verification: it proves the user controls that chat
   and constitutes the opt-in.
   `Label` is the Telegram `@username`, falling back to the first name, so the
   contacts list shows something human rather than a bare numeric id.
4. The bot replies in-chat confirming the link and naming the org, so a user who
   connected the wrong account notices instantly.

Security requirements on this flow, all of which need a test:

- **`telegram` contacts may never be created through the generic
  `POST /notification-contacts` endpoint.** Rejected with `VALIDATION_ERROR`.
  Without this rule any user could enter an arbitrary `chat_id` and page a
  stranger, since there is no verification step to stop them.
- An expired or unknown token gets a polite in-chat "this link has expired,
  generate a new one from your dashboard" — never a stack trace, never a hint
  about whether the token merely expired or never existed.
- Re-linking an already-linked chat updates the existing contact (refreshes the
  label, re-verifies) rather than creating a duplicate.
- `/stop` (or `/unlink`) in the chat, and the `my_chat_member` update reporting
  the bot was blocked, both delete the contact — the user must be able to opt
  out from Telegram itself, without going back to the dashboard.

### Inbound webhook — `server/internal/handlers/telegramcb/`

Modeled on [whatsappcb](server/internal/handlers/whatsappcb/handler.go), on the
public `api` group, wired only when `cfg.Telegram.Active()`.

- `POST /api/v1/integrations/telegram/webhook`.
- **Authenticity comes solely from the `X-Telegram-Bot-Api-Secret-Token`
  header**, compared constant-time against `WebhookSecret` *before the body is
  parsed*. Unlike Meta, Telegram does not sign the payload — there is no second
  line of defense, which is why the secret must be high-entropy and why a
  missing/mismatched header is a bare 403 with no body detail.
- Body capped like `maxWebhookBody` (1 MiB) before the check, bounding what an
  unauthenticated caller can make us allocate.
- Handled updates: `message` (only `/start <token>`, `/stop`, `/unlink`; all
  other text is info-logged and ignored in v1) and `my_chat_member` (bot blocked
  or kicked → disable/delete the affected contact).
- Always returns 200 once authenticated, even on an unhandled update type —
  Telegram retries non-2xx and would otherwise loop on an update we will never
  process.

### Escalation dispatch

- `models.UserContactTypeTelegram = "telegram"` in
  [user_contact.go](server/internal/db/models/user_contact.go). No migration:
  `user_contacts.type` is free-form text.
- `"telegram"` in `severities.allowedChannels()`
  ([severities/service.go:46](server/internal/handlers/severities/service.go#L46)),
  `channelTokenTelegram` + `severityAllowsTelegram(filter)` (explicit token or
  nil filter), and `telegram` added to `severityAllowsPersonTargets`.
- New `server/internal/jobs/jobtypes/job_escalation_step_telegram.go` with
  `pageTelegram`, closely mirroring `pageWhatsApp`
  ([job_escalation_step_whatsapp.go](server/internal/jobs/jobtypes/job_escalation_step_whatsapp.go)):
  unverified → skip; instance inactive → info-log skip; send; audit row; **a
  send failure returns 0 so the step falls through to the next escalation
  step**, exactly as SMS and WhatsApp failures do.
- `dispatchRoute` case in
  [job_escalation_step.go:507](server/internal/jobs/jobtypes/job_escalation_step.go#L507).
- **On `ErrBotBlocked` / `ErrChatNotFound`, clear the contact's `VerifiedAt`**
  in addition to the audit row. A blocked contact that stays "verified" is a
  route that silently swallows every future page — the worst failure mode a
  paging channel has. The dashboard then shows it as needing reconnection.

### Message format & per-incident threading

HTML body, one shape across the incident lifecycle:

```html
<b>🔴 Incident — Website Homepage</b>

<b>Status:</b> DOWN
<b>Detail:</b> HTTP 503 — Service Unavailable
<b>Org:</b> acme

<a href="https://solidping.io/dash0/orgs/acme/incidents/abc-123">View incident →</a>
```

Emoji/state by incident state, reusing the `whatsAppStateLabel` logic shape:
🔴 DOWN, 🟠 ESCALATED, 🟢 RESOLVED.

Threading keeps one incident in one Telegram conversation thread instead of
scattering four standalone messages:

- The first message for an incident+chat stores its `message_id` in a state
  entry (org-scoped, key `telegram_msg:<incidentUID>:<chatID>`, TTL a few days —
  well past any incident lifetime, and it must expire so the table does not grow
  without bound).
- Subsequent alerts for that incident send with `reply_to_message_id`.
- On resolution, additionally `editMessageText` the original to prefix ✅ and
  mark it resolved, so someone scrolling back does not read a stale red alert as
  live.
- A missing/expired state entry, or a `reply_to_message_id` Telegram rejects
  (the user deleted the original), must degrade to sending a plain standalone
  message. **Threading is a nicety; it may never cost a delivery.** Test this
  path explicitly.

> The idea sketch claimed this mirrors an existing Slack `message_ts` threading
> pattern for incident notifications. That pattern was **not found** in the
> codebase — Slack's `thread_ts` handling is in the interactive command path
> ([slack/mention_commands.go:564](server/internal/integrations/slack/mention_commands.go#L564)),
> not in escalation alerts. Build this on state entries as described; do not go
> looking for a Slack helper to reuse.

### Quota and rate limiting

Telegram messages are free, so — unlike SMS/voice/WhatsApp — **there is no
`maxTelegramPerMonth` entitlement and no `org_usage_counter` kind**. Adding a
monthly cap to a free channel would be metering for its own sake.

What is still needed:

- The **hourly runaway guard**, to bound damage from a flapping check or a
  dispatch loop: `SP_ENTITLEMENTS_TELEGRAM_RUNAWAY_PER_HOUR`, default 60
  (double the WhatsApp default — this channel is free, the guard is about
  sanity, not cost).
- Respect Telegram's limits: ~1 message/second per chat, ~30/second overall,
  ~20/minute to a group. The existing job queue and retry logic absorb this
  naturally; on 429 the retry must honor `parameters.retry_after` rather than
  the generic backoff.

### Frontend (dash0)

Per the repo rule, start from
[design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx) and
reuse its primitives; add any genuinely new primitive to that page.

- `routes/orgs/$org/account.notifications.tsx`: a **"Connect Telegram"** button
  (not an input field — there is nothing for the user to type). It calls the
  link endpoint, opens `t.me/…?start=…` in a new tab, and polls the contacts
  list until the new contact appears, with a visible expiry countdown and a
  "generate a new link" affordance once it lapses.
- A contact whose `VerifiedAt` was cleared by a block renders as **"Reconnect
  needed"** with the same connect action, not as a generic unverified contact.
- Hidden entirely when the public-config flag is off, via a
  `useTelegramEnabled()` hook over `GET /api/v1/config` — extend the existing
  WhatsApp hook's file rather than adding a second fetch of the same document.
- Telegram icon + label in `components/integrations/integration-icon.tsx` and
  `lib/channel-labels.ts` (the latter already exists to stop raw tokens being
  CSS-capitalized).
- `me.notifications.tsx` delivery history renders the channel and its failure
  reasons ("bot blocked by user").
- Fully responsive; i18n keys in en/fr/de/es.

### Docs

New page under `web/docs/docs/`:

- Creating the bot with @BotFather — `/newbot`, then `/setabouttext`,
  `/setdescription`, `/setuserpic`, `/setcommands`, `/setprivacy` **Enable**,
  `/setjoingroups`, `/setinline` **Disable**.
- Why dev and prod need two separate bots (one webhook per bot).
- The `SP_TELEGRAM_*` env matrix.
- Webhook registration, for operators not relying on the boot-time
  `setWebhook`:
  ```bash
  curl -sS "https://api.telegram.org/bot$SP_TELEGRAM_BOT_TOKEN/setWebhook" -d "url=https://solidping.io/api/v1/integrations/telegram/webhook" -d "secret_token=$SP_TELEGRAM_WEBHOOK_SECRET" -d 'allowed_updates=["message","my_chat_member"]'
  ```
- Troubleshooting table mapping each typed error to an operator action, and
  `/revoke` in @BotFather for token rotation.

### Tests

- **Client** against an `httptest` fake Bot API: `sendMessage` payload shape
  (path carries the token, parse mode, disabled preview, reply id); one case per
  typed error class; `retry_after` extraction from 429.
- **HTML escaping**: a check named `A & B <script>` produces a body Telegram
  accepts, with the entity escaped — the highest-value single test in this spec.
- **Webhook**: rejects missing / wrong / malformed secret header *before*
  parsing; accepts a valid one; unhandled update types still 200.
- **Connect flow**: `/start <token>` creates a verified contact bound to the
  right user and org; a replayed token creates nothing; an expired token
  replies politely and creates nothing; re-linking updates instead of
  duplicating; `POST /notification-contacts` with `type: telegram` is rejected.
- **Opt-out**: `/stop` and a `my_chat_member` block update both remove the
  contact.
- **Escalation**: telegram route sends; send failure returns 0 and the step
  falls through; an unverified contact is never messaged; `ErrBotBlocked`
  clears `VerifiedAt`.
- **Threading**: second alert replies to the first; a rejected
  `reply_to_message_id` still delivers a standalone message; resolution edits
  the original.
- **E2E (dash0)**: connect button visible only under the public-config flag;
  contact appears after a mocked webhook link.
- Test seams injected per-service/per-test, never a mutated package global.

### Manual E2E

Against the real **`SolidPing (dev)`** bot, with the laptop tunnel up
(`solidping-laptop.sh on`, webhook → `https://solidping.k8xp.com/...`):
generate a link from the dashboard, press Start, trigger an incident, block the
bot and confirm the contact flips to "Reconnect needed".

### Out of scope (v1)

- **Group and channel routing.** Only per-user DM contacts here. `/setjoingroups`
  stays enabled on both bots so adding org-level group routing later is
  additive, not a breaking re-registration.
- **Long-polling (`getUpdates`) mode.** Webhook-only in v1, matching the
  WhatsApp precedent. This does exclude self-hosters with no public URL, which
  is a real gap for an OSS product — it is a deliberate v1 cut, not an
  oversight, and is worth a follow-up spec. Document the limitation on the docs
  page.
- BYO-bot org connections (the idea sketch's model). The config shape does not
  preclude adding it later as a `telegram` *connection type* alongside this
  contact type.
- Interactive buttons, ack-via-reply, `/status` and `/mute` commands. The
  BotFather command list registered at setup advertises them, so either trim
  that list to `start`/`stop` at setup time or ship the commands — **do not
  leave advertised commands that answer nothing.**
- Telegram Login Widget as an auth provider (`/setdomain`) — unrelated feature.
- Status-page subscriber notifications over Telegram.

## Implementation Plan

Derived from the sections above; each step is one (or a few) commits.

1. **Config** — `TelegramConfig{Enabled, BotToken, BotUsername, WebhookSecret, BaseURL}`
   in `internal/config/config.go` with `Active()`, defaults (`Enabled:false`),
   `applyTelegramEnv` + `TelegramEnvVarNames()` (manual `SP_*` reader),
   registration in `envvars.go` `manualReaderPlatformEnvVars`, and
   `EntitlementsConfig.TelegramRunawayPerHour` +
   `SP_ENTITLEMENTS_TELEGRAM_RUNAWAY_PER_HOUR` (default 60).
2. **Public capability flag** — `TelegramPublicConfig{Enabled, BotUsername omitempty}`
   on `publicconfig.Response`, emitted only when `Active()`.
3. **Client package** — `internal/integrations/telegram/`: `NewClientFromConfig` /
   `NewClientWithBaseURL`, `SendMessage`, `EditMessageText`, `GetMe`, `SetWebhook`,
   centralized `EscapeHTML`, typed errors (`ErrUnauthorized`, `ErrBotBlocked`,
   `ErrChatNotFound`, `ErrRateLimited` w/ `RetryAfter`, `ErrRequestFailed`,
   `ErrNotConfigured`) + `FailureReason`.
4. **Contacts model + severities** — `models.UserContactTypeTelegram` (NOT in
   `VerifiableContactTypes`), `telegram` in `severities.allowedChannels()`,
   `channelTokenTelegram`, `severityAllowsTelegram`, person-target token.
5. **DB helpers** — `ListUserContactsByTypeValue`, `ClearUserContactVerified`
   on `db.Service` + postgres/sqlite implementations.
6. **Connect flow** — `POST /api/v1/orgs/:org/users/me/telegram/link` minting a
   single-use ≥128-bit token in a state entry (`telegram_link:<token>`, TTL 15m),
   returning `{url, expiresAt}`; `telegram` rejected by the generic
   `POST /notification-contacts` with `VALIDATION_ERROR`.
7. **Inbound webhook** — `internal/handlers/telegramcb/`:
   `POST /api/v1/integrations/telegram/webhook`, 1 MiB body cap, constant-time
   `X-Telegram-Bot-Api-Secret-Token` check before parsing, `/start <token>`,
   `/stop` / `/unlink`, `my_chat_member` block → contact removed, always 200
   once authenticated. Registered only when `cfg.Telegram.Active()`.
8. **Escalation dispatch** — `job_escalation_step_telegram.go` with `pageTelegram`
   (unverified skip, inactive skip, runaway reserve, send, audit, failure → 0),
   `dispatchRoute` case, `ErrBotBlocked`/`ErrChatNotFound` clears `VerifiedAt`.
9. **Message format & threading** — HTML body with emoji state label, state
   entries `telegram_msg:<incidentUID>:<chatID>` storing the first `message_id`,
   `reply_to_message_id` on follow-ups, `editMessageText` on resolution, graceful
   degradation to a standalone message on any threading failure.
10. **Boot-time wiring** — `GetMe` sanity check (warn on username mismatch) and
    `SetWebhook` self-heal when the app base URL is known.
11. **Frontend (dash0)** — `useTelegramEnabled()` in `api/public-config.ts`,
    "Connect Telegram" button with countdown + regenerate in
    `account.notifications.tsx`, "Reconnect needed" state, icon + label in
    `integration-icon.tsx` / `channel-labels.ts`, i18n en/fr/de/es, E2E spec.
12. **Docs** — `web/docs/docs/configuration/telegram.md` (BotFather setup,
    dev/prod two-bot rule, env matrix, webhook registration curl, troubleshooting
    table, webhook-only limitation).
13. **Tests** — client (payload shape, typed errors, retry_after), HTML escaping,
    webhook auth/parse/200, connect flow (create/replay/expired/re-link/generic
    endpoint rejection), opt-out, escalation, threading.
