---
model: opus
effort: medium
---

# SMS verification can probe any number of destinations: no cross-contact throttle, no ops alert on repeated allow-list refusals

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/79

## Source

Production logs showed a brand-new account creating several SMS notification contacts within
about two minutes and requesting a verification code for each, every destination in a different
high-cost country outside `SP_SMS_ALLOWED_COUNTRIES`. The instance allow-list refused every send
(no SMS left the instance), but the pattern is the usual signature of SMS pumping / IRSF probing,
and nothing throttled or flagged it. The report is right on all three points below.

## Problem

1. **The only throttle is per contact.** `VerifyContact`
   (`server/internal/handlers/usernotifications/verify.go:127`) calls
   `resendTracker.allow(contactUID, …)`: at most `verifyResendMax` (3) codes per contact per
   `verifyResendWindow` (1 h) (`verify.go:26-27`, `resend_limiter.go`). Creating a new contact is
   a fresh key, so an account can probe an unbounded number of destinations. Had one of them been
   in an allowed country, the code would have been sent on instance credentials.
2. **No operator signal.** Each refusal is one ERROR line from `LogInstanceSMSBreach`
   (`server/internal/entitlements/instance_sms_guard.go:139-150`), reached from
   `reserveInstanceSMSSpend` (`server/internal/handlers/usernotifications/service.go:884`), the
   escalation job (`jobs/jobtypes/job_escalation_step.go:963`) and the support wiring
   (`app/support_wiring.go:170`). `recordSMSGuardBreach` (`instance_sms_guard.go:186`) only feeds
   the Usage page tally. There is no `opsnotify` event for "N refused destinations from one org in
   M minutes", the actionable signal.
3. **Misleading log attribute.** The `countryCode` attribute (`instance_sms_guard.go:147`) holds,
   when nothing matched, the first three digits of the number (`countryAllowed`,
   `instance_sms_guard.go:248-256`), a dialling prefix hint and not a country code.

## Why a spec

- A new anti-abuse limit with product thresholds (how many destinations, per user or per org,
  stricter for young accounts) that the reporter left open.
- A new `opsnotify` event type (copy, i18n, recipients config) plus an optional temporary block:
  several subsystems (usernotifications, entitlements, opsnotify).
- Correctness hinges on concurrency (in-memory windows shared by concurrent requests) and on
  authorization scope (per user vs per org).

## Proposal

1. **Distinct-destination limiter.** Next to `resendTracker`, add a per-org (and per-user)
   limiter on the number of *distinct* SMS/WhatsApp destinations that may receive a verification
   code within a sliding window: default 5 per org per hour, 2 per hour when the requesting user
   was created less than 24 h ago. Checked in `VerifyContact` before `resolveCodeTransport`;
   re-sending to an already-counted destination does not consume a slot. It returns the same
   error as the resend limiter (`ErrResendTooSoon`, mapped to the same HTTP 429 / code), so the
   dashboard needs no change. In-memory and process-local like `resendLimiter`, with the same
   pruning; thresholds as constants (no new config knob unless trivial).
2. **Ops alert on repeated refusals.** In `entitlements`, keep a per-org sliding window of
   allow-list refusals (`CountryError`). When an org reaches K = 3 refusals within 10 minutes,
   emit one `opsnotify` notice (new event, e.g. `sms_destination_probing`) carrying the org UID
   and the refused prefixes, at most once per org per hour. Wire it where `LogInstanceSMSBreach`
   is called today (a callback/notifier injected into the entitlements service, so the package
   does not import opsnotify if it doesn't already). Localized copy like the other notices.
3. **Temporary block.** While an org is flagged (the hour after the alert), refuse every
   verification send for that org with the rate-limited error, without calling the provider.
4. **Logs.** Individual allow-list refusals drop to WARN once the aggregate alert exists; rename
   the attribute `countryCode` to `destinationPrefix` (keep `CountryError.CountryCode`'s meaning
   documented, or rename the field too).
5. **Tests** (Go): distinct-destination limit per org and for a young account; re-verify of the
   same destination not counted; the alert fires once at K refusals and not again within the
   hour; the block refuses sends while flagged and lifts after; concurrent calls do not exceed
   the limit (`-race`).
6. **Docs**: mention the limits next to `SP_SMS_ALLOWED_COUNTRIES` in the configuration docs.

## Open questions

- **Limit scope and numbers.** Recommended: 5 distinct destinations per org per hour, 2 for an
  account younger than 24 h, per org rather than per user. Trade-off: per org is the cost unit and
  catches several throwaway users in one org, but a large org onboarding many members' phones at
  once may hit it; per user would be laxer for orgs.
- **Temporary block or alert only.** Recommended: block for one hour after the alert. Trade-off:
  stops a probe mid-run at the price of blocking a legitimate user who mistyped several numbers
  (rare, and they get the usual rate-limited message).

## Resolved open questions

- **Limit scope and numbers.** Cap at 5 distinct destinations per org per hour, 2 for an account younger than 24 h, counted per org (not per user). (autopilot: recommended answer)
- **Temporary block or alert only.** After the ops alert fires, block SMS verification for that org for one hour; the user gets the usual rate-limited message. (autopilot: recommended answer)

## Closing the issue

This spec closes fclairamb/solidping-business#79. The implementing PR body **must** carry a
`Closes fclairamb/solidping-business#79` line. When the spec is archived to `specs/done/`, verify
the issue is closed and, if not, close it by hand:

    gh issue close 79 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
