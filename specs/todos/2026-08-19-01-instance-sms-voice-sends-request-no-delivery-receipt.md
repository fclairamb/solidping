---
model: opus
effort: high
---

# Server-provided SMS and voice sends request no delivery receipt, so the notification timeline silently never leaves its initial state

## Problem

`twilioStatusCallbackURL` returns the empty string whenever `conn` is nil
(`server/internal/jobs/jobtypes/job_escalation_step.go:927`):

```go
func twilioStatusCallbackURL(baseURL string, conn *models.Integration) string {
	if baseURL == "" || conn == nil {
		return ""
	}
	...
}
```

`conn` is nil for **every send made on the instance's own credentials** — the
server-provided mode that is the default and needs no org setup. Both call
sites pass the result straight through: `sendPhoneSMS` (`:954`) and
`placePhoneCall` (`:991`). Twilio only posts a status callback when the send
requested one, so on the server-provided path **no delivery receipt is ever
requested, for SMS or for voice**.

The consequence is silent. `UpdateIncidentNotificationDeliveryByMessageID`
(`server/internal/handlers/twiliocb/handler.go:218`) is never reached for these
sends, so the audit row written by `auditPhoneSend` keeps whatever state it was
created with. Nothing errors and nothing is logged — the timeline simply always
shows the same thing, whether the message reached the handset, was rejected by
the carrier, or hit an opted-out number.

**The documentation promises the opposite.** `web/docs/docs/configuration/sms.md`
states, of the two modes: *"Delivery status flows back in both modes, so the
notification history shows the provider's own delivered / pending / failed
state"*, and its troubleshooting table tells operators to read the
delivered/failed state to diagnose paging. On the default mode that state does
not exist.

**Observed 2026-08-19.** On a deployment configured for server-provided Twilio
SMS, a verification SMS was confirmed `delivered` with `error_code: null` by
Twilio's own Messages API, while SolidPing itself held no delivery state for
it. The message arrived; the product could not tell.

### Why this is not simply "the callback URL is missing"

The instance path is **already** callback-capable, and voice proves it. The
sentinel `sms.InstanceCallbackID` (`= "__instance__"`,
`server/internal/integrations/sms/resolve.go:200`) exists precisely so a
server-credential send can be called back:

- `voiceCallbackConnID` (`job_escalation_step.go:1009`) returns the sentinel
  when there is no per-org connection, and the TwiML URL built at `:986`
  carries `cid=__instance__` plus `oid=<orgUID>`.
- `resolveCallbackCredentials` (`twiliocb/handler.go:121`) recognises the
  sentinel, takes the org from `oid`, and validates the Twilio signature
  against the instance auth token.

So the outbound alert call already fetches its TwiML and accepts its DTMF
acknowledgement through the sentinel, verified working end to end. Only the
*status* URL builder was never taught the same trick. The defect is an
asymmetry between two adjacent functions, not a missing capability.

## Proposal

### 1. Build the instance status-callback URL

Give `twilioStatusCallbackURL` the org UID and let it emit the sentinel form
when `conn` is nil, mirroring the voice URL it sits next to:

```
<baseURL>/api/v1/integrations/twilio/status?cid=__instance__&oid=<orgUID>
```

Both call sites have `incident.OrganizationUID` in hand. Keep returning `""`
when `baseURL` is empty — an unreachable base URL is still a reason to request
no callback.

### 2. Resolve the callback token per capability, not always from voice

This is the part that must not be rushed. The sentinel branch currently does:

```go
if orgUID == "" || !h.cfg.Voice.Active() {
	return nil, ""
}
return &callbackTarget{orgUID: orgUID, connUID: cid}, h.cfg.Voice.Twilio.AuthToken
```

It gates on **voice** being active and validates with the **voice** auth token.
That is correct for the TwiML and gather endpoints, which are voice-only. It is
wrong for an SMS status callback, and two supported configurations break on it:

- **Twilio SMS with voice disabled.** `SP_VOICE_ENABLED=false` makes
  `Voice.Active()` false, so a perfectly valid SMS status callback is rejected
  with a bare `403`.
