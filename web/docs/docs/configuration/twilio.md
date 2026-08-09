---
sidebar_position: 8
title: SMS & Voice (Twilio)
---

# SMS & Voice Alerts (Twilio)

SolidPing pages on-call engineers over **SMS** and **voice calls** through
[Twilio](https://www.twilio.com). These are the two channels that reliably wake
someone at 3 a.m., and a voice call can be **acknowledged from the phone
keypad** without unlocking the screen.

Unlike [WhatsApp](./whatsapp.md), which is configured once per installation,
Twilio is a **per-organization connection**: each organization brings its own
Twilio account and pays Twilio directly. Nothing is configured with environment
variables.

## How it works

There are two independent ways an alert reaches a phone:

- **Per-user phone contacts.** A user adds their own number under
  **Account → Notifications** and completes a 6-digit code round-trip. Severity
  channel tokens `sms` and `voice` then route escalation steps to that verified
  number. This is the paging path.
- **A shared destination channel.** The connection's `to_numbers` list receives
  the same broadcast notifications as any other channel (per-check
  subscriptions, escalation `connection` targets, the **Test** button). Useful
  for a team phone or a NOC handset, and it needs no per-user verification.

Delivery status flows back: SolidPing attaches a signed `StatusCallback` to
every message and call, so the notification history shows Twilio's own
`queued` → `sent` → `delivered` (or `failed`) state.

## Prerequisites

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

## Step 1 — Get a phone number

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

### Alternative: a Messaging Service

Instead of a from-number you can send SMS through a **Messaging Service**
(**Messaging → Services**), which adds sender pools, sticky sender and
country-based routing. Configure its SID (`MG…`) as `messaging_service_sid` and
leave `from_number` empty — SolidPing accepts exactly one of the two.

A Messaging Service covers SMS only. Voice calls always need
`voice_from_number`.

### Regional and regulatory setup

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

## Step 2 — Copy the credentials

The **Account SID** and **Auth Token** are on the Twilio Console home page.

Consider creating a **subaccount** (**Account → Subaccounts**) dedicated to
SolidPing: it has its own SID/token pair, so rotating or revoking it does not
affect anything else you run on Twilio, and usage shows up on its own line in
the bill.

## Step 3 — Create the connection in SolidPing

**Integrations → New integration → Twilio**, then fill in:

| Field | Required | Notes |
|---|---|---|
| `account_sid` | yes | Must start with `AC` and be 34 characters. |
| `auth_token` | yes | **Secret** — encrypted at rest when `SP_ENCRYPTION_MASTER_KEY` is set (see [Security](./security.md)). Never returned by the API. |
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

Use **Test** to send to `to_numbers` and confirm credentials end to end.

## Step 4 — No webhook configuration needed

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

## Step 5 — Verify a phone number

Each user adds their own number under **Account → Notifications → add contact →
Phone (SMS)**, then requests a code:

- A 6-digit code arrives by SMS: *"[SolidPing] Your verification code is 123456
  (valid 10 minutes)."*
- The code is valid **10 minutes**, allows **5 attempts**, and can be re-sent at
  most **3 times per hour** per contact.
- **An unverified phone contact is never paged.** This is what keeps a typo
  from sending alerts to a stranger.

If no Twilio connection is configured for the organization, requesting a code
returns a validation error naming the missing provider.

## Step 6 — Route severities to phone

Add the channel tokens to the severities that should page:

| Token | Effect |
|---|---|
| `sms` | Sends an SMS to every verified phone contact on the target. |
| `voice` | Additionally places a voice call — requires `voice_from_number`. |

A severity with **no** channel filter keeps meaning "every channel", as before.
A severity listing only `email`/`slack` is unaffected by this feature.

### What an alert looks like

An escalation SMS is deliberately short, to stay within one GSM-7 segment:

```text
[SolidPing] acme: Payments API needs attention (escalated). Ack: https://…/ack?token=…
```

The `Ack:` link is a signed one-click acknowledgement — no login needed.

A voice call reads the organization and check name aloud, then:

> *Press 4 to acknowledge.*

Pressing **4** acknowledges the incident from the phone. Anything else re-prompts
once, then hangs up. Acknowledging — by keypad, by link, or in the dashboard —
cancels every pending escalation step, so no further SMS or call goes out.

## Spend protection

Two independent limits guard against a runaway escalation loop billing you for
thousands of messages:

| Guard | Default | Override |
|---|---|---|
| SMS per hour, per organization | 30 | `SP_ENTITLEMENTS_SMS_RUNAWAY_PER_HOUR` |
| Voice calls per hour, per organization | 10 | `SP_ENTITLEMENTS_CALL_RUNAWAY_PER_HOUR` |

On top of that, monthly quotas come from the organization's entitlements. A
self-hosted deployment is **unlimited** by default — you pay Twilio directly, so
SolidPing does not meter you. Managed plans set explicit monthly caps, visible
on the organization's **Usage** page.

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
| Twilio error **21608** | Trial account sending to an unverified number. |
| Twilio error **21606** / **21212** | The from-number is not owned by this account, or is not SMS-capable. |
| Twilio error **21610** | The recipient replied `STOP` and is on the opt-out list. They must text `START` to resubscribe. |
| Twilio error **21408** | Geo permissions block that destination country. |
| SMS arrives, no voice call | `voice_from_number` is empty, `SP_BASE_URL` is unset, or the severity lacks the `voice` token. |
| Voice call connects but says nothing useful, or callbacks return 403 | The auth token in the connection no longer matches Twilio's — signature validation fails. |
| Nothing sent, log says paging skipped | The contact is unverified, or the organization has no enabled Twilio connection. |
| Notification history stuck on `queued` | Twilio cannot reach `SP_BASE_URL` to post delivery status. |
| Sends stop mid-incident | An hourly runaway guard tripped — see [Spend protection](#spend-protection). |
