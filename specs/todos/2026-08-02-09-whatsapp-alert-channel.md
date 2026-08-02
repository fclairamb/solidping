---
model: opus
effort: high
---

# WhatsApp alert channel via Meta's Cloud API (direct, no BSP)

## Problem

The SMS/voice work
(specs/done/2026/07/2026-07-22-02-sms-voice-wakeup-alert-channels.md) gave
users phone-based wake-up alerts but explicitly left WhatsApp out of scope.
WhatsApp is cheaper than SMS in most countries, delivers more reliably
internationally, and is the primary messaging app for a large share of our
target users. Routing it through Twilio would limit the channel to orgs that
have BYO-Twilio configured and add per-message markup; Meta's WhatsApp
Business Cloud API is open to direct integration.

This is a **SaaS-first feature**: SolidPing's deployment supplies one
instance-level WhatsApp Business Account (WABA) and every user just verifies
their number. But all code ships in the open-source repo — a self-hoster
configures their own WABA through the same config keys and gets the identical
feature.

## Proposal

A `whatsapp` **direct channel** (like `sms`/`voice` — a synthetic severity
channel token, *not* an org-level connection type), delivered via a new
`server/internal/integrations/whatsapp/` package that talks straight to
`graph.facebook.com`. No BYO-WABA org connections in v1.

### Instance-level credentials (the OSS/SaaS split)

