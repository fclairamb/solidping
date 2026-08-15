---
sidebar_position: 9
title: SMS & Voice
---

# SMS & Voice Alerts

SolidPing pages on-call engineers over **SMS** and **voice calls**. These are
the two channels that reliably wake someone at 3 a.m., and a voice call can be
**acknowledged from the phone keypad** without unlocking the screen.

## The two modes

An organization gets SMS in one of two ways, and it does **not** have to choose
— the first is the default and needs no setup at all.

| Mode | Who configures it | Who pays | Sender |
|---|---|---|---|
| **Server-provided** (default) | The operator, once, with `SP_SMS_*` environment variables | The instance | One instance-wide sender |
| **Bring your own** (override) | The organization, as a Twilio integration | The organization, directly to Twilio | The organization's own number |

Resolution is **per organization and per capability**:

- An organization with its own Twilio integration sends through it — SMS *and*
  voice, since one Twilio account covers both. It never touches the instance
  credentials.
- An organization without one falls back to the instance-level provider.
- Deleting the organization's integration drops it back to the server-provided
  mode on the next send — or to "SMS unavailable" if the instance has no SMS
  configured. **No organization is ever left resolving to nothing.**

SMS and voice are resolved **independently**, because the providers differ:
OVHcloud sells SMS but has no voice API. An instance can therefore run OVH for
SMS and Twilio for voice at the same time, which is the configuration we expect
most deployments to want.

The organization's **effective** mode is shown on its **Integrations** page.

## How an alert reaches a phone

There are two independent ways an alert reaches a phone:

- **Per-user phone contacts.** A user adds their own number under
  **Account → Notifications** and completes a 6-digit code round-trip. Severity
  channel tokens `sms` and `voice` then route escalation steps to that verified
  number. This is the paging path.
- **A shared destination channel.** The connection's `to_numbers` list receives
  the same broadcast notifications as any other channel (per-check
  subscriptions, escalation `connection` targets, the **Test** button). Useful
  for a team phone or a NOC handset, and it needs no per-user verification.

