---
model: opus
effort: high
---

# Slack channel alerts don't mention the on-call person, and members have no integration identity mapping

## Problem

Alerts posted to a Slack channel (`server/internal/notifications/slack.go`) never
mention anyone: the on-call person gets no `<@USER_ID>` ping, so a channel alert
doesn't tell the responsible human it's *theirs* — even though the escalation
machinery already knows exactly who that is (check → group → org default policy
resolution in `server/internal/handlers/incidents/service.go` ~1585, on-call
resolution in `server/internal/handlers/oncallschedules/resolver.go`).

Three underlying gaps:

1. **No identity mapping.** There is no "this org member is `U123ABC` on this
   Slack workspace" record. The only Slack identity we capture is the
   `slack_user` row in `user_contacts`, created *only* if the user happens to
   sign in with Slack and accepts a suggestion banner — and it doesn't store the
   workspace (`team_id`), which is ambiguous when an org has several Slack
   integrations. Mentions need identity **per integration instance**.
2. **Admins are blind on paging coverage.** The members page
   (`web/dash0/src/routes/orgs/$org/organization.members.tsx`) shows role only.
   An admin can put someone on an on-call roster
   (`web/dash0/src/components/oncall/on-call-schedule-form.tsx`) without any
   signal that this person has zero pageable contacts beyond the silent email
   fallback in `job_escalation_step.go`.
3. **No safe admin path to provision personal targets.** Small teams want the
   admin to set up a colleague's paging. Letting admins write verified phone
   numbers directly would bypass the verification invariant (wrong-number
   SMS/voice pages, harassment vector).

What we are **not** missing: per-user contacts and routes
(`user_contacts` / `user_notification_routes`, UI at
`web/dash0/src/routes/orgs/$org/account.notifications.tsx`) already cover
"users set their own notification targets" with verification and Test buttons.
Don't rebuild that.

## Proposal

Phased; each phase lands independently.

### Phase 1 — identity mapping + on-call mention in Slack channel alerts

**New table `user_integration_identities`** (SQLite + Postgres migrations in
lockstep — see the `sync-pg-to-sqlite` skill):

- `uid`, `organization_uid`, `integration_uid`, `user_uid`, `external_id`
  (Slack user ID), `display_name`, `source` (`auto` | `manual`), timestamps.
- `UNIQUE(integration_uid, user_uid)` and `UNIQUE(integration_uid, external_id)`.
- Keep `user_contacts.slack_user` untouched — contacts stay "how to page me",
  identities are "who I am on this integration" (mentions, attribution).

**Auto-match on Slack connect + on demand.** The bot token already has
`users:read.email` (`server/internal/integrations/slack/service.go` scopes), so
call `users.lookupByEmail` for each org member — no re-consent needed. Run the
match after OAuth callback and expose an admin re-sync endpoint. Buckets:
matched (create `source: auto` identity), not found, ambiguous/multiple orgs —
surfaced in the UI, never silently guessed.

**REST** (conventions: `{ "data": [...] }`, camelCase, `$uid`):

- `GET  /orgs/:org/integrations/:uid/identities` — mapping status per member
  (matched / not found, source).
- `POST /orgs/:org/integrations/:uid/identities/sync` — re-run auto-match
  (admin).
- `PUT/DELETE /orgs/:org/integrations/:uid/identities/:userUid` — manual
  override (admin picks a workspace user from a searchable list).

**Mention in messages.** Add a `mention_on_call` boolean to `SlackSettings`
(`server/internal/db/models/integration.go`, JSON settings — no migration
needed), default **off**. When enabled, the notification job
(`server/internal/jobs/jobtypes/job_notification.go`) resolves, at send time:
check → effective escalation policy → first step's `schedule` targets (current
on-call via the resolver) and direct `user` targets → their identity rows for
this integration → prepend `<@id>` mentions to `incident.created` and
`incident.escalated` channel messages in
`server/internal/notifications/slack.go`. Resolved/reopened messages stay
mention-free. Fallbacks: user without identity → plain-text name (no ping);
no policy / no human targets → no mention. Never fail the send because mention
resolution failed.

**UI** (start from the design reference, as always):

