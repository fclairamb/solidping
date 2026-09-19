---
model: opus
effort: high
---

# Slack channel alerts don't name the on-call person unless an admin mapped them and the escalation policy's step 1 points at the schedule

## Problem

The expected behavior: when an alert is posted to the org's Slack channel and
someone is currently on call, the message says so, and it pings that person's
Slack handle when one is declared.

The mention machinery exists (spec `2026-08-12-03`, `mention_on_call` on
`SlackSettings` at `server/internal/db/models/integration.go:177`, resolution in
`server/internal/jobs/jobtypes/mentions.go:67`, rendering in
`server/internal/notifications/slack.go:419`). In practice the on-call person is
still frequently *not* named, because the current chain only fires when three
things line up at once:

1. **"On call" is derived from the escalation policy, not from the on-call
   schedule.** `resolveStepOneUsers` (`mentions.go:102`) walks check → effective
   policy (`escalation_policy_resolve.go:32`, check → group → org default) →
   *step 1* targets. An org that has an on-call schedule but whose check resolves
   to no policy, or to a policy whose step 1 is a Slack connection with the
   schedule at step 2, gets no mention at all: the person actually on call is
   never looked up.

2. **On `incident.escalated`, the wrong step is named.** The escalation job
   enqueues the channel notification with only `ConnectionUID` / `IncidentUID`
   / `EventType` (`job_escalation_step.go:416`, config struct at
   `job_notification.go:39`). `ResolveOnCallMentions` then names step-1 humans
   again, even when it is step 2 or 3 that just fired and pages someone else.
   A mention that disagrees with who is being paged is what the resolver's own
   comment says is "worse than no mention at all".

3. **A handle the member declared themselves is ignored.** The only identity
   source is `user_integration_identities`
   (`buildMentionTargets`, `mentions.go:209` →
   `GetUserIntegrationIdentity`), which is written exclusively from the admin
   side: `users.lookupByEmail` auto-match
   (`server/internal/handlers/integrations/identities.go:404`) or an admin's
   manual pick (`identities.go:583`, "Member mapping" section at
   `web/dash0/src/components/integrations/integration-form.tsx:1879`). A member
   who signed in with Slack (user auth provider, `provider_id` = Slack user id,
   `server/internal/db/models/auth.go:199`) or who added a `slack_user` contact
   from Account → Notifications (`SlackConnectRow`,
   `web/dash0/src/routes/orgs/$org/account.notifications.tsx:552`, value =
   Slack user id via `usernotifications/service.go:253`) has declared exactly
   the handle we need, and it is never consulted. If their Slack email differs
   from their SolidPing email and no admin mapped them, they render as a
   plain-text name and nobody is pinged. There is also no member-facing way to
   see or fix "how I appear in channel alerts".

Two smaller points, same feature:

- The line reads `<@U1> — you are on call for this.` When the person has no
  handle it becomes `Alice — you are on call for this.`, which reads like a
  message *to* Alice rather than telling the channel *who* is on call.
- `mention_on_call` is off for every Slack integration created before
  2026-08-12 (explicit decision in the done spec, no backfill). Any org from
  before that date sees none of this until an admin finds the switch at
  `integration-form.tsx:1773`. Not a bug, but it is the first thing to check
  when reproducing.

## Proposal

Keep the safety properties of the existing resolver (nil on every uncertain
case, recover(), never fail a send) and fix the three gaps in place.

### 1. Name whoever is on call, following the step that actually fired

- Extend `NotificationJobConfig` with optional `stepUid` (and `repeatIndex`,
  both already in hand at `job_escalation_step.go:416` as `r.config.StepUID` /
  `r.config.RepeatIndex`). When set, `ResolveOnCallMentions` resolves the
  targets of *that* step instead of step 1. `incident.created` keeps step 1.
- When the check resolves to a policy but the fired step has no human target,
  fall back to the first step that names a `schedule` or `user` target, so a
  "step 1 = Slack channel, step 2 = on-call schedule" policy still names the
  schedule's current on-call on `incident.created`. Keep the deduplication and
  display-name ordering exactly as today.
- Out of scope: inventing an on-call person for a check with no effective
  policy. An org can have several schedules and nothing ties a schedule to a
  check except a policy target, so guessing would name the wrong person. Note
  this as a follow-up (an org-level "default on-call schedule") rather than
  building it here.

### 2. Honor a self-declared Slack identity

Identity resolution order in `buildMentionTargets`, per user:

1. `user_integration_identities` row for this integration (unchanged, admin
   mapping always wins).
2. The user's `slack_user` contact whose workspace matches the integration's
   `team_id`.
3. The user's Slack auth provider (`provider_id` = Slack user id) when the org's
   Slack sign-in provider is the same workspace as the integration
   (`auth.go:15` carries the team id).

For 2 the contact must know its workspace. `user_contacts.slack_user` stores
only the user id today (the original spec flagged this). Add a nullable
`team_id` (Postgres + SQLite migrations in lockstep, `sync-pg-to-sqlite`
skill), populate it on creation from the `SlackSuggestion` path (the suggestion
already knows `settings.TeamID` of the channel it is bound to), and treat a
contact with a NULL `team_id` as "unknown workspace": use it only when the org
has exactly one Slack integration. Never cross workspaces.

Make the admin view agree with what the sender does: `SyncIdentities` seeds
`source: auto` rows from sources 2 and 3 as well, so the "Member mapping"
section shows these members as matched instead of "not found". Manual rows are
never overwritten (existing rule at `identities.go:386`).