Delivery status flows back in both modes, so the notification history shows the
provider's own delivered / pending / failed state — see
[Delivery receipts](#delivery-receipts).

## Server-provided mode

### Choosing a provider

| | Twilio | OVHcloud |
|---|---|---|
| SMS | yes | yes |
| Voice | yes | **no** |
| Indicative price to France | ~$0.0798 / SMS | ~0,054–0,060 € HT / SMS |
| Delivery receipts | per-message callback | one service-level callback |
| Acknowledge by SMS reply | yes | no |

OVH is roughly half the price for European traffic; Twilio is the only option
for voice and for ack-by-SMS-reply. Running both is normal.

### Common settings

| Variable | Default | Notes |
|---|---|---|
| `SP_SMS_ENABLED` | `false` | Kill switch. It only ever turns the feature **off** — credentials are still required to turn it on. |
| `SP_SMS_PROVIDER` | `twilio` | `twilio` or `ovh`. |
| `SP_SMS_SENDER` | — | The instance-wide sender: an E.164 number for Twilio, a registered alphanumeric sender or short number for OVH. |
| `SP_SMS_GLOBAL_RUNAWAY_PER_HOUR` | `500` | Instance-wide hourly cap — see [Spend protection](#spend-protection). Negative disables it. |
| `SP_SMS_ALLOWED_COUNTRIES` | *(empty = all)* | Comma-separated E.164 country calling codes, e.g. `33,1,44`. |

There is deliberately **no per-organization sender** on this path: the shared
credentials send under one sender for every organization. An organization that
needs its own branding uses the bring-your-own integration instead.

### Twilio as the instance provider

| Variable | Notes |
|---|---|
| `SP_SMS_TWILIO_ACCOUNT_SID` | The `AC…` account identifier. |
| `SP_SMS_TWILIO_AUTH_TOKEN` | **Secret.** Environment/SSM only — never logged, never returned by any API. |
| `SP_SMS_TWILIO_REGION` | `us1` (default), `ie1`, `au1`… See [Region](#region). |
| `SP_SMS_TWILIO_MESSAGING_SERVICE_SID` | Send through a Messaging Service (`MG…`) instead of `SP_SMS_SENDER`. |
| `SP_SMS_TWILIO_BASE_URL` | Overrides the API base. For an egress proxy, and for pointing tests at a fake API with no code path differing. |

### OVHcloud as the instance provider

Create an application at [eu.api.ovh.com/createToken](https://eu.api.ovh.com/createToken)
with `GET`/`POST`/`PUT` rights on `/sms/*`.

| Variable | Notes |
|---|---|
| `SP_SMS_OVH_ENDPOINT` | `ovh-eu` (default), `ovh-ca` or `ovh-us`. |
| `SP_SMS_OVH_APPLICATION_KEY` | The application key. |
| `SP_SMS_OVH_APPLICATION_SECRET` | **Secret.** |
| `SP_SMS_OVH_CONSUMER_KEY` | **Secret.** |
| `SP_SMS_OVH_SERVICE_NAME` | The SMS account, `sms-xxxxx-1`. |
| `SP_SMS_OVH_BASE_URL` | Overrides the endpoint-derived API base (proxy / tests). |
| `SP_SMS_OVH_DLR_TOKEN` | The delivery-receipt path secret. Optional — one is derived from the JWT secret when unset. |

Two OVH specifics worth knowing before you debug the wrong thing:

- **Clock skew is an auth failure.** OVH signs requests against *its own*
  clock. SolidPing fetches `/auth/time` once at startup and keeps the delta, so
  a pod a few seconds off still authenticates. If you see hard `403`s with
  credentials you are sure of, check that `/auth/time` is reachable.
- **`noStopClause` is always sent.** Without it OVH appends a "STOP" clause
  that consumes characters — possibly pushing the message into a second billed
  segment — and is legally reserved for marketing, which transactional alerting
  is not.

### Voice (Twilio only)

Voice has its own independent block, so turning off SMS never silences the
calls:

| Variable | Notes |
|---|---|
| `SP_VOICE_ENABLED` | Kill switch. |
| `SP_VOICE_FROM_NUMBER` | E.164 caller ID. |
| `SP_VOICE_TWILIO_ACCOUNT_SID` / `SP_VOICE_TWILIO_AUTH_TOKEN` | Credentials. The token is a **secret**. |
| `SP_VOICE_TWILIO_REGION` | See [Region](#region). |
| `SP_VOICE_TWILIO_BASE_URL` | Proxy / test override. |

### Sender ID rules

A sender is not free text. Since the **AF2M charter of March 2026**, an
alphanumeric sender ID used for French traffic must:

- use the **Latin alphabet and digits only** — no accents, punctuation,
  emoji or other special characters;
- **not resemble a phone number** (no all-digit or number-like sender);
- **correspond to a registered brand** — the sender must identify the actual
  sending organization, and operators reject senders that impersonate one.

Other countries have their own rules (some forbid alphanumeric senders
outright, some require pre-registration). Your provider's country guidelines
are the authority.

### Delivery receipts

- **Twilio** receives a per-message `StatusCallback`, signed and validated with
  the account auth token.
- **OVH** has **no per-job callback field** — the callback URL is a
  service-level setting on the SMS account, and OVH does not sign it. SolidPing
  therefore registers
  `/api/v1/integrations/ovhsms/dlr/<token>` at startup and authenticates the
  endpoint **by construction**: the token is compared in constant time, an
  unknown token gets a bare `404`, and every payload field is treated as
  untrusted input. The handler is idempotent, because OVH retries.

  If startup registration fails (read-only credentials, for instance), set the
  same URL as the account's callback in the OVH control panel.

Either way, the notification timeline renders the same delivered / pending /
failed states.

## Bring-your-own mode (per-organization Twilio)

An organization that wants its own account, sender identity or billing
relationship adds a **Twilio integration** — this is unchanged and fully
supported. It overrides the instance provider for that organization's SMS *and*
voice.

Bring-your-own is **Twilio-only**: OVH is offered as an instance-level provider
only.

### Prerequisites

On Twilio's side you need:

1. A **Twilio account** (trial works for testing — see [Trial
   accounts](#trial-accounts) for the caveats).
2. A **phone number** with the capabilities you want: **SMS** for text alerts,
   **Voice** for calls. One number can do both.
3. Your **Account SID** and **Auth Token**.

On SolidPing's side:

4. `SP_BASE_URL` set to a URL **Twilio can reach from the public internet**.
   Voice calls and delivery statuses are callbacks — Twilio fetches them from
   your server. It defaults to `http://localhost:4000`, which Twilio cannot
   reach: the call is placed and then fails on the unreachable TwiML, and
   delivery statuses never arrive. SMS sending itself still works.

:::caution Use the Auth Token, not an API Key
SolidPing authenticates with the **Account SID (`AC…`) + primary Auth Token**.
A Twilio **API Key / Secret** (`SK…`) is rejected at validation, and it could
not work anyway: inbound callbacks are authenticated by validating
`X-Twilio-Signature`, which Twilio computes with the **account auth token**.
:::

### Step 1 — Get a phone number

In the [Twilio Console](https://console.twilio.com): **Phone Numbers → Manage →
Buy a number**.

- Filter on the capabilities you need — tick **SMS** and **Voice**.
- Pick a number in (or close to) the country you are paging into. Domestic
  routes are cheaper, deliver better, and avoid a lot of filtering.
- Some countries require an approved **regulatory bundle** (address / identity
  documents) before the number can be issued or used. Twilio prompts for it
  during purchase; approval is asynchronous.

You do **not** need to configure anything else on the number — see
[Step 4](#step-4--no-webhook-configuration-needed).

:::tip One number or two
A single number can send SMS *and* place calls. Configure it as both
`from_number` and `voice_from_number`. Use two numbers only if you want alerts
to come from a distinct caller ID, or if your SMS sender is a Messaging Service
and your voice number is separate.
:::

#### Alternative: a Messaging Service

Instead of a from-number you can send SMS through a **Messaging Service**
(**Messaging → Services**), which adds sender pools, sticky sender and
country-based routing. Configure its SID (`MG…`) as `messaging_service_sid` and
leave `from_number` empty — SolidPing accepts exactly one of the two.

A Messaging Service covers SMS only. Voice calls always need
`voice_from_number`.

#### Regional and regulatory setup

Two settings silently drop messages if they are wrong. Check both before
concluding SolidPing is broken:

- **Geo permissions.** Twilio blocks destinations that are not explicitly
  enabled: **Messaging → Settings → Geo permissions** and **Voice → Settings →
  Geo permissions**. Enable every country your on-call engineers are in.
- **Sender registration.** US long codes require **A2P 10DLC** brand and
  campaign registration (**Messaging → Regulatory Compliance**) — unregistered
  traffic is heavily filtered. Other countries have their own rules
  (alphanumeric sender IDs, pre-registration); Twilio's country guidelines are
  the authority.

### Step 2 — Copy the credentials

The **Account SID** and **Auth Token** are on the Twilio Console home page.

Consider creating a **subaccount** (**Account → Subaccounts**) dedicated to
SolidPing: it has its own SID/token pair, so rotating or revoking it does not
affect anything else you run on Twilio, and usage shows up on its own line in
the bill.

### Step 3 — Create the connection in SolidPing

**Integrations → New integration → Twilio**, then fill in:

| Field | Required | Notes |
|---|---|---|
| `account_sid` | yes | Must start with `AC` and be 34 characters. |
| `auth_token` | yes | **Secret** — encrypted at rest when `SP_ENCRYPTION_MASTER_KEY` is set (see [Security](./security.md)). Never returned by the API. |
| `region` | no | Which Twilio regional edition to talk to. Empty = US1 (default). See [Region](#region) below. |
| `from_number` | one of | SMS sender, E.164 (`+15551234567`). |
| `messaging_service_sid` | one of | SMS sender as a Messaging Service (`MG…`). Exactly one of these two. |
| `voice_from_number` | no | Caller ID for voice calls, E.164. **Voice is disabled when empty.** |
| `to_numbers` | no | Shared recipients for broadcast sends, E.164. Leave empty if you only page per-user contacts. |

Validation rejects a malformed SID and any number that is not
[E.164](https://en.wikipedia.org/wiki/E.164) (`^\+[1-9]\d{6,14}$`) — a leading
`+`, country code, no spaces or dashes. This is deliberate: a bad number would
otherwise fail silently at 3 a.m. rather than at configuration time.

Mark the connection **default** if the organization has more than one Twilio
connection — the phone-verification flow and escalation paging always use the
default (or the only enabled one).

On every **Create** and on any **Update** that changes the account SID, auth
token, or region, SolidPing makes one live call to Twilio
(`GET /2010-04-01/Accounts/{AccountSid}.json`) to confirm the credentials
actually work before saving. A save is refused — nothing is persisted — if
Twilio rejects the credentials, or if Twilio could not be reached to check
them. This is deliberate: it turns a bad paste into an error on the
configuration form, not a silent failure the next time an incident fires. A
save that only changes something else (the name, `from_number`,
`voice_from_number`, `to_numbers`) never makes this call.

#### Region

Twilio's [regional editions](https://www.twilio.com/docs/global-infrastructure)
give each region its own API host and its own account, credentials, and data
residency. `region` picks which host SolidPing talks to for this connection —
it does not change anything on Twilio's side.

- **Empty (default) or `us1`** — the global/US1 edge, `api.twilio.com`. This
  is what every connection used before this field existed, and stays the
  behavior if you never set it.
- **`ie1`** — Ireland, for EU data residency.
- **`au1`** — Australia.
- Any other well-formed token (two lowercase letters + digits, matching what
  Twilio itself uses in the console) is accepted too — SolidPing does not
  hard-code the list of regions Twilio supports.

Two things that trip people up:

- **Credentials are per-region, not portable.** An `ie1` account has its own
  Account SID and Auth Token — a `us1` token pasted into an `ie1` connection
  (or vice versa) will not work, and the live credential check above will
  refuse to save it.
- **The region is chosen when the Twilio account is created, not switched
  later.** If you picked the wrong region in Twilio's console, the fix is a
  new Twilio account (or subaccount) in the right region, not changing
  `region` on the existing SolidPing connection — the SID/token you already
  have are scoped to the old region regardless of what `region` says here.

Use **Test** to send to `to_numbers` and confirm credentials end to end.

### Step 4 — No webhook configuration needed

**Do not configure webhooks on the number in the Twilio Console.** SolidPing
passes every callback URL per request, signed and scoped to one incident:

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/integrations/twilio/voice` | Returns the TwiML spoken during an alert call. |
| `POST /api/v1/integrations/twilio/voice/gather` | Receives the DTMF digit that acknowledges the incident. |
| `POST /api/v1/integrations/twilio/status` | Delivery status for messages and calls. |

Each request is authenticated by validating `X-Twilio-Signature` against the
connection's auth token; anything that fails returns `403` with no detail.

Two consequences worth knowing:

- **`SP_BASE_URL` must be publicly reachable.** On its `http://localhost:4000`
  default, Twilio cannot fetch the TwiML, so alert calls connect and then fail,
  and delivery statuses never arrive.
- **Rotating the Twilio auth token breaks callbacks** until the connection is
  updated — Twilio signs with the new token while SolidPing still validates
  with the old one. Update the connection first, then rotate.

The number's own "A call comes in" / "A message comes in" webhooks are
irrelevant: SolidPing never handles inbound calls or texts on it.

## Verify a phone number

Each user adds their own number under **Account → Notifications → add contact →
Phone (SMS)**, then requests a code:

- A 6-digit code arrives by SMS: *"[SolidPing] Your verification code is 123456
  (valid 10 minutes). Msg&data rates may apply. Reply STOP to unsubscribe, HELP
  for help."*
- The code is valid **10 minutes**, allows **5 attempts**, and can be re-sent at
  most **3 times per hour** per contact.
- **An unverified phone contact is never paged.** This is what keeps a typo
  from sending alerts to a stranger.

The code rides whichever provider the organization resolves to. If neither mode
is available — no organization integration *and* no instance-level SMS —
requesting a code returns a validation error naming the missing provider.

## Route severities to phone

Add the channel tokens to the severities that should page:

| Token | Effect |
|---|---|
| `sms` | Sends an SMS to every verified phone contact on the target. |
| `voice` | Additionally places a voice call — requires a caller ID (`SP_VOICE_FROM_NUMBER`, or `voice_from_number` on the organization's own integration). |

A severity with **no** channel filter keeps meaning "every channel", as before.
A severity listing only `email`/`slack` is unaffected by this feature.

### What an alert looks like

An escalation SMS is deliberately short:

```text
[SolidPing] acme: Payments API needs attention (escalated). Ack: https://…/ack?token=… Reply STOP to unsubscribe.
```

The `Ack:` link is a signed one-click acknowledgement — no login needed.

Every alert body carries the `Reply STOP to unsubscribe.` footer. US A2P 10DLC
registration declares that recurring traffic carries opt-out language, so the
footer is not optional decoration — see [Opt-out handling](#opt-out-handling).

A voice call reads the organization and check name aloud, then:

> *Press 4 to acknowledge.*

Pressing **4** acknowledges the incident from the phone. Anything else re-prompts
once, then hangs up. Acknowledging — by keypad, by link, or in the dashboard —
cancels every pending escalation step, so no further SMS or call goes out.

## Opt-out handling

Every alert body carries the terse `Reply STOP to unsubscribe.` footer, and the
first message a number ever receives — the verification code — carries the
fuller `Msg&data rates may apply. Reply STOP to unsubscribe, HELP for help.`
disclosure. US A2P 10DLC registration declares that recurring traffic carries
opt-out language, so these are compliance text, not decoration.

`STOP` itself is handled at the platform level:

- **Twilio** maintains the opt-out list through its Advanced Opt-Out feature.
  A send to an opted-out number fails with error **21610**; the recipient must
  text `START` to resubscribe. SolidPing has no inbound-SMS webhook of its own.
- **OVH** would normally append its own STOP clause, which is why SolidPing
  always sends `noStopClause: true`: that clause is legally reserved for
  marketing, and it costs characters (possibly a whole extra billed segment) on
  every alert. Our own footer carries the opt-out language instead.

Acknowledging an incident **by SMS reply** is a Twilio-only capability and is
not offered on the OVH path.

## Spend protection

Four independent limits guard against a runaway escalation loop billing you for
thousands of messages. The first two apply to **every** send, whoever pays:

| Guard | Default | Override |
|---|---|---|
| SMS per hour, per organization | 30 | `SP_ENTITLEMENTS_SMS_RUNAWAY_PER_HOUR` |
| Voice calls per hour, per organization | 10 | `SP_ENTITLEMENTS_CALL_RUNAWAY_PER_HOUR` |

The other two guard **instance spend**, so they apply **only** to
server-provided sends. A bring-your-own send bills the organization's own
Twilio account, so it neither consumes the instance-wide cap nor is gated by
the allow-list:

| Guard | Default | Override |
|---|---|---|
| SMS per hour, instance-wide | 500 | `SP_SMS_GLOBAL_RUNAWAY_PER_HOUR` (negative disables) |
| Destination country allow-list | all countries | `SP_SMS_ALLOWED_COUNTRIES` |

The instance-wide cap closes a real hole: 200 organizations each respecting a
30/hour per-organization guard still add up to 6 000 SMS/hour of *your* money.
The allow-list closes another: a count quota under-prices expensive
destinations — France is around 0,05 € while some destinations exceed 0,30 €, a
six-fold spread on an identical quota.

Both instance guards **log loudly** and surface on the organization's **Usage**
page when they refuse a send. They never fail silently: what they drop is an
alert.

On top of all four, monthly quotas come from the organization's entitlements. A
self-hosted deployment is **unlimited** by default — the operator's own
credentials pay, so SolidPing does not meter them. Managed plans set explicit
monthly caps, visible on the organization's **Usage** page.

## Trial accounts

A Twilio trial account is fine for a first end-to-end test, with three caveats:

- You can only send to **verified caller IDs** — add each destination number
  under **Phone Numbers → Verified Caller IDs** first, or every send fails with
  error **21608**.
- Messages carry a **"Sent from your Twilio trial account"** prefix, which eats
  into the SMS segment.
- Trial credit is small; voice calls burn it fastest.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `account_sid must be a Twilio Account SID (AC…)` | An API Key SID (`SK…`) was pasted instead of the Account SID. |
| `exactly one of from_number or messaging_service_sid is required` | Both are set, or neither is. |
| `Twilio rejected these credentials for region <region>` | The SID/token belong to a different region's account than `region` says — see [Region](#region). |
| `could not verify Twilio credentials for region <region>` | Twilio was unreachable, timed out, or errored while SolidPing tried to confirm the credentials on save. Try again. |
| Twilio error **21608** | Trial account sending to an unverified number. |
| Twilio error **21606** / **21212** | The from-number is not owned by this account, or is not SMS-capable. |
| Twilio error **21610** | The recipient replied `STOP` and is on the opt-out list. They must text `START` to resubscribe. |
| Twilio error **21408** | Geo permissions block that destination country. |
| SMS arrives, no voice call | `voice_from_number` is empty, `SP_BASE_URL` is unset, or the severity lacks the `voice` token. |
| Voice call connects but says nothing useful, or callbacks return 403 | The auth token in the connection no longer matches Twilio's — signature validation fails. |
| Nothing sent, log says paging skipped | The contact is unverified, or neither mode is available: no organization integration **and** no instance-level SMS (`SP_SMS_ENABLED` plus credentials). |
| Instance SMS refused, "country not in allow-list" in the logs | The destination's country calling code is outside `SP_SMS_ALLOWED_COUNTRIES`. Bring-your-own sends are never gated by it. |
| Instance SMS refused, "instance-wide hourly cap exhausted" | `SP_SMS_GLOBAL_RUNAWAY_PER_HOUR` tripped — alerts are being dropped instance-wide. Check the Usage page. |
| OVH returns `403` with credentials you are sure of | Clock skew, or `/auth/time` unreachable — OVH signs against its own clock. |
| OVH delivery receipts never arrive | The SMS account's service-level `callBack` is not set. SolidPing registers it at startup; set it manually in the OVH control panel if that failed. |
| Notification history stuck on `queued` | Twilio cannot reach `SP_BASE_URL` to post delivery status. |
| Sends stop mid-incident | An hourly runaway guard tripped — see [Spend protection](#spend-protection). |
