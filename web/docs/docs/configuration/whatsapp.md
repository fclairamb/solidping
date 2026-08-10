---
sidebar_position: 7
title: WhatsApp
---

# WhatsApp Alerts

SolidPing can page on-call engineers over **WhatsApp**, using Meta's WhatsApp
Business Cloud API directly — no Business Solution Provider (BSP) in between,
and no per-message reseller markup.

WhatsApp is usually cheaper than SMS, delivers far more reliably
internationally, and is the primary messaging app for a large share of the
world. It sits alongside SMS and voice as a **direct channel**: a user verifies
their own number, and severities can route to it with the `whatsapp` channel
token.

## How it works

- **One WhatsApp Business Account (WABA) per installation.** The credentials are
  instance-level, exactly like SMTP — not per organization. A SaaS deployment
  supplies one set; a self-hoster supplies their own. No code path differs.
- **Users opt in per number.** A user adds a WhatsApp contact under
  **Account → Notifications** and completes a code round-trip. That verification
  is both the reachability proof and Meta's required record of consent to
  receive business-initiated messages. A WhatsApp contact is deliberately
  **separate** from an SMS phone contact, even when the digits are identical:
  consenting to SMS is not consenting to WhatsApp.
- **Templates only.** Alerts are business-initiated, and WhatsApp does not
  deliver free-form text outside a 24-hour customer-service window. SolidPing
  therefore sends only pre-approved message templates.
- **Delivery statuses come back over a webhook**, so the notification history
  shows `sent` → `delivered` → `read`, or a failure with Meta's reason.

## Prerequisites

You need, on Meta's side:

1. A **Meta app** of type *Business*, with the **WhatsApp** product added.
2. A **WhatsApp Business Account (WABA)** and a **sender phone number**. Meta
   provides a free test number for development; production needs a real number
   you control that is not currently registered to a personal WhatsApp account.
3. A **permanent system-user access token** with the
   `whatsapp_business_messaging` permission. The 24-hour token shown in the
   developer dashboard is for manual testing only — a running server needs a
   permanent one, created under **Business Settings → Users → System users**.
4. Two **approved message templates** (see below). Approval is asynchronous and
   typically takes minutes to a few hours.

:::note
Template approval is an **offline prerequisite**. SolidPing never creates
templates for you: Meta's review is asynchronous, so a boot-time create would
add a write-scoped token requirement for no latency benefit. If a template is
missing, paused or disabled, the send is recorded as a handled delivery failure
("WhatsApp message template unavailable") and the escalation moves on to the
next step — it never crashes the server.
:::

## Step 1 — Create the templates

Create both templates in **WhatsApp Manager → Message templates**, in the
language you configure as `template_language` (default `en`).

### Alert template

- **Name**: `solidping_alert`
- **Category**: **Utility**
- **Body** (four variables):

  ```text
  Alert: {{1}} is now {{2}}. Please check your SolidPing dashboard for more information.
  Detail: {{3}}
  Organization: {{4}}. Sent automatically by the SolidPing monitoring platform.
  ```

| Variable | Meaning | Sample |
|---|---|---|
| `{{1}}` | Check or incident name | `Payments API` |
| `{{2}}` | New state | `DOWN` / `ESCALATED` / `RESOLVED` |
| `{{3}}` | Detail line | `connection refused` |
| `{{4}}` | Organization slug | `acme` |

:::caution Three rules the body must satisfy
WhatsApp Manager rejects a body that:

1. **begins or ends with a placeholder**;
2. contains **two adjacent placeholders** (`{{1}} {{2}}` with nothing between);
3. has **too many variables for its length** — the error reads *"This template
   has too many variables for its length. Reduce the number of variables or
   increase the message length."*

Rule 3 is the one that catches people out: four variables need a fair amount of
surrounding prose. A terse body such as `Alert: {{1}} is now {{2}}.` / `Detail:
{{3}}` / `Organization: {{4}}. Sent by SolidPing.` is **rejected** even though it
satisfies rules 1 and 2. The body above is padded enough to pass; if you shorten
it, keep the ratio of static text to placeholders roughly comparable.

SolidPing only cares about the **order and count** of the variables, never the
surrounding words, so you are free to reword the static text (or translate it)
as long as the four placeholders stay in the order given, none of them is the
first or last thing in the body, and the body stays long enough for rule 3.
:::

One template covers **down, escalated and resolved** because the state is a
variable. Do not create three separate templates — each one is a separate
approval to maintain.

### Verification template

- **Name**: `solidping_verify`
- **Category**: **Authentication**

:::danger Authentication templates require a verified business
Meta refuses to create **any** Authentication-category template until your
business portfolio has completed **business verification**, with the misleading
error *"This WhatsApp business account does not have permission to create
message template"* — even though Utility templates create fine on the same WABA
minutes earlier. Nothing about the template is wrong; the account simply is not
verified yet.

Complete verification first (Business settings → Security Center → Business
Verification). Once it passes, the same template is created **and approved
within seconds**. Until then, WhatsApp contact verification cannot work at all,
so plan the verification well ahead of your rollout.
:::

