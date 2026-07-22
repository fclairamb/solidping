---
model: opus
effort: xhigh
---

# No alert channel can reliably wake someone up (SMS / voice call)

## Problem

SolidPing ships the full paging brain — escalation policies with
repeats-until-ack, on-call rotations, snooze, acknowledgment — but the terminal
channels are email, Slack, Discord, Google Chat, Mattermost, ntfy, Opsgenie,
Pushover, webhooks and web push
([integration.go:16-28](server/internal/db/models/integration.go:16)). None of
these reliably wakes a sleeping on-call engineer, and the one "real pager"
escape hatch, Opsgenie, is being sunset by Atlassian. Every competitor treats
SMS/voice as the defining feature of paid alerting tiers.

The codebase is already half-scaffolded for this:

- The severity channel tokens `"sms"`, `"voice"`, `"critical_push"` are
  accepted by validation
  ([severities/service.go:46-55](server/internal/handlers/severities/service.go:46))
  and seeded on the default `critical` severity
  ([severity.go:111](server/internal/db/models/severity.go:111)) — but nothing
  downstream honors them.
- `UserContactTypePhone = "phone"` exists on the org-scoped contact model with
  a `VerifiedAt` field
  ([user_contact.go:13,32](server/internal/db/models/user_contact.go:13)), and
  the escalation dispatcher has a literal stub: `case
  models.UserContactTypePhone:` logs *"SMS provider not configured; skipping
  route"*
  ([job_escalation_step.go:440-446](server/internal/jobs/jobtypes/job_escalation_step.go:440)).
- The dash0 account-notifications page already renders "Phone (SMS)" contact
  UI behind a disabled placeholder *"SMS not available — requires an SMS
  provider configured by your admin"*
  ([account.notifications.tsx:137-141](web/dash0/src/routes/account.notifications.tsx:137)).

This spec fills in the sending backend (Twilio first) and wires the existing
seams together.

## Proposal

### Scope (v1)

A `twilio` connection type (org credentials), SMS + voice-call senders, verified
per-user phone contacts, severity-token dispatch for `sms`/`voice`, a
DTMF-acknowledge voice flow, signed inbound callbacks, and monthly quota
entitlements. **Out of scope**: `critical_push` / native mobile app (needs an
app + iOS critical-alert entitlement; web push
([webpush.go](server/internal/notifications/webpush.go)) remains the interim),
a SaaS-managed Twilio pool (design the client seam for it, implement with
billing later), WhatsApp, and a dedicated severity-editor UI (none exists
today; tokens are already editable via API).

### 1. Twilio connection type

- Constant `ConnectionTypeTwilio ConnectionType = "twilio"` in
  [integration.go:16-28](server/internal/db/models/integration.go:16). The
  `CapabilitiesFor` default branch already yields `CanNotify: true`
  ([integration.go:52](server/internal/db/models/integration.go:52)) — no
  change needed.
- **Settings** (stored on `Integration.Settings` JSONMap):
  - `account_sid` (public), `auth_token` (**secret**), `from_number` **or**
    `messaging_service_sid` (public), `voice_from_number` (public, optional —
    voice disabled when absent), `to_numbers` `[]string` (optional: shared
    recipients for direct-channel sends, e.g. a team phone).
  - Secret split: add `ConnectionTypeTwilio: {"auth_token"}` to
    `connectionSecretFields`
    ([conn_secrets.go:27-39](server/internal/crypto/credentials/conn_secrets.go:27)).
    Encryption/decryption then rides the existing envelope — senders always see
    plaintext because `job_notification.go` decrypts + merges before dispatch
    ([job_notification.go:96-115](server/internal/jobs/jobtypes/job_notification.go:96)).
- **Validation**: add `twilio` to the type switch in
  [integrations/service.go:377-387](server/internal/handlers/integrations/service.go:377)
  plus per-type checks (SID format `AC…`, token non-empty, exactly one of
  `from_number` / `messaging_service_sid`, E.164 for all numbers —
  `^\+[1-9]\d{6,14}$`, no new dependency).
- **Client**: new package `server/internal/integrations/twilio` with
  `NewClientWithBaseURL(accountSID, authToken, baseURL)` exactly like the
  Slack client seam
  ([slack/client.go:69-77](server/internal/integrations/slack/client.go:69));
  real API base as a default const. Methods: `SendSMS(to, from/mss, body,
  statusCallback)` and `CreateCall(to, from, twimlURL)` (form-encoded POSTs).

