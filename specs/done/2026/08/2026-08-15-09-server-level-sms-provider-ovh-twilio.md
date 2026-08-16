---
model: opus
effort: high
---

# SMS sending is hard-wired to Twilio and configured per-org, so customers must bring their own account and no cheaper provider is possible

## Problem

**Two defects, same feature area.**

**1. The provider is hard-wired.** `*models.TwilioSettings` is threaded as a
concrete type all the way down the send path: `sendPhoneSMS`
(`server/internal/jobs/jobtypes/job_escalation_step.go:859`), `placePhoneCall`
(`:894`), the client constructor seam `var newTwilioClient =
twilio.NewClientWithBaseURL` (`:35`), the user-notification / verification send
(`server/internal/handlers/usernotifications/service.go:548`), the signed status
callback (`server/internal/handlers/twiliocb/handler.go:76`), credential
validation (`server/internal/handlers/integrations/service.go:510`), the sender
registry (`server/internal/notifications/registry.go:45`) and the resolver
(`server/internal/integrations/twilioconn/resolve.go:77`). There is no seam at
which a second provider could be introduced.

This costs money. Twilio bills **$0.0798 per SMS to France** (its own pricing
page); OVHcloud bills **0,054–0,060 € HT** for the same message — roughly half.
For an alerting product whose SMS are transactional and low-volume per org but
numerous in aggregate, that is a direct margin line.

**2. Per-org configuration is the *only* option, and it should not be
required.** SMS is the *last*
messaging channel still configured as a per-org `Integration`
(`ConnectionTypeTwilio`, `server/internal/db/models/integration.go:28`), with
per-org encrypted credentials (`server/internal/crypto/credentials/conn_secrets.go:43`).
Every other channel is already instance-level — see `WhatsAppConfig`
(`server/internal/config/config.go:752`), whose doc comment states the intent
exactly: *"one deployment-wide identity, no per-org bring-your-own WABA in v1.
SaaS supplies `SP_WHATSAPP_*` in its deployment env; a self-hoster does exactly
the same with their own Meta app and WABA — no code path differs between the
two."* Telegram follows the same shape.

Requiring a customer to open a Twilio account, buy a number and paste
credentials before they can receive a single SMS alert is an onboarding cliff no
competitor imposes.

It also leaves the entitlements model incoherent: `ReserveSMS`
(`server/internal/entitlements/usage_reserve.go:37`), `MaxSmsPerMonth` and the
hourly runaway guard meter SMS volume — a quota over spending that, today,
belongs to the customer's own Twilio account. Those limits only become
meaningful once the instance pays for the messages.

## Proposal

**Decision (2026-08-16): two modes, customer's choice.** An org either uses
the **server's credentials** (instance-level config, the default — zero setup)
or **brings its own** provider account through a per-org custom integration,
which overrides the instance provider for that org (see §7). What this spec
removes is the *requirement* to bring your own account, not the ability. The
UI must explain this choice explicitly (see §8).

### 1. Instance-level SMS configuration, mirroring `WhatsAppConfig`

Add `SMSConfig` to `config.go` alongside `WhatsApp` / `Telegram`, with the same
contract: a kill switch, secrets from env/SSM only, and an `Active()` method
that is *the* single enablement rule applied identically by the escalation
dispatcher, the verification flow and the public config endpoint.

```
SP_SMS_ENABLED            bool    kill switch
SP_SMS_PROVIDER           string  "twilio" | "ovh"
SP_SMS_SENDER             string  alphanumeric sender / E.164, provider-dependent

SP_SMS_TWILIO_ACCOUNT_SID       SP_SMS_TWILIO_AUTH_TOKEN (SECRET)
SP_SMS_TWILIO_REGION            SP_SMS_TWILIO_MESSAGING_SERVICE_SID
SP_SMS_TWILIO_BASE_URL          (test/E2E override, like SP_WHATSAPP_BASE_URL)

SP_SMS_OVH_ENDPOINT             ovh-eu | ovh-ca | ovh-us
SP_SMS_OVH_APPLICATION_KEY      SP_SMS_OVH_APPLICATION_SECRET (SECRET)
SP_SMS_OVH_CONSUMER_KEY (SECRET)
SP_SMS_OVH_SERVICE_NAME         the sms-xxxxx-1 account
SP_SMS_OVH_BASE_URL             (test/E2E override)
```