- Slack panel in
  `web/dash0/src/components/integrations/integration-form.tsx`: a "Member
  mapping" section (matched / not found buckets, re-sync button, manual
  override) + the "Mention the on-call person in alerts" switch.

### Phase 2 — paging-coverage visibility

- **Members page**: add a coverage column — small channel icons
  (verified/unverified state), explicit "email fallback only" state. Data via a
  members-list extension or a dedicated admin endpoint aggregating
  `user_notification_routes` per member (routes themselves are currently
  `users/me`-scoped; expose only channel *types* + verified flags to admins,
  never values like phone numbers).
- **On-call schedule form/detail + escalation policy editor**
  (`web/dash0/src/routes/orgs/$org/escalation-policies.*.tsx`): warning badge
  next to any rostered member / `user` target whose only route is the email
  fallback — "on-call but only reachable by email".
- **Account notifications page**: promote Slack from the post-sign-in
  suggestion banner to a first-class "Connect Slack" row alongside Telegram and
  browser push, showing the linked workspace name.

### Phase 3 — admin pre-provisioning (kept minimal)

- Admin can **add** a phone/WhatsApp contact for a member in **unverified**
  state ("Set up paging…" row action on the members page); it becomes pageable
  only after the member completes the existing verification flow. Admin can
  also trigger a "set up your paging" nudge email.
- **Invariant: an admin can never create or flip a contact to verified.**
- Teams mentions (`msteams-bot`, AAD object IDs via bot roster) are explicitly
  **out of scope** — the identity table is designed to accommodate them later.

### Testing

- Backend: table-driven tests for the email auto-match buckets, mention
  resolution (policy chain, schedule resolver, missing identity fallback), and
  the admin-cannot-verify invariant — including negative tests proving no
  mention is emitted when the toggle is off or no identity exists.
- Playwright: Slack member-mapping section, coverage column, on-call warning
  badge.

### Open questions

- Default `mention_on_call` to on for *newly created* Slack integrations? (Off
  for existing either way.)
- When several people are pageable at step 1, mention all or only schedule
  resolutions? (Spec default: all step-1 human targets, deduplicated.)

## Resolved open questions

Answered by the author on 2026-08-12. These are directives — implement them as
written, do not re-litigate.

**Q: Default `mention_on_call` to on for *newly created* Slack integrations?
(Off for existing either way.)**

**Decision: yes — ON for newly created Slack integrations, OFF for existing
ones.** The `mention_on_call` field on `SlackSettings` defaults to `false` at
the struct/JSON level, so every integration already stored (whose settings JSON
has no such key) keeps decoding to `false` and behavior is unchanged for them.
The *creation path* for a new Slack integration must explicitly set
`mention_on_call: true` when the admin does not specify a value, and the Slack
panel in `integration-form.tsx` must show the switch pre-enabled when creating
a new integration. Do not backfill or migrate existing rows.

**Q: When several people are pageable at step 1, mention all or only schedule
resolutions?**

**Decision: mention ALL step-1 human targets, deduplicated.** Resolve the
effective escalation policy's first step, take both its `schedule` targets
(resolved to the current on-call person via the on-call resolver) and its direct
`user` targets, union them, and deduplicate by user uid before rendering
mentions. Order the mentions deterministically (e.g. by resolved display name)
so message content is stable and testable. Users with no identity row for this
integration degrade to plain-text names as already specified; users with an
identity render as `<@id>`.

## Resolved scope

**Decision: implement all three phases (Phase 1, Phase 2 and Phase 3) in this
spec.** The "each phase lands independently" note describes commit/PR
granularity, not a reduced scope — do not descope to Phase 1. Implement the
phases in order, committing each phase's work separately, and make sure the
Phase 3 invariant ("an admin can never create or flip a contact to verified")
is covered by an explicit negative test. Teams mentions remain out of scope as
the spec states.

## Implementation Plan

Three phases, committed separately. Backend first in each phase, then REST, then dash0.

### Phase 1 — identity mapping + on-call mention in Slack channel alerts

1. **Model** — `server/internal/db/models/user_integration_identity.go`:
   `UserIntegrationIdentity` (`uid`, `organization_uid`, `integration_uid`,
   `user_uid`, `external_id`, `display_name`, `source`, timestamps) plus
   `IdentitySourceAuto` / `IdentitySourceManual` and a constructor.