### 2. Direct-channel sender (registry path)

- `TwilioSender` in `server/internal/notifications/twilio.go` implementing
  `Sender.Send(ctx, jctx, payload)`
  ([sender.go:42-47](server/internal/notifications/sender.go:42)), registered
  as a new case in `GetSender`
  ([registry.go:19-42](server/internal/notifications/registry.go:19)).
- Behavior mirrors [pushover.go](server/internal/notifications/pushover.go):
  parse settings from `payload.Integration.Settings`, switch on the four
  `EventType`s, send one SMS per entry in `to_numbers` (no-op with a clear
  error if empty). Return transient errors compatible with `IsNetworkError`
  ([sender.go:50-79](server/internal/notifications/sender.go:50)) for retry.
- SMS body (aim ≤ 160 GSM-7 chars):
  `"[SolidPing] {org}: {check} DOWN — ack: {shortAckURL}"` using
  `payload.AppBaseURL` + the signed ack-link pattern (`buildAckURL`,
  [email.go:328](server/internal/notifications/email.go:328)).
- Set `payload.MessageID` from the Twilio SID so the
  `incident_notifications` audit row carries it
  ([sender.go:16-39](server/internal/notifications/sender.go:16),
  [job_notification.go:223-246](server/internal/jobs/jobtypes/job_notification.go:223)).
- This automatically enables: per-check broadcast fan-out via
  `check_connections`, escalation `connection` targets, and the existing
  `TestIntegration` synchronous test-send
  ([integrations/handler.go:260](server/internal/handlers/integrations/handler.go:260)).

### 3. Severity-token dispatch (the correctness-critical piece)

Today `fanOutWithSeverity`
([job_escalation_step.go:181-231](server/internal/jobs/jobtypes/job_escalation_step.go:181))
gates **all** user/schedule/all_admins targets on `filter["email"]` alone
(lines 202, 211, 220), and `connectionPassesSeverityFilter` matches
`filter[string(conn.Type)]` (`:280`). Changes:

- **Person targets**: replace the `filter["email"]` gate with
  `severityAllowsPersonTargets(filter)` = filter is nil OR contains any of
  `{"email", "sms", "voice", "push", "critical_push"}`. Then pass the filter
  down into `pageUser` / `pageSchedule` / `pageAllAdmins` →
  `dispatchRoute` (`:386-454`) and filter **per route** with this token map:

  | route/contact type | included when filter contains        |
  |--------------------|--------------------------------------|
  | `email`            | `email` (unchanged)                  |
  | `slack_user`       | `email` (unchanged, historical)      |
  | `webpush`          | `email` (compat) or `push`/`critical_push` |
  | `phone`            | `sms` → send SMS; additionally `voice` → place call |

  `nil` filter keeps meaning "everything" (current behavior). Backward
  compatibility must be proven by tests: a severity of `{email, slack}`
  behaves byte-for-byte as today.
- **Connection targets**: extend `connectionPassesSeverityFilter` so a
  `twilio` connection passes when the filter contains `"sms"` or `"voice"`
  (its type string is not a severity token).
- **Phone route dispatch** (replacing the stub at `:440-446`): resolve the
  org's default enabled `twilio` integration; decrypt settings with the same
  `DecryptForOrg` + `MergeConfig` dance as
  [job_notification.go:96-115](server/internal/jobs/jobtypes/job_notification.go:96);
  send to the contact's number **only if `VerifiedAt` is set**; place a voice
  call when the filter includes `voice` and `voice_from_number` is configured.
  No default twilio connection → keep today's info-log skip.
- Existing guards stay load-bearing: `incidentNeedsPaging` (`:149-158`) skips
  acked/resolved/snoozed incidents, and ack cancels pending jobs via
  `CancelPendingForIncident`
  ([jobsvc/service.go:399-449](server/internal/jobs/jobsvc/service.go:399)) —
  no SMS/call fires after an ack.

### 4. Phone verification flow

Phone contacts are already created unverified (auto-verify covers only
email/webpush,
[usernotifications/service.go:196-201](server/internal/handlers/usernotifications/service.go:196)).
Add a code flow modeled on the subscriber-token + TOTP-confirm patterns:

- Columns on `user_contacts` (consolidated release migration, currently
  `008_v0_7_0`, in **both** `server/internal/db/postgres/migrations/` and
  `server/internal/db/sqlite/migrations/`; share the file with spec
  `2026-07-22-01` if same release):

  ```sql
  ALTER TABLE user_contacts ADD COLUMN verify_code_hash varchar(64);
  ALTER TABLE user_contacts ADD COLUMN verify_expires_at timestamptz;
  ALTER TABLE user_contacts ADD COLUMN verify_attempts smallint NOT NULL DEFAULT 0;
  ```

- Endpoints on the existing group
  `/orgs/:org/users/me/notification-contacts`
  ([server.go:935-940](server/internal/app/server.go:935)):
  - `POST /:uid/verify` — generates a 6-digit code (crypto/rand), stores
    SHA-256 hash + 10-min expiry, sends it via the org's default twilio
    connection. Rate-limit 3 sends/hour/contact; error `VALIDATION_ERROR` if
    no provider configured.
  - `POST /:uid/verify/confirm` `{code}` — constant-time compare, max 5
    attempts then invalidate, success stamps `VerifiedAt` and clears the
    verify columns.

### 5. Voice call flow (DTMF acknowledge)

- `dispatchRoute` places the call with
  `twimlURL = Server.BaseURL + "/api/v1/integrations/twilio/voice?cid={integrationUid}&token={ackToken}"`
  — `Server.BaseURL` is the existing public-URL config
  ([config.go:588](server/internal/config/config.go:588), default
  `http://localhost:4000`, `:700`; dev uses the `solidping.k8xp.com` tunnel).
  `ackToken` reuses the **existing signed ack-JWT** already used by
  `AcknowledgeIncidentByLink` / `VerifyAckToken`
  ([incidents/handler.go:199-250](server/internal/handlers/incidents/handler.go:199)).
- New public route group mirroring the Slack inbound pattern
  ([server.go:1191-1197](server/internal/app/server.go:1191)) with a
  `VerifyMiddleware` that loads the integration from `cid`, decrypts
  `auth_token`, and validates `X-Twilio-Signature` (HMAC-SHA1 over URL +
  sorted form params); invalid → 403, no body detail.
  - `POST /api/v1/integrations/twilio/voice` → verifies `token`, responds
    TwiML: `<Say>` org + check + state (twice), `<Gather numDigits="1"
    action=".../voice/gather?cid=…&token=…">` "Press 4 to acknowledge".
  - `POST /api/v1/integrations/twilio/voice/gather` → digit `4` calls a new
    `AcknowledgeIncidentFromPhone(ctx, orgUID, incidentUID, phone)` modeled on
    `AcknowledgeIncidentFromSlack`
    ([incidents/service.go:2218](server/internal/handlers/incidents/service.go:2218))
    with `Via: "phone"` and the phone number recorded on the event payload
    (mirroring `acknowledged_by_email` attribution); responds `<Say>`
    confirmation. The normal ack path then cancels all pending escalation
    jobs.
  - `POST /api/v1/integrations/twilio/status` — SMS/call delivery status
    callback; updates the `incident_notifications` row's `DeliveryDetails` by
    `MessageID`. Should-have; ship behind the same middleware.

### 6. Quotas & runaway protection

There is **no persistent monthly-counter pattern today** (quotas are live DB
counts or in-memory token buckets,
[entitlements/service.go:220-248](server/internal/entitlements/service.go:220),
[usage.go:106-169](server/internal/entitlements/usage.go:106)) — this is
net-new:

- New table (same consolidated migration, both engines):

  ```sql
  CREATE TABLE org_usage_counters (
    organization_uid varchar(36) NOT NULL,
    kind varchar(32) NOT NULL,          -- 'sms' | 'voice'
    period_start date NOT NULL,         -- first day of UTC month
    count integer NOT NULL DEFAULT 0,
    PRIMARY KEY (organization_uid, kind, period_start)
  );
  ```

  Reserve-then-send: dialect-aware conditional upsert
  (`INSERT … ON CONFLICT DO UPDATE SET count = count + 1 WHERE count < ?
  RETURNING count`; supported by both PG and modern SQLite — implement in both
  `db/postgres` and `db/sqlite` services). No row returned → quota exhausted →
  skip send with a `QuotaError`-style audit entry, never block the rest of the
  escalation step.