Note the koanf multi-word-key quirk: `SP_SMS_*` keys with underscores need the
manual env reader in `internal/config/envvars.go`, as with `rate_limiting`.

Voice stays Twilio-only and gets its own independent instance-level block
(`SP_VOICE_*`) — see §3.

### 2. A `sms.Sender` interface with two implementations

New package `server/internal/integrations/sms`:

```go
type Sender interface {
    SendSMS(ctx context.Context, p *SendParams) (*SendResult, error)
}

type SendParams struct {
    To             string // E.164, validated by the existing twilio.ValidE164
    Body           string
    StatusCallback string // empty = no DLR requested
}

type SendResult struct {
    ProviderMessageID string // Twilio SID / OVH job id
}
```

`From` deliberately does **not** appear in `SendParams`: it is not the same
concept across providers (Twilio wants an E.164 number or a
`MessagingServiceSID`; OVH wants a pre-registered alphanumeric sender or a short
number). The sender identity is baked into each provider's constructor from
instance config.

`twilio` keeps its current client; add a thin adapter implementing `Sender`.
The adapter must be constructible from *either* credential source: instance
config (`SMSConfig`) or a per-org integration's decrypted credentials (the
bring-your-own path, §7) — both modes share the same send path. Build the
instance-level `Sender` once at startup from `SMSConfig` and inject it; the
resolver (§3) hands back either that shared instance `Sender` or a per-org
Twilio-backed one, replacing the `newTwilioClient` package-level seam with an
injected interface (tests then use a fake `Sender` rather than swapping a
package var).

### 3. Capabilities become per-channel, resolved independently

**Decision taken: SMS provider and voice provider are independent.** OVH has no
voice API, so binding them would mean losing escalation calls in exchange for
cheaper SMS.

Extend `Capabilities` (`server/internal/db/models/integration.go:45`) with
`CanSendSMS` / `CanPlaceCall` and resolve each capability separately. This
follows the philosophy already stated in that file — *"capability is now data,
not a special case"*. An instance can therefore run OVH for SMS and Twilio for
voice simultaneously, which is the configuration we expect to ship.

Resolution order, per org and per capability: a per-org custom integration
providing the capability wins; otherwise the instance-level config applies.
`job_escalation_step.go:306`'s `conn.Type == ConnectionTypeTwilio` branch
becomes that two-step resolution (org integration first, then instance
config) instead of today's per-org-only lookup.

### 4. OVH client package

New `server/internal/integrations/ovhsms`, hand-rolled like `twilio/client.go`
(no SDK — consistent with the rest of the repo).

Auth is the OVH signed-request scheme, four headers:

```
X-Ovh-Application  application key
X-Ovh-Consumer     consumer key
X-Ovh-Timestamp    unix seconds
X-Ovh-Signature    "$1$" + SHA1(secret + "+" + CK + "+" + METHOD + "+" + URL + "+" + body + "+" + timestamp)
```

**Gotcha to implement, not discover in production:** the signature uses OVH's
*server* time. The official SDKs fetch `/auth/time` once and keep a delta. A
laptop or pod with a few seconds of clock skew gets a hard auth failure that
looks like a bad credential. Fetch the delta at client construction and cache it.

Send: `POST /sms/{serviceName}/jobs` with `message`, `receivers` (array),
`sender`, `priority`, and **`noStopClause: true`** — mandatory for
transactional traffic; without it OVH appends a "STOP" clause that both consumes
characters (possibly a second segment, i.e. double cost) and is legally reserved
for marketing.

