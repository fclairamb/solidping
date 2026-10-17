---
model: opus
effort: medium
---

# Hosted plans include no SMS or voice, and bring-your-own Twilio is blocked too

## Problem
SMS, voice and WhatsApp alerts are implemented. On every hosted plan they are also impossible
to use:

1. **Plans give 0.** The SaaS defaults set `MaxSmsPerMonth`, `MaxCallsPerMonth` and
   `MaxWhatsappPerMonth` to 0 (`server/internal/entitlements/defaults.go:93-98`, applied
   `:166-168`). A field billing leaves nil keeps that default (`entitlements/service.go:574,
   590-598`). Billing cannot send these fields: its struct only has `maxChecks`,
   `maxSsoUsers`, `maxChecksPerMinute`, `maxDeportedAgents` and `maxCustomDomains`
   (`solidping-billing/server/internal/entitlements/entitlements.go:23-43`). Free, Starter
   (€5), Pro (€15) and Scale (€45) all end up at 0
   (`solidping-billing/server/internal/plans/plans.go:89-175`).
2. **Bring-your-own is blocked by the same 0. This is a bug.** An org with its own Twilio
   integration pays for its own messages. The design says such a send skips the instance-spend
   guards and only keeps the per-org hourly runaway guard
   (`server/internal/jobs/jobtypes/job_escalation_step.go:820-825`, `usage_reserve.go:15-19`).
   But `reservePhoneChannel` (`job_escalation_step.go:858-895`) calls `ReserveSMS` /
   `ReserveCall` for every send. Those go through `reserveUsage` (`usage_reserve.go:81-114`),
   which also applies the monthly cap and refuses at 0 (`:100-101`). So a hosted org that
   brings its own Twilio gets `sms_quota_exhausted` on every page.
3. **Usage is invisible.** `Usage` exposes only `WhatsappThisMonth`
   (`entitlements/usage.go:39`). The Usage page shows WhatsApp only
   (`web/dash0/src/routes/orgs/$org/organization.usage.tsx:199-201`).
4. **A dropped page is silent.** Past the cap, the send is dropped with an info log and a skipped
   `incident_notifications` row (`job_escalation_step.go:884-889, 903-917`). Nobody is told
   the org has run out.

Every hosted comparison against BetterStack, UptimeRobot, Pingdom or StatusCake scores this as
a loss (`wiki/competitors/comparison/features.md:106-107,131-137`). StatusCake gives 75 SMS on
Superior, Pingdom 50 on its lowest tier, Pulsetic 30 then $0.10 each
(`comparison/pricing.md:75,86,102`).

## Proposal
Three repos. Ship part A first: it is a bug fix and needs no pricing decision.

### A. Bring-your-own skips the monthly cap (solidping)
1. Split the reservation in `entitlements/usage_reserve.go`. Keep `reserveUsage` (runaway guard,
   then monthly cap) for instance-credential sends. Add a runaway-only path for
   bring-your-own sends: same bucket, no `ReserveMonthlyUsage`, no counter row.
2. `reservePhoneChannel` (`job_escalation_step.go:858`) takes the `*smssvc.Resolution` and
   picks the path per capability:
   - SMS: bring-your-own when `resolution.SMSMode == ModeBringYourOwn`.
   - Voice: bring-your-own when `resolution.VoiceMode == ModeBringYourOwn`. Modes are resolved
     per capability (`integrations/sms/resolve.go:35-41`). An org whose integration has no
     `voice_from_number` uses instance voice, and that must still consume the cap.
   - WhatsApp: always the instance path. It is instance-only through Meta Cloud
     (`config/config.go:3346-3355`).
   Put the rule in one helper on `Resolution` next to `InstanceCredentialsForSMS`
   (`resolve.go:63`), e.g. `InstanceCredentialsForVoice`, so callers never compare modes.
3. Keep the order of `job_escalation_step.go:752-763`: instance-spend guards before any
   reservation.
4. Fix the comments at `job_escalation_step.go:820-825` and `usage_reserve.go:15-19` to describe
   the new rule.

### B. Plan quotas (solidping-billing)
5. Add `maxSmsPerMonth`, `maxCallsPerMonth` and `maxWhatsappPerMonth` to billing's struct
   (`entitlements.go:23-43`) and to the push (`solidping/client.go:67-78`). The OSS decoder
   already accepts these keys (`server/internal/db/models/entitlements_payload.go:51-57`), so
   this ships without an OSS release.
6. Set them per plan in `plans.go:89-175`, using the values from Open question 1.
7. Push the new entitlements to every existing paid org once, by reusing whatever re-sync path
   billing has (see To verify). Do not wait for the next plan change.