- Entitlements: `MaxSmsPerMonth *int` / `MaxCallsPerMonth *int` on
  `EntitlementLimits`
  ([entitlements_payload.go:37-44](server/internal/db/models/entitlements_payload.go:37))
  + the strict wire struct in `UnmarshalJSON` (`:60-85`) + `overlayLimits`
  ([entitlements/handler.go:304-317](server/internal/handlers/entitlements/handler.go:304))
  + defaults ([defaults.go:98-137](server/internal/entitlements/defaults.go:98)):
  SaaS `Int(0)` (billing raises per plan), self-hosted `nil` (unlimited —
  bring-your-own Twilio).
- Runaway guard independent of entitlements: per-org in-memory token bucket
  (reuse the `limiterFor` bucket shape from
  [entitlements/service.go:226-241](server/internal/entitlements/service.go:226)),
  e.g. 30 SMS + 10 calls per org per hour, config-overridable.
- Coalescing: at most one SMS and one call per (incident, phone number,
  escalation-step job) — natural since steps are discrete jobs; dedupe within
  a single job run when a user matches via both `user` and `all_admins`
  targets (the existing fan-out already dedupes per target list; verify and
  test).

### 7. Dashboard (dash0)

- **Integrations**: new `case "twilio"` in the per-type form registry
  ([integration-form.tsx:314-432](web/dash0/src/components/integrations/integration-form.tsx:314))
  — SID, auth token (secret field), from-number / messaging-service toggle,
  voice from-number, to-numbers chip input (reuse the chip-input pattern from
  the recent HTTP-expected-status work); icon in
  [integration-icon.tsx](web/dash0/src/components/integrations/integration-icon.tsx);
  Test button already wired via `TestIntegration`.