Also provide `VerifyCredentials` mirroring `twilio.VerifyCredentials`, and
endpoint validation mirroring `twilio.ValidRegion`, so the operator gets a clear
startup error rather than a runtime failure on the first incident.

The exact field name for the per-job DLR callback must be confirmed against
`https://eu.api.ovh.com/console/?section=/sms&branch=v1` during implementation —
the docs disagree between `callBack`, `callback` and a service-level setting.
Do not guess.

### 5. Delivery receipts for OVH

**Decision taken: implement now, not deferred.**

OVH does **not** sign its callbacks — unlike Twilio, whose signature is verified
at `twiliocb/handler.go:91`. So the endpoint must authenticate by construction:

- New route `/api/v1/integrations/ovhsms/dlr/{token}`, where `{token}` is a
  high-entropy value generated per message (or per instance, rotated) and stored
  alongside the outbound record.
- Constant-time comparison; unknown/expired token → 404, never 401 with detail.
- Map OVH job status to the existing internal delivery-status model, so the
  timeline renders identically whichever provider sent the message.
- The handler must be strictly idempotent: OVH may retry.

Treat callback payload fields as untrusted input — they are attacker-influenceable
given an unsigned endpoint.

### 6. Cost guards — the part that carries real money

Today's runaway guard (`defaultSMSRunawayPerHour = 30`,
`entitlements/usage_reserve.go:17`) was a safety belt while the *customer* paid.
Once the instance pays, it is the only thing between a bug and the bill, and it
has two holes:

- **It is per-org only.** 200 orgs × 30/h is 6 000 SMS/h of instance spend with
  every individual limit respected. Add an **instance-wide** hourly cap
  (`SP_SMS_GLOBAL_RUNAWAY_PER_HOUR`), checked before the per-org reservation.
  This cap guards *instance* spend, so it applies only to sends going through
  the server's credentials — bring-your-own sends (§7) bill the customer's own
  account and must not consume it (the per-org runaway guard still applies to
  them, as today's safety belt).
- **A count quota under-prices expensive destinations.** France is ~0,05 €;
  some destinations exceed 0,30 € — a 6× spread on an identical quota. Add a
  destination-country allow-list (`SP_SMS_ALLOWED_COUNTRIES`, empty = all) so an
  unexpected country prefix cannot quietly become the most expensive line on the
  bill. Like the global cap, the allow-list gates server-credential sends only;
  an org sending through its own integration chooses its own destinations on
  its own bill.

Both breaches must log loudly and surface on the org Usage page rather than
failing silently.

### 7. Per-org integrations stay supported — bring-your-own as an override

The per-org Twilio integration is **kept, not removed**: it is the
bring-your-own mode for orgs that want their own account, sender identity, or
billing relationship.

- Keep `twilio` in the creatable-type map
  (`handlers/integrations/service.go:475`). Existing `ConnectionTypeTwilio`
  rows keep working unchanged — no migration, no disable, no removal banner.
  No org is ever left resolving to nothing.
- Resolution (§3): an org with its own integration sends through it — SMS
  *and* voice, since one Twilio account covers both capabilities. An org
  without one falls back to the instance-level provider(s).
- Bring-your-own sends bill the customer's own account: the instance-spend
  guards in §6 (global cap, country allow-list) do not apply to them; the
  per-org runaway guard does.
- Bring-your-own is **Twilio-only** for now. A per-org OVH integration is out
  of scope — OVH is offered as an instance-level provider only.

### 8. UI and docs

**The UI must explain the two modes** — this is part of the feature, not a
nicety. On the org's integrations surface, show an SMS panel stating the
org's *effective* mode:

- **Server-provided (default):** "SMS is provided by this instance" — whether
  it is active and the sender (never the credentials). Next to it, explain
  that the org *may* add its own Twilio integration to send with its own
  account, sender and billing instead.
- **Bring-your-own:** when a per-org Twilio integration exists, state
  explicitly that it overrides the instance provider for this org's SMS and
  voice, and that deleting it falls the org back to the server-provided mode
  (or to "SMS unavailable" if the instance has no SMS configured).

Other UI/docs work:

- Keep the per-org Twilio integration form (it is the bring-your-own path).
- Follow the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) — no raw Radix.
- Update `server/internal/app/docsres/docs/configuration/twilio.md` into a
  provider-neutral `sms.md` documenting both providers' env vars **and the
  two modes** (server-provided default, per-org bring-your-own override); the
  docs site and the OpenAPI reference are built from these.