- **OVH for SMS, Twilio for voice.** An explicitly supported pairing (see
  `VoiceConfig`'s doc comment and the SMS docs). Here the SMS status callback
  must not be validated against Twilio voice credentials at all — the OVH path
  ignores `StatusCallback` entirely by design
  (`server/internal/integrations/sms/sender.go:177`) and has its own
  service-level DLR endpoint.

Resolve the token from the capability the callback belongs to: the SMS status
path should use `cfg.SMS.Twilio.AuthToken` gated on the instance SMS provider
actually being Twilio and active; the voice paths keep using
`cfg.Voice.Twilio.AuthToken`. Where a single `/status` endpoint serves both,
distinguish by payload (`MessageSid` vs `CallSid`, already discriminated at
`handler.go:206`) or by carrying the capability on the signed URL — and reject
rather than guess when neither matches.

Note the two tokens are frequently the *same string* (one Twilio account
serving both), which is exactly why this must be driven by config rather than
by whichever happens to be populated: a test on a deployment where they match
will pass no matter which one the code reads.

### 3. Do not regress the security properties

The existing guarantees must survive:

- `oid` is attacker-supplied but sits inside the signed URL, so a forged org id
  cannot pass signature validation. Adding `oid` to the SMS status URL inherits
  this and must not weaken it.
- Any failure to resolve credentials returns a bare `403` with no detail
  (`handler.go:83`). Keep it bare — a distinguishing error message would let an
  unauthenticated caller probe which orgs and modes exist.
- `HandleStatus` scopes the DB write by `target.orgUID`, so a callback can only
  touch its own org's rows. The sentinel path must keep taking the org from the
  signed `oid` and never from the payload.

## Verification

Tests must prove the negative, since the current failure is silent and a test
that merely asserts "a callback was handled" passes today for voice.

1. **The bug, pinned.** A server-credential SMS send records a `StatusCallback`
   parameter on the outbound Twilio request. Assert on the request the fake
   Twilio API actually received — asserting on the URL builder's return value
   alone would not have caught this defect, because the builder is
   *correct* for the BYO case it was written for.
2. **Round trip.** A status callback posted to the sentinel URL with a valid
   signature updates the notification row; the same payload with a wrong
   signature returns `403` and writes nothing.
3. **Positive controls for the two broken configurations.** With
   `SP_VOICE_ENABLED=false` and Twilio SMS active, an SMS status callback must
   be accepted. With OVH as the SMS provider and Twilio voice active, an SMS
   send must request no Twilio status callback, and a voice callback must still
   work.
4. **Cross-capability rejection.** A callback carrying a `CallSid` must not be
   validated with the SMS token, nor a `MessageSid` with the voice token, on a
   deployment where the two tokens differ.
5. **BYO unchanged.** Existing per-org connection callbacks keep working, with
   `cid` still naming the connection.

## Out of scope

- The OVH delivery-receipt path, which has its own service-level callback and
  token (`SP_SMS_OVH_DLR_TOKEN`) and is unaffected.
- Surfacing delivery state in the dashboard beyond what the timeline already
  renders for the bring-your-own mode.

## Implementation Plan

### 1. `job_escalation_step.go` — build the callback URL for both modes

- `twilioStatusCallbackURL(baseURL, orgUID, connID string) string` now takes the
  `cid` to carry rather than a `*models.Integration`, and always appends
  `oid=<orgUID>` — exactly like the TwiML URL next to it, which already carries
  `oid` in both modes. Still returns `""` when `baseURL` (or `connID`) is empty.
- `smsStatusCallbackURL(cfg, baseURL, orgUID, resolution)` picks the `cid`:
  the org integration's UID for a bring-your-own SMS, the `__instance__`
  sentinel for a server-credential one — and returns `""` when the instance SMS
  provider is not Twilio (OVH posts no Twilio status callback and its receipts
  arrive on the service-level DLR endpoint).
- `voiceStatusCallbackURL(baseURL, orgUID, resolution)` reuses the existing
  `voiceCallbackConnID`, so the status URL and the TwiML URL of the same call
  can never disagree on `cid`. This also fixes the latent case of an org that
  has a Twilio integration without `voice_from_number`: voice falls through to
  the instance, so the status callback must carry the sentinel, not the
  connection UID.

### 2. `twiliocb/handler.go` — resolve the token per capability

- Introduce a `capability` discriminator and split the middleware in two:
  `VerifyVoiceMiddleware` (TwiML + gather, voice-only by construction) and
  `VerifyStatusMiddleware` (`/status`, either capability).
- `req.ParseForm()` moves ahead of credential resolution, because the status
  path discriminates on the payload.
- Instance-sentinel token resolution:
  - voice endpoints → `cfg.Voice.Twilio.AuthToken`, gated on `Voice.Active()`;
  - `/status` with a `MessageSid` and no `CallSid` → `cfg.SMS.Twilio.AuthToken`,
    gated on `SMS.Active()` **and** the resolved provider being Twilio;
  - `/status` with a `CallSid` and no `MessageSid` → the voice token;
  - anything else (neither, or both) → reject. No guessing.
- Bring-your-own resolution is untouched: the connection named by `cid` still
  supplies the token and the org.
- Security properties preserved: `oid` is read only from the signed URL, every
  resolution failure still returns a bare `403` with no body, and `HandleStatus`
  still scopes the write by `target.orgUID`.

### 3. `app/server.go` — point the three routes at the right middleware.

### 4. Tests

- `job_escalation_step_status_callback_test.go` (new): asserts on the form the
  fake Twilio API actually received — server-credential SMS and voice both
  carry `StatusCallback` with `cid=__instance__&oid=<org>`; bring-your-own still
  carries `cid=<connUID>`; OVH-for-SMS carries no Twilio callback at all.
- `twiliocb/handler_test.go`: sentinel status round trip (valid signature
  updates the row, wrong signature returns 403 and writes nothing), the two
  previously broken configurations, cross-capability rejection with two
  DIFFERENT tokens, and the unchanged bring-your-own path.