Authentication templates are not free-form: Meta supplies the body copy and
**requires a copy-code button**. SolidPing sends the 6-digit code twice — once as
the body variable and once as the button parameter — which is the only shape Meta
accepts for this category. Getting the template shape wrong is the single most
common cause of every verification send failing with error **132000**
(component/parameter mismatch), so create it exactly as below.

**In WhatsApp Manager (UI path):**

1. **New template → Category: Authentication → One-time Passcode**.
2. Under **Code delivery setup**, select **Copy code**. This matters: the form
   defaults to **Zero-tap autofill**, which (a) requires you to accept the
   WhatsApp Business Terms of Service and (b) demands an Android **package name**
   and **app signature hash** — neither of which exists for a web application.
   *One-tap autofill* has the same Android-app requirement. Only **Copy code**
   matches what SolidPing sends.
3. Leave the body as the Meta-supplied authentication copy (*Add security
   recommendation* is ticked by default and is fine to keep).
4. Optionally tick **Add expiration time for the code** and set `10` minutes,
   matching SolidPing's own code TTL.

:::note Zero-tap is not an option for SolidPing
Zero-tap and one-tap autofill deliver the code straight into *your Android app*
via a broadcast intent, so they are Android-only and require an APK you control.
They are irrelevant to a web dashboard, and non-Android recipients fall back to a
copy-code button anyway.
:::

**Via the WABA API (`POST /{waba-id}/message_templates`):**

```json
{
  "name": "solidping_verify",
  "category": "AUTHENTICATION",
  "language": "en",
  "components": [
    { "type": "BODY", "add_security_recommendation": true },
    { "type": "FOOTER", "code_expiration_minutes": 10 },
    {
      "type": "BUTTONS",
      "buttons": [
        { "type": "OTP", "otp_type": "COPY_CODE", "text": "Copy code" }
      ]
    }
  ]
}
```

The `"type": "OTP"` button with `"otp_type": "COPY_CODE"` is mandatory. A
body-only authentication template — created without that buttons component —
will be approved but will reject every send SolidPing makes.

**What SolidPing sends at delivery time** (so you can match the template to it):

```json
{
  "messaging_product": "whatsapp",
  "recipient_type": "individual",
  "to": "15551234567",
  "type": "template",
  "template": {
    "name": "solidping_verify",
    "language": { "code": "en" },
    "components": [
      { "type": "body", "parameters": [{ "type": "text", "text": "123456" }] },
      {
        "type": "button",
        "sub_type": "url",
        "index": "0",
        "parameters": [{ "type": "text", "text": "123456" }]
      }
    ]
  }
}
```

The button component uses `sub_type: "url"` at send time even though the
template declares an `OTP` / `COPY_CODE` button — that is Meta's documented
send-side representation for copy-code buttons, not a mismatch.

Both names are configurable if your WABA already uses a naming convention.

## Step 2 — Configure SolidPing

All settings are environment variables (or the equivalent `whatsapp:` block in
the config file). **`SP_WHATSAPP_ACCESS_TOKEN` and `SP_WHATSAPP_APP_SECRET` are
secrets**: keep them in your secret store (SSM, Docker secrets, Kubernetes
secrets), never in a committed config file. SolidPing never logs them, never
returns them from an API, and never sends them to a browser.

```bash
SP_WHATSAPP_ENABLED=true
SP_WHATSAPP_ACCESS_TOKEN=EAAG...            # permanent system-user token (SECRET)
SP_WHATSAPP_PHONE_NUMBER_ID=123456789012345 # numeric id, not the phone number
SP_WHATSAPP_WABA_ID=987654321098765         # optional, diagnostics only
SP_WHATSAPP_APP_SECRET=abcdef0123456789     # signs inbound webhooks (SECRET)
SP_WHATSAPP_WEBHOOK_VERIFY_TOKEN=a-long-random-string
```

Optional overrides:

```bash
SP_WHATSAPP_API_VERSION=v23.0               # pinned Graph API version
SP_WHATSAPP_ALERT_TEMPLATE=solidping_alert
SP_WHATSAPP_VERIFY_TEMPLATE=solidping_verify
SP_WHATSAPP_TEMPLATE_LANGUAGE=en
SP_WHATSAPP_BASE_URL=                       # override the Graph API base
```

```yaml
whatsapp:
  enabled: true
  access_token: EAAG...
  phone_number_id: "123456789012345"
  waba_id: "987654321098765"
  app_secret: abcdef0123456789
  webhook_verify_token: a-long-random-string
  api_version: v23.0
  alert_template: solidping_alert
  verify_template: solidping_verify
  template_language: en
```

The feature is **off unless all three of** `enabled`, `access_token` and
`phone_number_id` are set. Anything less and the dashboard hides the WhatsApp
contact type entirely — it does not offer a channel that cannot deliver.