- Document the March 2026 AF2M charter constraint on the sender: Latin
  alphanumeric only, no special characters, must not resemble a phone number,
  and must correspond to a registered brand.

### 9. Tests

- Table-driven tests for the OVH signature against known-good vectors,
  **including a clock-skew case** proving the `/auth/time` delta is applied.
- A test proving `noStopClause` is always sent true — a negative control, since
  regressing it costs money silently rather than failing.
- Fake OVH and fake Twilio HTTP servers behind `SP_SMS_*_BASE_URL`, so the whole
  feature is exercised in test mode with no code path differing from production
  (the `SP_WHATSAPP_BASE_URL` precedent).
- DLR endpoint: valid token, wrong token, replayed callback (idempotency),
  malformed payload.
- Guard tests: per-org cap, instance-wide cap, disallowed country — each must
  prove the send is *refused*, not merely counted.
- Escalation tests proving OVH-for-SMS + Twilio-for-voice resolve independently.
- Resolution-order tests: an org **with** its own Twilio integration sends
  through it (never the instance credentials — assert the instance fake
  receives nothing, a negative control); an org **without** one uses the
  instance provider; deleting the org integration falls back to the instance
  provider on the next send.
- Guard-scoping test: a bring-your-own send must not consume the instance-wide
  cap or be blocked by the country allow-list.

### Out of scope / open questions

- **Per-org sender ID on the server-provided path.** The shared instance
  credentials send under one sender for every org. Orgs that need their own
  branding use the bring-your-own integration (§7); no per-org sender
  configuration exists on the server-provided path.
- **Inbound SMS / ack-by-reply.** Twilio's callback handler also carries ack.
  OVH's `senderForResponse` uses a different mechanism (short number). Not
  addressed here; ack via SMS reply stays Twilio-only for now.
- **Self-hosted defaults.** `entitlements/defaults.go:82` documents "unlimited
  (self-hosted, bring-your-own Twilio)". The wording needs updating, but the
  behaviour is unchanged: unlimited for self-hosted, since the operator's own
  credentials pay.
- Provider failover (OVH primary, Twilio fallback on error) is deliberately not
  in this spec — it needs a retry/dedup design of its own.

## Resolved open questions

Every item in the section above is already settled by the spec. Restated here
as directives so there is nothing left to decide at implementation time:

> **Per-org sender ID.** Instance-level config means every org sends under one
> sender.

**Decision (updated 2026-08-16):** The server-provided path ships one
instance-wide sender with no per-org customization. Per-org branding *is*
available, but only through the bring-your-own integration (§7) — do not add
any per-org sender setting to the server-provided path.

> **Inbound SMS / ack-by-reply.**

**Decision:** Out of scope. Ack-by-SMS-reply stays Twilio-only via the existing
`twiliocb` path; do not build an OVH inbound/`senderForResponse` equivalent.

> **Self-hosted defaults.** `entitlements/defaults.go:82` documents "unlimited
> (self-hosted, bring-your-own Twilio)".

**Decision:** In scope, but **wording only** — update that comment to drop the
"bring-your-own Twilio" framing. The behaviour is unchanged: self-hosted stays
unlimited, since the operator's own credentials pay. Do not alter any limit
value in `defaults.go`.

> Provider failover (OVH primary, Twilio fallback on error)

**Decision:** Out of scope. A send failure surfaces as a failure; do not add
cross-provider retry or fallback.

