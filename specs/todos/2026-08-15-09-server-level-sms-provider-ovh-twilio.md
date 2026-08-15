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

**2. Configuration is per-org, and it should not be.** SMS is the *last*
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
Build the concrete `Sender` once at startup from `SMSConfig` and inject it,
replacing the `newTwilioClient` package-level seam with an injected interface
(tests then use a fake `Sender` rather than swapping a package var).

### 3. Capabilities become per-channel, resolved independently

**Decision taken: SMS provider and voice provider are independent.** OVH has no
voice API, so binding them would mean losing escalation calls in exchange for
cheaper SMS.

Extend `Capabilities` (`server/internal/db/models/integration.go:45`) with
`CanSendSMS` / `CanPlaceCall` and resolve each capability separately. This
follows the philosophy already stated in that file — *"capability is now data,
not a special case"*. An instance can therefore run OVH for SMS and Twilio for
voice simultaneously, which is the configuration we expect to ship.

`job_escalation_step.go:306`'s `conn.Type == ConnectionTypeTwilio` branch becomes
a capability check against instance config, not a per-org connection lookup.

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
- **A count quota under-prices expensive destinations.** France is ~0,05 €;
  some destinations exceed 0,30 € — a 6× spread on an identical quota. Add a
  destination-country allow-list (`SP_SMS_ALLOWED_COUNTRIES`, empty = all) so an
  unexpected country prefix cannot quietly become the most expensive line on the
  bill.

Both breaches must log loudly and surface on the org Usage page rather than
failing silently.

### 7. Migration of existing per-org Twilio connections

Existing `ConnectionTypeTwilio` integrations exist in live databases. They must
not be left resolving to nothing — that breaks escalations silently, which is
the worst possible failure for an alerting product.

- Stop creating them: remove `twilio` from the creatable-type map
  (`handlers/integrations/service.go:475`).
- Existing rows: mark disabled, surface an explicit banner on the integration
  explaining SMS is now instance-level, and keep the row readable (never delete
  customer data as part of a migration).
- Log a startup warning listing orgs that still have one, so the operator can
  confirm the instance-level credentials are in place before the old path stops
  being used.

### 8. UI and docs

- Remove the per-org Twilio integration form; replace with a read-only "SMS is
  provided by this instance" state showing whether it is active and the sender.
- Follow the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) — no raw Radix.
- Update `server/internal/app/docsres/docs/configuration/twilio.md` into a
  provider-neutral `sms.md` documenting both providers' env vars; the docs site
  and the OpenAPI reference are built from these.
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

### Out of scope / open questions

- **Per-org sender ID.** Instance-level config means every org sends under one
  sender. Normal for SaaS, but it forecloses per-org branding without
  reintroducing per-org configuration. Assumed acceptable; flag if not.
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

**Decision:** Accepted. Ship one instance-wide sender; do **not** reintroduce
any per-org sender configuration.

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