2. **Migration** — scratch migration `011_user_integration_identities.up.sql` /
   `.down.sql` in BOTH `server/internal/db/postgres/migrations/` and
   `server/internal/db/sqlite/migrations/` (highest existing number today is
   `010_v0_10_0`). Unique indexes on `(integration_uid, user_uid)` and
   `(integration_uid, external_id)`.
3. **db.Service** — `ListUserIntegrationIdentities`,
   `GetUserIntegrationIdentity`, `UpsertUserIntegrationIdentity`,
   `DeleteUserIntegrationIdentity`, implemented for postgres + sqlite.
4. **SlackSettings.MentionOnCall** (`mention_on_call`) — `false` at the
   struct/JSON level so every stored integration is unchanged; the CREATE path
   in `integrations.Service.CreateIntegration` sets it to `true` when a new
   Slack integration does not specify it. No backfill.
5. **Slack client** — `LookupUserByEmail` (`users.lookupByEmail`, form-encoded)
   returning found / not-found without erroring on `users_not_found`.
6. **Identity service** (`server/internal/handlers/integrations/identities.go`)
   — `ListIdentities` (per-member mapping status), `SyncIdentities` (buckets:
   matched / notFound / ambiguous, never silently guessed; `manual` rows are
   never overwritten by `auto`), `SetIdentity`, `DeleteIdentity`.
7. **REST** — `GET /orgs/:org/integrations/:uid/identities`,
   `POST …/identities/sync`, `PUT`/`DELETE …/identities/:userUid` (admin for
   the writes). OpenAPI + `wiki/api-specification/` updated.
8. **Auto-match on connect** — best-effort sync after the Slack OAuth callback.
9. **Mention resolution** — `ResolveEscalationPolicyUID` moved to `jobtypes`
   (incidents delegates), new `jobtypes/mentions.go` resolving check →
   effective policy → first step → `schedule` targets (via the on-call
   resolver) + direct `user` targets, deduplicated by user uid and ordered by
   display name; each user maps to its identity row for this integration.
   `Payload.OnCallMentions` carries them; `slack.go` prepends the mention line
   to `incident.created` and `incident.escalated` only. Every failure path is
   swallowed (logged) so a mention never fails a send.
10. **dash0** — Slack panel in `integration-form.tsx`: "Mention the on-call
    person in alerts" switch (pre-enabled on create) + "Member mapping" section
    (matched / not-found buckets, re-sync button, manual override picker,
    `Trash2` destructive clear). i18n in en/fr/de/es.

### Phase 2 — paging-coverage visibility

1. **Admin coverage endpoint** — `GET /orgs/:org/members/coverage` aggregating
   `user_notification_routes` per member. Exposes only channel *type* +
   `verified` + `enabled`, never contact values.
2. **Members page** — coverage column with small channel icons and an explicit
   "email fallback only" state.
3. **On-call schedule form/detail + escalation policy editor** — warning badge
   next to any rostered member / `user` target reachable only by the email
   fallback.
4. **Account notifications page** — first-class "Connect Slack" row showing the
   linked workspace name (promoted from the post-sign-in suggestion banner).

### Phase 3 — admin pre-provisioning

1. `POST /orgs/:org/members/:uid/contacts` (admin) creates a phone/WhatsApp
   contact for another member in **unverified** state, plus its route.
   Invariant enforced in the service: the admin path can never set
   `verified_at` and can never flip an existing contact to verified.
2. `POST /orgs/:org/members/:uid/paging-nudge` (admin) sends the "set up your
   paging" email.
3. **Members page** — "Set up paging…" row action navigating to
   `/orgs/$org/organization/members/$userUid/paging` (a route, not a modal).

### Testing

- Backend table-driven tests: auto-match buckets (matched / not found /
  ambiguous / manual-preserved); mention resolution (policy chain, schedule
  resolver, direct user targets, dedup + ordering); negative tests — toggle
  off ⇒ no mention, no identity ⇒ plain-text name and no `<@`, resolved and
  reopened messages mention-free, resolution failure never fails the send;
  admin-cannot-verify invariant (create unverified, and cannot flip verified).
- Playwright: Slack member-mapping section, members coverage column, on-call
  warning badge.