Member-facing: on Account → Notifications, extend the existing Slack row to
show "Mentioned in channel alerts as @handle" / "Not linked, ask an admin or
sign in with Slack". Read-only for the member; the write paths stay the ones
above. Start from the design reference as for any UI change.

### 3. Wording

Render the line as `On call: <@U1>` (several: `On call: <@U1>, <@U2>`; no
handle: `On call: Alice`). Same block position (`prependMentionBlock`,
`slack.go:461`), still only on `incident.created` / `incident.escalated`, still
absent when there is nothing to say so untouched messages stay byte-identical.
Update the fixtures in `mentions_test.go` and the Slack sender tests
accordingly. Discord shares `MentionTarget`; keep its rendering
(`discord.go:449`) unchanged unless the change is trivially the same.

### Testing

- `mentions_test.go`: table-driven cases for step-aware resolution (created →
  step 1; escalated with `stepUid` → that step; fired step with no humans →
  first human step; no policy → nil), plus a negative control proving that a
  schedule at step 2 is *not* named when step 1 already has a human target.
- Identity fallback: identity row wins over contact; contact used only when
  `team_id` matches (negative control: a contact from another workspace yields
  plain text); NULL `team_id` used only with a single Slack integration;
  auth-provider fallback with matching / non-matching team.
- Slack sender: rendering of the new `On call:` line with 0, 1, 2 targets and
  with a target lacking a handle.
- Playwright: the Account → Notifications Slack row states.

## Open questions

- Should the `mention_on_call` default flip to on for pre-2026-08-12
  integrations now that the feature has been out for a month? The done spec
  chose no backfill; this spec does not change that unless told to.
- Is naming the current on-call for a check with *no* effective policy wanted
  (org-level default schedule)? Left out here, see §1.

## Implementation Plan

### §1 — step-aware on-call resolution (`server/internal/jobs/jobtypes/`)

1. `NotificationJobConfig` gains optional `stepUid` / `repeatIndex` (JSON
   `omitempty`). `job_escalation_step.go`'s `enqueueNotificationFor` fills both
   from `r.config`. `incidents/service.go` (incident.created) leaves them empty.
2. `ResolveOnCallMentions` takes the config's `stepUid`. `resolveStepOneUsers`
   becomes `resolveStepUsers(ctx, …, stepUID)`:
   - load the effective policy (unchanged `ResolveEscalationPolicyUID`),
   - pick the *fired* step when `stepUID` names one belonging to that policy,
     else the lowest-position step,
   - resolve its targets to humans (unchanged `resolveTargetUser`),
   - when that yields nobody, walk the remaining steps in position order and
     take the first that names a `schedule` or `user` target which resolves.
   Deduplication and display-name ordering unchanged. Every failure still
   yields nil.
3. Out of scope, recorded as a follow-up: naming an on-call for a check with no
   effective policy (needs an org-level default schedule).

### §2 — self-declared Slack identity

4. Migration `022_v0_30_0` (Postgres + SQLite, lockstep): nullable
   `user_contacts.team_id text`. Model gains `TeamID *string`; both
   `UpsertUserContact` implementations keep an existing value with
   `coalesce(excluded.team_id, user_contacts.team_id)`.
5. `usernotifications.Service.CreateContact` stamps `team_id` from the org's
   Slack channel settings (`GetSlackChannelForOrg` → `SlackSettings.TeamID`)
   when the contact type is `slack_user`.
6. New package `server/internal/identitylink`: `DeclaredSlackIdentity(ctx,
   dbSvc, integration, userUID)` returns the Slack user id a member declared
   themselves — (a) a `slack_user` contact whose `team_id` equals the
   integration's, (b) a `slack_user` contact with a NULL `team_id` **only** when
   the org has exactly one live Slack integration, (c) the user's Slack auth
   provider when the org's Slack `organization_providers` row carries the same
   team id. Never crosses workspaces; nil on every uncertain case.
7. `buildMentionTargets` consults it only when there is no
   `user_integration_identities` row (admin mapping always wins).
8. `SyncIdentities`' Slack resolver falls back to the same helper when the
   email lookup misses, so the admin "Member mapping" table agrees with the
   sender. `manual` rows are still skipped before any lookup.
9. `GET …/notification-routes` response gains `slackMention`
   (`linked`, `externalId`, `workspace`), resolved with the same helper plus the
   identity row, for the member-facing read-only line.

### §3 — wording + frontend

10. `renderMentions` emits `On call: <@U1>, <@U2>` (comma-separated), plain name
    when a target has no handle, `""` when there is nothing to say so
    mention-free messages stay byte-identical. Discord unchanged.
11. Account → Notifications: the existing Slack row shows "Mentioned in channel
    alerts as @handle" or "Not linked — ask an admin, or sign in with Slack".
    Read-only. Keys added to `en`/`fr`/`de`/`es`.

### Tests

12. `mentions_test.go`: step-aware table (created → step 1; escalated with
    `stepUid` → that step; fired step with no humans → first human step; no
    policy → nil) + negative control (schedule at step 2 NOT named when step 1
    already has a human).
13. `mentions_identity_test.go`: identity row beats contact; contact only on a
    team match (negative control: foreign workspace → plain text); NULL team id
    only with a single Slack integration; auth provider with matching and
    non-matching team.
14. `slack_mentions_test.go`: `On call:` with 0/1/2 targets and with a
    handle-less target, plus a byte-identical assertion for a mention-free
    message.
15. Playwright: the Account → Notifications Slack row states.