### C. Make the quota visible (solidping)
8. Add `SmsThisMonth` and `CallsThisMonth` to `Usage` (`entitlements/usage.go:39`), read from
   `org_usage_counter` like WhatsApp. Show them on the Usage page next to WhatsApp, as used /
   cap, with `data-testid`s. Strings in `src/locales/{en,fr,de,es}/`.
9. The first time a kind is refused for the monthly cap in a month, email the org's admins
   once: which channel, the cap, and the plan that raises it. Do not send it again that month
   for that kind. Runaway-guard refusals do not send it. (Open question 3.)

### D. Copy (solidping-website, solidping wiki)
10. `solidping-website/src/pages/pricing.tsx:507` stops saying SMS, voice and WhatsApp are
    "not included in the hosted plans today". List the per-plan quotas. Keep `:168` ("SMS/voice
    via Twilio") for self-hosted.
11. Update `wiki/competitors/comparison/solidping-position.md:18-19,99` and
    `comparison/features.md:106-107,131-137`. Add a `CHANGELOG.md` entry for parts A and C.

## Tests
- `entitlements/usage_reserve_test.go`:
  - an instance send at cap 0 is refused;
  - an instance send at cap N writes the counter and the N+1th is refused;
  - a bring-your-own send at cap 0 is allowed and writes no `org_usage_counter` row;
  - a bring-your-own send still hits the hourly runaway guard: the 31st SMS in an hour is
    refused.
- `jobs/jobtypes/job_escalation_step_test.go`:
  - SaaS defaults (cap 0) plus a bring-your-own Twilio integration: SMS and voice are sent and
    no `*_quota_exhausted` row is written;
  - the same org on instance credentials: both skipped with `sms_quota_exhausted` /
    `voice_quota_exhausted` (today's behaviour);
  - mixed modes, bring-your-own SMS with instance voice: SMS sent, voice refused at cap 0;
  - WhatsApp is counted whatever the Twilio mode.
- Usage handler test: `smsThisMonth` and `callsThisMonth` appear and match the counter.
- Admin-email test: two refusals in the same month send one email; a refusal in the next month
  sends another; a runaway refusal sends none.
- solidping-billing: a plan-to-entitlements test asserting the three fields per plan, and a
  client test asserting they are in the PUT body.
- dash0 E2E (`web/dash0/e2e/`): the Usage page shows SMS and calls counters.

## To verify
- That production (`k8xp` repo, `k8s/solidping/`) has instance SMS and voice configured
  (`SP_SMS_ENABLED`, `SP_SMS_PROVIDER`, `SP_VOICE_ENABLED`, `SP_VOICE_FROM_NUMBER`), and which
  provider sends SMS. Without them, part B grants a quota nobody can use: ship A and C, and
  leave B until the instance is configured.
- The `SP_SMS_ALLOWED_COUNTRIES` value in production. A quota is only safe with a country
  allow-list: SMS pumping to premium-rate destinations is the main fraud risk.
- Whether solidping-billing has a re-sync of entitlements for existing subscriptions (step 7),
  or needs a one-shot command.
- Current per-unit prices to cross-check Open question 1: Twilio SMS to France is $0.0798
  (spec `2026-08-15-09`), OVH €0.054 to €0.060. Look up the Twilio voice price per minute to
  French mobiles and the Meta WhatsApp utility-template price for France.

## Resolved open questions
Decided by Florent on 2026-10-17.

1. **Quotas per month:** Free 0 SMS, 0 calls, 0 WhatsApp. Starter 20, 5, 20. Pro 60, 15, 60.
   Scale 200, 50, 200.
2. **Free plan:** 0 on every channel. Free orgs can bring their own Twilio, and part A makes
   that work.
3. **Admin email when a cap is hit:** yes, as in step 9. Once per kind per month.
4. **Overage:** none. The cap is a hard cap.
5. **WhatsApp:** the plan values above are set in billing. Production has no `SP_WHATSAPP_*`,
   `SP_SMS_*` or `SP_VOICE_*` config yet (only the k8xp dev overlay does, checked
   2026-10-17), so nothing is advertised until it does.

## Batch scope
This repo's batch implements parts A and C (steps 1-4 and 8-9) plus a `CHANGELOG.md` entry.
The other steps live in other repos and are not part of the batch, so the audit skips them:

- steps 5-7 (solidping-billing) are done in a separate PR in that repo;
- steps 10-11 (website pricing copy and wiki pricing claims) wait until production has instance
  SMS and voice configured. Until then, the hosted quotas exist but no provider sends them.

## Non-goals
- Per-message overage billing.
- A per-org WhatsApp provider (it stays instance-only).
- Changing plan prices.