`WhatsAppConfig{Enabled, AccessToken, PhoneNumberID, WABAID, AppSecret,
WebhookVerifyToken, APIVersion, AlertTemplate, VerifyTemplate,
TemplateLanguage}` in server config, mirroring `SlackConfig`
([config.go:563](server/internal/config/config.go#L563)), default
`Enabled: false` like [config.go:934](server/internal/config/config.go#L934).

- `AccessToken` is a permanent system-user token with
  `whatsapp_business_messaging`; `AppSecret` signs inbound webhooks. Both are
  secrets — env/SSM only, never logged.
- Multi-word koanf keys (`phone_number_id`, `webhook_verify_token`, …) need
  the manual `SP_*` env reader — same quirk as `rate_limiting`.
- SaaS supplies `SP_WHATSAPP_*` in its deployment env; self-hosters do the
  same with their own WABA. No code path differs between the two.

### Sender package — `server/internal/integrations/whatsapp/`

- `client.go`: `POST /{api_version}/{phone_number_id}/messages` with
  `type: template` payloads; typed errors for the failure classes we must
  handle (template paused/disabled, recipient not on WhatsApp, rate limit /
  messaging-tier cap, token expired). Base URL injectable for `httptest`.
- Template send only in v1 — alerts are business-initiated, so free-form text
  is not deliverable outside a 24h session anyway.

### Templates (pre-approved on the WABA)

Two templates, names/language configurable (defaults `solidping_alert`,
`solidping_verify`, `en`):

- **Alert** — Meta category *utility*. Variables: check/incident name, new
  state, detail line, org. Body must be written so one template covers
  down/escalate/resolve (state is a variable).
- **Verification code** — Meta category *authentication* (Meta forces the
  copy-code button format for OTP-style templates).

Setup (creating the Meta app, WABA, number, templates, webhook) is documented
in a new `web/docs/` page for self-hosters; SaaS does it once. Template
approval is an offline prerequisite — the server treats "template not
found/paused" as a delivery failure, never a crash.

### Inbound webhook — `server/internal/handlers/whatsappcb/`

Analogous to [twiliocb](server/internal/handlers/twiliocb/handler.go):

- `GET` handshake: echo `hub.challenge` when `hub.verify_token` matches.
- `POST` events validated with `X-Hub-Signature-256` (HMAC-SHA256, app
  secret) before parsing.
- Delivery statuses (`sent`/`delivered`/`read`/`failed`) update the
  notification delivery record like Twilio status callbacks do; `failed` with
  a template/tier error is surfaced in the delivery history.
- Inbound user replies are accepted (and open the free 24h session window)
  but only logged in v1 — no command handling.

### Reuse the channel-agnostic per-recipient machinery

- `whatsapp` contact type in
  [user_contact.go](server/internal/db/models/user_contact.go) (alongside
  `email/phone/slack_user/…`). Deliberately distinct from the `phone` (SMS)
  contact even when the number is the same: verification proves WhatsApp
  reachability and doubles as Meta's required opt-in record.
- Verification round-trip via the authentication template, reusing
  `usernotifications` verify + resend limiter
  (server/internal/usernotifications/verify.go, resend_limiter.go) and the
  existing `/users/me/notification-contacts[/{uid}/verify[/confirm]]`
  endpoints (openapi.yaml ~2034–2130) — only the send transport is new.
- `whatsapp` severity channel token in `allowedChannels()`
  ([severities/service.go:46](server/internal/handlers/severities/service.go#L46)),
  offered only when the instance has WhatsApp enabled — the API must expose
  an instance-capability flag for the frontend (v1: piggyback on whatever
  endpoint the dashboard already uses for instance config; add one if none
  fits — open question below).
- `dispatchRoute` case in
  [job_escalation_step.go](server/internal/jobs/jobtypes/job_escalation_step.go)
  (`pageUser` → `dispatchRoute`, ~438/485): render template variables, send,
  record delivery. A send failure falls through to the next escalation step
  like SMS failures do.
- Per-org monthly quota via `org_usage_counter` + entitlements reservation
  (server/internal/entitlements/usage_reserve.go), mirroring the SMS quota
  (`maxWhatsappPerMonth` entitlement, shown on the Usage page).

### Frontend (dash0)

- `account.notifications.tsx`: add-WhatsApp-number flow (E.164 input →
  authentication-template code → confirm), per-route toggle — mirror the
  existing phone-verify UI. Hidden when the instance capability flag is off.
- Severity channel picker gains the `whatsapp` token under the same flag.
- WhatsApp icon + labels; i18n in en/fr/de/es.
- Delivery history (`me.notifications.tsx`) renders the new channel and
  failure reasons (e.g. "recipient not on WhatsApp").

### Tests

- Client against a fake Graph API (`httptest`): template send payload shape,
  each typed error class.
- Webhook: handshake, signature validation (reject bad/missing signature),
  status → delivery-record updates.
- Verification round-trip with a fake sender; resend limiter applies.
- Escalation dispatch: `whatsapp` route sends, failure advances escalation.
- Quota: reservation blocks at the cap, counter increments on send.
- E2E: contact add/verify flow and channel picker visibility under the
  capability flag (fake Graph API wired in test mode).

#### Manual E2E against the real Meta test number

The Meta side is already provisioned (2026-08-02): app **SolidPing** under the
**Webingenia** business portfolio, with a WABA, a free test sender number, and
the user's own phone verified as a test recipient. For real end-to-end checks
(fresh 24h access token, adding test recipients, triggering a template send,
watching the "Check test webhooks" panel), **use the Chrome MCP
(`claude-in-chrome`) and drive the Meta developer dashboard directly** from the
user's logged-in Chrome session: developers.facebook.com → My Apps → SolidPing
→ WhatsApp use case → Step 1 "Try it out". IDs and credentials live in the
operator's deployment config, not in this repo; anything that submits a form
or accepts Meta terms still needs the user's per-action OK in chat.

### Open questions

- Where the instance-capability flag lives — existing instance/config
  endpoint vs a small new one the dashboard can query pre-auth-org.
- Whether SaaS should auto-create the templates via the WABA API on boot
  (nice, but Meta approval is async anyway) — v1 leans manual + documented.
- Graph API version pinning policy (config default, e.g. `v23.0`).

### Out of scope (v1)

- BYO-WABA org-level connections (the settings shape doesn't preclude it).
- Rich/interactive messages, buttons, ack-via-reply commands.
- Free-form follow-ups inside the 24h session window.
- Embedded signup / Meta Tech Provider onboarding.
- Status-page subscriber notifications over WhatsApp.
