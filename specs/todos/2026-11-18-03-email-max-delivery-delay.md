---
model: sonnet
effort: high
pr: batch
automerge: false
---

# An email check stays up when the probe mail arrives 13 minutes late, and notices a lost mail only a day later

## Problem

An `smtp` check in send mode mails a probe to an `email` check. The probe carries
`X-SolidPing-Sent-At` (`server/internal/handlers/emailcheck/handler.go:46`), so the handler knows
the send to receive delay.

- `enrichFromSMTPHeaders` (`handler.go:285`) writes that delay as the `latencyMs` output key and
  metric, and nothing else reads it. A probe sent at 09:52:13 and received at 10:05:42
  (`latencyMs: 809000`) is recorded `up`.
- `recordResult` (`handler.go:415`) hardcodes the result duration to 0 (`handler.go:427`). The
  response-time chart of an email check is flat, and the slow rule (`slow_threshold_ms`,
  `server/internal/degraded/rule.go`, `server/internal/handlers/degradedeval/service.go:265`)
  never sees a slow delivery.
- A probe that never arrives is only noticed by `passiveVerdict`
  (`server/internal/checkworker/worker.go:2229`) when the email check's period runs out. With a
  1 day period, a relay that drops mail is reported about a day late, although the server knows
  the exact time the probe was sent.
- `EmailConfig` (`server/internal/checkers/checkemail/config/config.go:43`) has no delay option.

## Proposal