Also note the one deliberately-unresolved *research* item in §4: the OVH
per-job DLR callback field name (`callBack` / `callback` / service-level
setting) must be confirmed against the live OVH API console
(`https://eu.api.ovh.com/console/?section=/sms&branch=v1`) during
implementation. This is a lookup for the implementer, not a question for the
user — but per the spec, **do not guess it**.

## Implementation Plan

Ordered so each step lands as its own commit and a later failure never strands
the earlier ones.

**Research item resolved up front (§4).** The OVH API schema
(`https://eu.api.ovh.com/1.0/sms.json`, fetched during implementation) shows
`POST /sms/{serviceName}/jobs` accepts exactly: `charset`, `class`, `coding`,
`differedPeriod`, `message`, `noStopClause`, `priority`, `receivers`,
`receiversDocumentUrl`, `receiversSlotId`, `sender`, `senderForResponse`,
`tag`, `validityPeriod`. **There is no per-job callback field.** The DLR
callback is a *service-level* setting: `callBack` on the `sms.Account` model
(`PUT /sms/{serviceName}`), documented as "URL called when state of a sent SMS
changes". So §5's "per instance, rotated" token is not a fallback — it is the
only shape the OVH API supports. OVH appends `id`, `ptt`, `date`,
`description`, `descriptionDlr` to the callback query string; `ptt` 4 =
delivered, 3 = accepted, 1/2 = intermediate retry, everything else = failure
(OVH docs `tout_savoir_sur_les_utilisateurs_sms`).

1. **Config (§1).** `SMSConfig` + `VoiceConfig` in `config.go`, mirroring
   `WhatsAppConfig` (kill switch, secrets env/SSM only, one `Active()` rule).
   Manual env readers in `applySMSEnv` / `applyVoiceEnv` + `SMSEnvVarNames` /
   `VoiceEnvVarNames` wired into `envvars.go` (koanf multi-word quirk).
2. **OVH client (§4).** `internal/integrations/ovhsms`: signed-request scheme,
   `/auth/time` delta cached at construction, `SendSMS` with
   `noStopClause: true` always, `VerifyCredentials`, `ValidEndpoint`.
3. **`sms.Sender` seam (§2).** `internal/integrations/sms`: `Sender`,
   `SendParams`, `SendResult`, a Twilio adapter constructible from *either*
   instance config or per-org decrypted settings, an OVH adapter, and
   `NewInstanceSender` built once at startup.
4. **Capabilities + resolution (§3, §7).** `CanSendSMS` / `CanPlaceCall` on
   `models.Capabilities`; `sms.Resolver` returning a `Resolution{Mode, Sender,
   Voice, Conn}` — per-org Twilio integration first, instance config second.
5. **Guards (§6).** Instance-wide hourly cap and destination-country
   allow-list on the entitlements service, applied **only** on the
   server-credential path; loud logging plus a per-org breach counter surfaced
   on the Usage API.
6. **Send-path rewiring.** `job_escalation_step.go` (`pagePhone`,
   `sendPhoneSMS`, `placePhoneCall`), `usernotifications` verify + test SMS,
   and `twiliocb` (instance-credential voice callbacks) move onto the seam;
   the `newTwilioClient` package vars go away.
7. **OVH DLR (§5).** `/api/v1/integrations/ovhsms/dlr/{token}`, constant-time
   token compare, 404 on mismatch, idempotent, untrusted payload.
8. **Docs (§8).** `configuration/twilio.md` → provider-neutral `sms.md`
   (both providers, both modes, AF2M charter, opt-out content preserved).
9. **UI (§8).** SMS mode panel on the org integrations surface explaining the
   effective mode and what overrides what, from the design reference.
10. **Tests (§9)** land alongside each step; the negative controls
    (`noStopClause`, disallowed country refused, unknown DLR token 404, BYO
    never touches instance credentials or the global cap) are acceptance
    criteria.

Also in scope, wording only: `entitlements/defaults.go` drops the
"bring-your-own Twilio" framing (no limit value changes).
