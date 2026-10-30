---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# New users without an org get stuck on /no-org: create a personal org for them

## Problem

A user who signs in and belongs to no organization lands on `/no-org`
(`LoginActionNoOrg`, `server/internal/handlers/auth/service.go:447`). Prod data
(2026-08-04 to 2026-10-30) shows this is rare but costly at our volume:
- About 8 real sign-ups a week. 4 users never got an org at all.
- The only two of them with a session replay did the same thing: Google
  sign-up, then `/no-org`, then a click on "I'd rather join an existing
  organization" (`JoinOrgCard`, `web/dash0/src/routes/no-org.tsx:123`,
  `:147`), then they left without sending a join request.

So two cheap changes. Out of scope: the colleague banner, cleanup of
abandoned orgs, and an instance setting.

## Proposal

1. **Auto-create a personal org at sign-in when nothing else applies.**
   - Wherever a sign-in would otherwise end in `LoginActionNoOrg` for a user
     with no membership, first check three things in this order, and keep
     today's behaviour when one applies:
     1. a pending invitation (`findOrgInvitationForEmail`,
        `server/internal/handlers/auth/join_policy.go:598`);
     2. a matching `registration_email_pattern` (`autoJoinMatchingOrgs`,
        `service.go:2801`);
     3. a pending membership request by the user.
   - If none applies, create the org with `Service.CreateOrg`
     (`service.go:3484`):
     - empty slug, so `orgslug.GenerateUnique` derives one;
     - `SlugBase` set to the first name;
     - `Name` set to the localized "{firstName}'s org", the same text as the
       `createOrg.suggestedPersonal` suggestion;
     - the user as OWNER.
     The sign-in then completes into that org instead of `/no-org`.
   - Put this in one helper called from every no-org exit:
     - password login: `resolveDefaultOrgForLogin` / `resolveFromMemberships`,
       `service.go:1053`, `:1073`;
     - registration confirmation: `service.go:2737`-`:2752`;
     - provider logins: `pendingSession`, `join_policy.go:536`.
   - Out of scope: a user who just deleted their last org (`org_delete.go:209`)
     still lands on `/no-org`. Recreating one would undo their action.
   - A pending MCP / device authorization (`returnTo`) must still resume after
     the auto-creation. `orgCreationReturnTo` (`web/dash0/src/lib/login-destination.ts`,
     added on this branch for fclairamb/solidping-business#35) already handles
     this after a manual creation. Do the same for the automatic one.
2. **Hide "I'd rather join an existing organization" when there is nothing to
   join.** On `/no-org`, render `JoinOrgCard` only when the user arrived through
   an explicit join path:
   - a `?join=<slug>` search param;
   - a pending or rejected membership request;
   - an invite that failed.
   Otherwise the page shows only org creation. Keep the locale keys used in
   every locale, or remove unused ones (`locale-parity.test.ts`).

## Tests

- `server/internal/handlers/auth`, table-driven over password login,
  registration confirm and provider login:
  - pending invite: joins the invited org and **no** personal org is created;
  - pattern match: joins the matching org, no personal org;
  - pending membership request: no personal org;
  - none of the above: exactly one personal org, the user is OWNER, the login
    action is `default`, a slug collision gets a suffix;
  - a second login creates no second org;
  - a user who deleted their last org still gets `LoginActionNoOrg`.
- `web/dash0`:
  - unit test that `JoinOrgCard` is hidden without a join reason and shown with
    `?join=<slug>`;
  - the existing `create-org-card` / `login-destination` tests still pass;
  - a Playwright check that a fresh Google-style sign-up lands on its org
    dashboard.

## To verify

- Whether a pending membership request can be queried per user, for step 1.3.
