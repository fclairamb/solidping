---
model: opus
effort: high
pr: batch
automerge: false
---

# New users without an org get stuck on /no-org: create a personal org for them

## Problem

A user who signs up or signs in and belongs to no organization lands on
`/no-org` (`LoginActionNoOrg`, `server/internal/handlers/auth/service.go:447`).
That page already offers a pre-filled personal org as its main action, plus a
secondary "request to join by slug" (`web/dash0/src/routes/no-org.tsx`), but
users still stall there: "what is an org, which slug?" is a question a new user
cannot answer, and every extra step before the first check loses some of them.

The no-org outcome comes from several places, all of which must behave the same:
- password login: `resolveDefaultOrgForLogin` / `resolveFromMemberships`
  (`service.go:1053`, `:1073`, and `:1130`-`:1146`);
- registration confirmation, after `autoJoinMatchingOrgs`
  (`service.go:2737`-`:2752`);
- provider (SSO/social) logins through `JoinOrgViaLogin` / `pendingSession`
  (`server/internal/handlers/auth/join_policy.go:190`, `:536`);
- the hand-off exchange (`handoff_exchange.go:82`) and org deletion
  (`org_delete.go:209`).

Discord and Slack logins already auto-create an org
(`discord_service.go:358`, `slack_service.go:388` `findOrCreateOrganization`),
so the codebase already does this for some entry points.

## Proposal

1. **Resolution order at first sign-in** (no membership yet). Try, in order:
   1. a pending invitation for the email (`findOrgInvitationForEmail`,
      `join_policy.go:598`);
   2. an org whose `registration_email_pattern` matches (`autoJoinMatchingOrgs`,
      `service.go:2801`);
   3. a pending membership request by this user;
   4. only then: **auto-create a personal org**.
   Steps 1-3 keep today's behaviour exactly. Put the decision in one helper
   (e.g. `ensurePersonalOrg(ctx, user)`) called from every no-org site listed
   above, so the paths cannot drift.

2. **Auto-creation** reuses `Service.CreateOrg` (`service.go:3484`) with no slug.
   It is named like the `/no-org` suggestion (`createOrg.suggestedPersonal`:
   "{firstName}'s org", localized). It gets a slug from `orgslug.GenerateUnique`
   with the first name as `SlugBase`, and the user as OWNER. The login then
   completes as `LoginActionDefault` into that org: no form, no `/no-org`.
   Mark the org as auto-created personal (a column or org parameter, e.g.
   `organizations.auto_personal`) so the cleanup (step 5) and the banner (step 4)
   can target it.

3. **Gate.**
   - New instance setting `auth.auto_personal_org` (`SP_AUTH_AUTO_PERSONAL_ORG`,
     in `server/internal/config/config.go`).
   - Default `true` when `SP_DEPLOYMENT_MODE=saas`, `false` for self-hosted.
   - It must respect any existing restriction on who may create orgs: if a
     user could not create one by hand, none is created for them.
   - When off, today's `/no-org` flow is unchanged.

4. **Colleague hint.** When the email's domain is not webmail
   (`isFreeMailDomain`, `autojoin_regex.go`) and existing members of exactly one
   org share it, still create the personal org. Then show a dismissable banner
   in dash0: "People from acme.com use SolidPing in *their org*: request to
   join". It reuses the membership-request mutation of `no-org.tsx`.
   - Expose only the org slug/name, and only for an org that allows join
     requests (`loginJoinRequestSuppressedReason`, `join_policy.go:312`).
     Otherwise show nothing: the hint must not reveal which private orgs exist.

5. **Abandoned personal orgs.** Seed nothing heavy at auto-creation (no sample
   checks, no paid-tier entitlements beyond the SaaS free default).
   - A periodic job flags auto-created personal orgs that still have zero checks
     and a single member after 23 days, and emails the owner a warning.
   - It soft-deletes them at 30 days, through the same path as a user-initiated
     org deletion.
   - An org that ever had a check, a second member or a paid entitlement is
     never touched.

6. **Never auto-join a shared org by a match-all pattern.**
   `validateAutoJoinRegex` (`autojoin_regex.go:97`) stays as it is. This spec
   only creates *personal* orgs.

7. **Measure first.** Before shipping the auto-creation, log/count `/no-org`
   outcomes (landed, created, join requested, left) so the effect is visible.
   Use the existing analytics/metrics path, whichever dash0 already uses.

8. Docs: a short section in `web/docs/` on sign-up and organizations (the
   setting, the 30-day cleanup). `CHANGELOG.md` entry.

## Tests

- `server/internal/handlers/auth`: table-driven over the no-org entry points
  (password login, registration confirm, provider login):
  - invite pending: joins the invited org, **no** personal org created;
  - pattern match: joins the matching org, no personal org;
  - pending membership request: no personal org, still `noOrg`/pending;
  - none of the above, setting on: one personal org, user OWNER, login action
    `default`, slug collision gets a suffix;
  - setting off (self-hosted default): no org created, `LoginActionNoOrg` as today;
  - logging in twice creates only one org (idempotent; concurrent first logins
    race-safe).
- Colleague hint: shown for a non-webmail domain with one matching org that
  accepts requests. Not shown for gmail.com, for two matching orgs, or for an org
  that suppresses join requests.
- Cleanup job: flags at 23 days and deletes at 30 an auto-personal org with no
  checks and one member. It does **not** delete an org with one check, a second
  member, `auto_personal=false`, or one younger than 30 days.
- Playwright (`web/dash0/e2e/`): fresh signup in SaaS mode lands on the
  dashboard of its personal org (`data-testid` on the banner and its dismiss /
  request buttons); self-hosted still lands on `/no-org`.
- Config test: default true under `saas`, false under `self-hosted`, env override.

## To verify

- Whether a "users may create orgs" restriction already exists for self-hosted
  instances, and where (step 3 must honour it).
- Whether a pending membership request is queryable per user (step 1.3).
- Where `/no-org` outcomes can be counted today (step 7).

## Open questions

1. Delete abandoned personal orgs after 30 days, or only flag them for an admin?
   **Recommended:** soft-delete at 30 days after a warning email at day 23. The
   orgs hold nothing, by definition.
2. Self-hosted default? **Recommended:** off, opt-in via the setting.
3. Should the colleague banner appear when several orgs share the domain?
   **Recommended:** no, only for exactly one, to avoid listing orgs.

## Resolved open questions

(autopilot: recommended answer)
1. Soft-delete abandoned personal orgs 30 days after a warning email at day 23.
2. Self-hosted default is off, opt-in via the setting.
3. Show the colleague banner only when exactly one org shares the domain.