1. **Config.** Add an optional `max_delivery_delay` to `EmailConfig` (`config/config.go:43`): a Go
   duration string (`10m`), absent means today's behaviour exactly. Parse it in `FromMap`, emit it
   in `GetConfig`, validate it in `ValidateSpec` (positive, at least 1 minute, lower than the check
   period when the period is known), and regenerate `server/internal/checkers/schemas/email.json`.
   It only applies to probes that carry `X-SolidPing-Sent-At`: a mail from any other sender has no
   trustworthy send time (the `Date:` header is the sender's clock) and is never judged late.
2. **Duration.** In `recordResult` (`handler.go:427`), set the result duration to the delivery
   latency when `enrichFromSMTPHeaders` computed one, keep 0 otherwise. Keep the `latencyMs`
   output key and metric as they are. Reorder so the latency is known before the status is decided.
3. **Arrived late.** In `recordResult`, when the token-derived status is `up`, the limit is set and
   the latency exceeds it, the result is `down` with the message
   `delivered after 13m29s, limit 10m`. Order of precedence: an explicit `down` / `error` from the
   sender first, then a sender-authentication failure (`applySenderAuth`, `handler.go:502`), then
   the delay. When both the authentication and the delay fail, join the two messages. The `auth`
   output block is kept in every case.
4. **Never arrived.** The email check goes `down` at send time + limit, without waiting for its
   period. Reference: the last successful send of each `smtp` check that delivers to it (the
   relation behind `delivery_check_uid`, `server/internal/checkers/checksmtp/config/config.go:81`,
   and `delivery_to`). When such a send is older than the limit and no result of the email check
   carries its `sentAt`, record an evaluation row `down` (timeout wording:
   `sent at 09:52, not received after 10m`), with `sentAt`, `sourceCheckUid` and the usual
   evaluation markers of `passiveVerdict`. It must be evaluated near send time + limit, not once
   per period: pick the mechanism that fits the passive loop (a one-off evaluation scheduled by the
   SMTP send, or a shorter evaluation cadence for an email check with a limit) and document it in
   the spec's done notes. One row per missed probe, never one per tick.
5. **Late after timeout.** A probe that arrives after its timeout row is recorded as in step 3
   (`down`, real latency). The check recovers on the next probe that arrives in time.
6. **Degraded tier.** No second threshold. With step 2 the slow rule can read the delivery
   latency. Confirm the degraded sweep evaluates email checks (see To verify); if it skips passive
   checks, make it cover an email check whose slow rule is configured.
7. **Frontend** (`web/dash0/src/components/checks/form/types/misc.tsx`): a "Maximum delivery
   delay" input in the email check form, `data-testid="email-max-delivery-delay-input"`, with a
   help line saying it needs a paired SMTP check. i18n keys in every locale the app ships
   (`web/dash0/src/locales/*/checks.json`). The edit PATCH already carries the email config (#868).
8. **Docs** (`web/docs/docs/`, the email check page): the option, the two failure messages, and
   the advice that follows from them: a 10 minute limit calls for an hourly SMTP probe, not a
   daily one, and the slow rule defaults to 3 slow probes out of 6, so at one mail a day set it to
   1 of 1. Add a `CHANGELOG.md` entry.
9. **Sender-authentication check.** A result recorded on v0.49.1 for an email check whose stored
   config had `require_spf`, `require_dkim`, `require_dmarc` and `check_blocklists` showed no
   `auth` key in its output. Read `applySenderAuth` (`handler.go:502`) and `AuthEnabled`
   (`config/config.go:59`) for a path that skips the evaluation on a configured check (for example
   `check.Config` not loaded on the check `HandleEmail` resolves by token). Fix it if there is
   one. Either way add the test below.

## Tests

- `server/internal/checkers/checkemail/config/`: `max_delivery_delay` round-trips through
  `FromMap` / `GetConfig`; rejected when not a duration, negative, under 1 minute, or not lower
  than the period; absent leaves the config map unchanged.
- `server/internal/handlers/emailcheck/`: a probe inside the limit is `up` with duration equal to
  the latency; a probe over the limit is `down` with the `delivered after … limit …` message and
  the real duration; the same late probe with no limit set is `up` (positive control); a mail
  without `X-SolidPing-Sent-At` is `up` whatever the limit and has duration 0; an explicit `down`
  subject tag wins over the delay; an authentication failure plus a late delivery gives both
  messages and keeps `auth`.
- Never arrived: an SMTP send older than the limit with no matching email result yields exactly
  one `down` evaluation row with the timeout message, and a second evaluation adds none; a send
  younger than the limit yields none; a check without `max_delivery_delay` keeps today's
  period-based verdict; the late mail arriving afterwards records a `down` result, and the next
  in-time probe records `up`.
- Degraded: an email check with the slow rule at 1 of 1 and a latency above `slow_threshold_ms`
  opens a degraded incident; with the latency below it, none.
- Step 9: `HandleEmail` on a check whose stored config has the four options records an `auth`
  block, resolved through the same token lookup production uses (not a hand-built check).
- E2E (`web/dash0/e2e/email-check-sender-auth.spec.ts` or a sibling): set the delay on create and
  on edit, reload, assert the stored config and the unchanged token.

## To verify

- Whether `ListChecksForDegradedEval` and `Service.probes`
  (`server/internal/handlers/degradedeval/service.go:265`) include passive checks and read the
  result duration of an email result.
- How the passive loop schedules an email check's evaluation (`passiveVerdict`,
  `worker.go:2229`, `inFirstSignalGrace`, `worker.go:2204`) and which of the two mechanisms of
  step 4 is the smaller change.
- How an SMTP send result exposes its send time and target (`sentAt` in its output, or only the
  header on the mail), so step 4 can match a send to an email result.
- The docs page that describes the email check under `web/docs/docs/`.

## Open questions

- Should a late probe keep the check `down` until the next probe? Recommended: yes. The verdict
  is per probe, and the remedy is a more frequent SMTP probe.
- Should the delay failure be `down`, or `degraded`? Recommended: `down`, with the slow rule as
  the softer tier.
- Should `max_delivery_delay` be rejected when it is not lower than the check period?
  Recommended: yes, a limit longer than the period can never fire before the period verdict.

## Resolved open questions

- A late probe keeps the check `down` until the next probe: the verdict is per probe. (autopilot: recommended answer)
- The delay failure is `down`; the slow rule stays the softer tier. (autopilot: recommended answer)
- Reject `max_delivery_delay` when it is not lower than the check period. (autopilot: recommended answer)