:::tip Why the API version is pinned
`api_version` defaults to a specific Graph API version rather than tracking the
newest. A Meta version rollout can change payload semantics; pinning means you
bump it deliberately, after re-testing, instead of discovering the change during
an outage.
:::

`SP_WHATSAPP_BASE_URL` exists for two legitimate cases: fronting Meta with an
egress proxy, and pointing a staging installation at a fake Graph API for
end-to-end testing. Leave it empty in production.

## Step 3 — Subscribe the webhook

Delivery statuses and inbound replies arrive at:

```text
https://<your-solidping-host>/api/v1/integrations/whatsapp/webhook
```

In the Meta app dashboard, under **WhatsApp → Configuration → Webhook**:

1. **Callback URL**: the URL above.
2. **Verify token**: the same string as `SP_WHATSAPP_WEBHOOK_VERIFY_TOKEN`.
3. Click **Verify and save**. Meta issues a `GET` handshake; SolidPing echoes
   the challenge only when the token matches.
4. Subscribe to the **`messages`** field.

The route only exists when WhatsApp is configured — an installation without
credentials exposes no WhatsApp endpoint at all.

### Webhook security

Every `POST` must carry a valid `X-Hub-Signature-256` header: an HMAC-SHA256 of
the exact raw request body, keyed by your app secret. SolidPing validates it
**before parsing the body**, and answers `403` with no detail on a missing,
malformed or mismatched signature. This is the only thing standing between a
public endpoint and an anonymous caller writing to your delivery records — if
`app_secret` is wrong, every webhook silently fails signature validation, and
delivery statuses simply stop updating.

## Step 4 — Users add their numbers

Each user goes to **Account → Notifications → + Add method → WhatsApp**, enters
their number in international **E.164** format (`+15551234567`), and clicks
**Verify**. They receive a 6-digit code over WhatsApp and enter it back.

Until a contact is verified it is **never messaged**. Codes expire after 10
minutes, allow 5 attempts, and are rate-limited to 3 sends per hour per contact.

## Step 5 — Route severities to WhatsApp

Add the `whatsapp` channel token to whichever severities should page over
WhatsApp. Like `voice`, WhatsApp is only ever used on an **explicit** token — an
escalation with no severity attached will not send WhatsApp messages. That is
deliberate: a user who opted in for critical pages should not be messaged for
everything.

## Quotas and runaway protection

Two independent limits apply:

| Limit | Default | Purpose |
|---|---|---|
| `maxWhatsappPerMonth` entitlement | unlimited (self-hosted) | Per-organization monthly cap, shown on **Organization → Usage** |
| `SP_ENTITLEMENTS_WHATSAPP_RUNAWAY_PER_HOUR` | `30` | Per-organization hourly guard against a broken escalation loop |

Self-hosted installations are unlimited by default — you pay Meta directly, so
SolidPing does not meter you. The hourly runaway guard still applies, because a
bug should not be able to run up an unbounded bill.

Exceeding either limit skips the send and records it in the notification
history; it never fails the escalation step.

## Troubleshooting

| Symptom in the delivery history | Meaning | Fix |
|---|---|---|
| *WhatsApp message template unavailable* | Template missing, paused, disabled, or the parameters do not match its definition | Check the template's status in WhatsApp Manager; a template can be paused automatically after negative user feedback |
| *Recipient is not on WhatsApp* | The number is not a reachable WhatsApp user (Meta code 131026) | Have the user re-add the correct number |
| *WhatsApp rejected the message we built* | Meta code 131051 — an unsupported message type or a component shape the approved template does not declare | A SolidPing-side or template-shape problem, **not** a bad number. Re-check the template against "What SolidPing sends at delivery time" above |
| *Outside the 24-hour WhatsApp session window* | Meta code 131047 — a non-template message needed an open session window | Should not occur in v1 (templates only); report it if you see it |
| *Rate limited by WhatsApp (messaging tier cap)* | Meta throttled the send — app request limit, WABA messaging tier, or per-user engagement pacing (code 131049) | Transient. Raise the messaging tier by growing quality-rated volume; the next escalation repeat may succeed. **Not** a sign of a bad recipient number |
| *WhatsApp credentials expired* | The access token is expired, revoked, or lacks `whatsapp_business_messaging` | Re-issue the permanent system-user token |
| *WhatsApp is not configured on this instance* | The kill switch is off, or a credential is missing | Check `SP_WHATSAPP_ENABLED`, `SP_WHATSAPP_ACCESS_TOKEN`, `SP_WHATSAPP_PHONE_NUMBER_ID` |
| Statuses never move past `sent` | Webhook not delivering | Re-check the callback URL, the verify token, the `messages` subscription, and that `app_secret` matches the app |

## Limits of the current version

- No bring-your-own-WABA per organization — credentials are instance-level.
- Inbound replies are accepted and logged, but carry no commands: you cannot
  acknowledge an incident by replying (use the SMS ack link or the dashboard).
- No rich or interactive messages, and no free-form follow-ups inside the
  24-hour session window.
- Status-page subscribers cannot subscribe over WhatsApp.