- **Account notifications**: flip on the phone flow in
  [account.notifications.tsx](web/dash0/src/routes/account.notifications.tsx) —
  remove the disabled placeholder (`:137-141`), enable the `phone` add-contact
  type (`:245,305-307`), add the two-step verify UI (code entry, resend with
  countdown). Availability signal: expose a `smsAvailable` boolean where the
  page can read it (smallest viable: derive from the org integrations list the
  page can already query; otherwise add the flag to the org view response —
  implementer's choice, document it).
- Start from the design reference
  ([design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx));
  add missing primitives (secret input, verify-code input) to it.
- i18n: `integrations.json`, `account.json`, `escalation.json` across all four
  locales (`web/dash0/src/locales/{en,fr,de,es}/`).
- OpenAPI: hand-edit `server/internal/app/openapi/openapi.yaml` for the new
  endpoints/fields.

### Testing

- **Twilio client**: `httptest.NewServer` via the base-URL seam, asserting
  posted form fields (`To`, `From`/`MessagingServiceSid`, `Body`,
  `StatusCallback`, `Url`) — same discipline as
  [webhook_test.go:120](server/internal/notifications/webhook_test.go:120) and
  [slack/client_test.go:57](server/internal/integrations/slack/client_test.go:57).
- **Sender/registry**: `GetSender(twilio)` resolves; four event types produce
  correct bodies; empty `to_numbers` errors cleanly
  ([registry_test.go](server/internal/notifications/registry_test.go)).
- **Dispatch semantics** (the regression-sensitive part): table-driven tests on
  `fanOutWithSeverity`/`dispatchRoute` proving — severity `{sms}` pages phone
  routes and skips email; `{email, slack}` behaves exactly as today
  (backward-compat control); `{voice}` places a call without SMS; unverified
  phones are never dialed; acked incident sends nothing; missing provider
  degrades to the current log-and-skip.
- **Callbacks**: signature valid/invalid/missing, expired token, DTMF `4` acks
  the incident and `CancelPendingForIncident` fires, other digits re-prompt.
- **Verification flow**: issue/confirm/expiry/attempt-cap/rate-limit,
  constant-time compare.
- **Counters**: both engines — increment, limit boundary, month rollover,
  concurrent reserve (testcontainers PG, `_postgres_test.go` convention).
- **Entitlement wire**: `maxSmsPerMonth`/`maxCallsPerMonth` decode via the
  strict `UnmarshalJSON`, overlay, and PUT round-trip.

### Rollout order

1. Connection type + client + direct-channel sender + tests (ships value
   alone: SMS to shared `to_numbers`).
2. Phone verification flow + per-route severity dispatch.
3. Voice + callbacks + DTMF ack.
4. Quotas/entitlements + dashboard polish.

### Open questions

- Voice retry-on-no-answer: leave to escalation repeats (recommended — keeps
  one paging brain) or add a provider-level single retry?
- Should `slack_user` routes eventually get their own severity token instead
  of riding `email`? (Out of scope; note for a follow-up.)
- Short ack links: the ack JWT makes SMS URLs long — worth a `/a/:token`
  shortener route, or accept the length in v1?

## Implementation Plan

Structured along the spec's own rollout order; each phase ships value and is
committed independently.

### Phase 1 — Twilio connection type + client + direct-channel sender
- `ConnectionTypeTwilio = "twilio"` in `db/models/integration.go`; typed
  `TwilioSettings` struct + `FromJSONMap` helpers.
- `connectionSecretFields[ConnectionTypeTwilio] = {"auth_token"}` in
  `crypto/credentials/conn_secrets.go`.
- Validation case in `handlers/integrations/service.go` (accept `twilio`);
  per-type settings validation (SID `AC…`, token non-empty, exactly one of
  `from_number`/`messaging_service_sid`, E.164 numbers).
- New package `internal/integrations/twilio` — `NewClientWithBaseURL`,
  `SendSMS`, `CreateCall`, `ValidateSignature` (HMAC-SHA1). httptest tests.
- `notifications/twilio.go` `TwilioSender` (mirror pushover.go), registered in
  `registry.go`; SMS body with signed ack URL; `MessageID` from Twilio SID.
- Tests: client form fields, sender four event types, empty `to_numbers`.

### Phase 2 — Phone verification + severity-token dispatch (correctness-critical)
- Migration (append to `008_v0_7_0` both engines): `verify_code_hash`,
  `verify_expires_at`, `verify_attempts` columns on `user_contacts`.
- `user_contacts` model fields + DB service methods to set/clear verify state.
- `POST /orgs/:org/users/me/notification-contacts/:uid/verify` and
  `/verify/confirm` endpoints (6-digit crypto/rand, SHA-256 hash, 10-min
  expiry, 3 sends/hr, 5 attempts, constant-time compare). Sends via org
  default twilio.
- Severity dispatch: `severityAllowsPersonTargets(filter)`, thread filter into
  `pageUser`/`pageSchedule`/`pageAllAdmins`/`dispatchRoute`; per-route token
  map (email/slack_user/webpush/phone). `connectionPassesSeverityFilter`
  passes twilio on `sms`/`voice`. Phone route dispatch sends SMS (verified
  only) + optional call.
- Regression test: filter `{email, slack}` behaves byte-for-byte as today.

### Phase 3 — Voice call flow (DTMF ack)
- Public route group `/api/v1/integrations/twilio/{voice,voice/gather,status}`
  with `VerifyMiddleware` (loads integration by `cid`, decrypts `auth_token`,
  validates `X-Twilio-Signature`).
- TwiML `<Say>`/`<Gather>` responses; `AcknowledgeIncidentFromPhone` (Via
  "phone", phone on payload); DTMF 4 acks + cancels pending jobs; status
  callback updates `incident_notifications.DeliveryDetails` by MessageID.

### Phase 4 — Quotas & runaway protection
- Migration: `org_usage_counters` table (both engines).
- Reserve-then-send conditional upsert in postgres + sqlite services
  (`ON CONFLICT … WHERE count < ? RETURNING count`); QuotaError skip.
- Entitlements: `MaxSmsPerMonth`/`MaxCallsPerMonth` on `EntitlementLimits`
  wired through UnmarshalJSON, copy-through, overlayLimits, merge(), defaults.
- Per-org in-memory token-bucket runaway guard (reuse limiterFor shape).

### Phase 5 — Dashboard + i18n + OpenAPI
- dash0 `twilio` integration form case + icon; to-numbers chip input.
- account.notifications phone flow: remove disabled placeholder, enable phone
  add-contact, two-step verify UI; `smsAvailable` derived from org
  integrations.
- i18n keys across en/fr/de/es; OpenAPI hand-edit; design-reference primitives
  as needed. Playwright E2E for the new form/verify behavior.
