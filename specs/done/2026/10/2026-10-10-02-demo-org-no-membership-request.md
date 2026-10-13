---
model: sonnet
effort: medium
---

# Sign-ins on the demo org, or on the SaaS default org, must not file a membership request

## Problem
Rule 6 of the federated admission flow ends in `ensureMembershipRequestForLogin` (`server/internal/handlers/auth/join_policy.go:292`, defined at `:746`). It writes a `pending` `membership_requests` row, notifies the org's admins (`notifyAdminsOfMembershipRequest`, `:760,780`), and the handoff redirects to `/d/auth/complete?membershipPending=<org>` and then `/d/no-org?membershipPending=<org>`. The user reads "Your sign-in to {{org}} succeeded, but that organization hasn't admitted you yet. A join request was sent to its admins." (`web/dash0/src/locales/en/auth.json`). Two orgs reach that path by accident, and neither will ever admit anyone.

**The demo org.** A visitor on the live demo (`/d/orgs/demo`, reached via the www "Live demo" CTA / `?demo=true`) clicks "Account" and lands on `/d/orgs/demo/login?session_expired=false`. Signing up there (seen with Google OAuth) files a request to `demo`. Nobody approves demo join requests: prod has 3 pending ones, the oldest from 2026-09-11. PostHog shows 5 people reaching `membershipPending=demo` between 2026-09-25 and 2026-10-10.

**The SaaS `default` org.** `suppressDefaultOrgJoinRequest` (`join_policy.go:~301-314`, spec 2026-09-05-01) already skips the request, but only when all three hold: SaaS, `default`, and an account minted by this very callback (`opts.newlyCreatedUser`). Its comment says a returning user signing in on `/orgs/default/login` "is asking for that org on purpose". That is not true. The dashboard sends org-less users there itself: an expired session goes to `/d/orgs/default/login?session_expired=true&returnTo=%2Fd%2Fno-org`. Seen in PostHog on 2026-10-08: a new user signed up through `default` (correctly, no request), signed in again about 100 seconds later on `/d/orgs/default/login?session_expired=false`, was no longer "newly created", got a request filed to `default` plus the `membershipPending=default` notice, and left without creating an org. On SaaS, `default` is the operator's own org. People join it by invite, never through a sign-in request.

A user who signs up from the demo and comes back later hits the `default` case too, so both are fixed together.

## Proposal
1. Replace `suppressDefaultOrgJoinRequest` with one predicate in `join_policy.go` (e.g. `suppressLoginJoinRequest(org)`), true when either:
   - **demo org:** `org.Slug == s.fullCfg.Demo.ResolvedOrgSlug()` (`server/internal/config/config.go:1164`, default `config.DefaultDemoOrgSlug = "demo"`, `:2776`). Any deployment mode, new or returning user. Do not gate on `Demo.Enabled`: the `demo` org and its stale requests exist in prod regardless of the flag.
   - **SaaS default org:** `s.fullCfg.Deployment.Mode == config.DeploymentModeSaaS && org.Slug == defaults.Organization`. Drop the `newlyCreatedUser` condition.

   Self-hosted `default` keeps today's behaviour: there it is usually the one real org, and a colleague's sign-in should reach its admins.
2. Update the rule-6 call site (`join_policy.go:~280-290`) and its comment, and the `suppressDefaultOrgJoinRequest` doc comment that claims a returning user on `/orgs/default/login` asks on purpose. Keep the log line, naming which carve-out fired.
3. If `newlyCreatedUser` / `newlyCreatedUserOption` (`join_policy.go:109-139`) has no other reader after step 1, remove it and its plumbing through the connectors. If something else reads it, leave it alone.
4. Make sure a suppressed login yields no `membershipPending`. `PendingOrgSlug` (doc at `join_policy.go:78-85`) is already documented as empty when rule 6 suppressed the request. Confirm the new predicate goes through the same path, so `CompleteOrgLogin` sets `session.MembershipPending` (`:~856`) to empty and `handoffRedirect` (`:808`) carries no param. The user lands on the plain create-your-org `/no-org` screen.
5. Frontend (`web/dash0/src/lib/auth-handoff.ts`): no change expected once the backend stops sending the param. Only touch it if step 4 finds a gap.
6. The explicit request endpoint (`server/internal/handlers/auth/membership_requests.go`, `POST .../membership-requests`) should refuse the demo org and, on SaaS, `default`. Use a 4xx with a clear error code, not a silent success.
7. One-off SQL for the PR description only. **Do not run it.** Prod data changes are Florent's to execute. Check the column names and the cancelled status value against `server/internal/db/models/membership_request.go` and the postgres migration `001_v0_1_0.up.sql` first:
   ```sql
   UPDATE membership_requests
   SET status = 'cancelled'
   WHERE status = 'pending'
     AND organization_uid IN (SELECT uid FROM organizations WHERE slug IN ('demo', 'default'));
   ```
   The `default` half applies to the SaaS deployment only.

## Tests
- `server/internal/handlers/auth/join_policy_default_org_test.go` (`TestJoinOrgViaLoginDefaultOrgNewAccount`): flip the SaaS + `default` + returning-user case to "no request". Keep the self-hosted `default` cases (new and returning) opening a request. Rename the test if "NewAccount" no longer fits.
- New cases in the same table, or a sibling `join_policy_demo_org_test.go`:
  - demo org + brand-new account, SaaS: no `membership_requests` row, pending org-less outcome, `PendingOrgSlug` empty.
  - demo org + returning account: no row.
  - demo org on self-hosted: no row.
  - custom `Demo.OrgSlug` (e.g. `showcase`): that slug is suppressed, and an org literally named `demo` then gets no special treatment (opens a request).
  - Negative controls: a regular org (`acme`) still opens a `pending` request for new and returning users, on SaaS and self-hosted.
- `TestCompleteOrgLoginPendingOrgSlug` (`join_policy_default_org_test.go:124`): extend with demo and SaaS-returning-default cases. The handoff redirect has no `membershipPending` param. A regular org still has it.
- `membership_requests.go` handler test: the explicit request to `demo` (any mode) and to `default` (SaaS) is refused. To `default` on self-hosted it is accepted.
- `web/dash0/src/lib/auth-handoff.test.ts`: only if step 5 changes the frontend.

## To verify
- That `PendingOrgSlug` stays empty on the suppressed path (`join_policy.go:~856` and wherever `joinOrgViaLogin`'s result is turned into `PendingOrgSlug`).
- That every connector (Google, GitHub, Slack, Discord, password signup if it applies) reaches rule 6 through `joinOrgViaLogin`, and none calls `ensureMembershipRequestForLogin` directly.
- Whether the demo org carries a flag of its own in `organizations`. The Go code only shows `users.demo` (`server/internal/db/models/auth.go:84-96`) and the config slug. Without an org flag, the config slug is the identification.
- Other readers of `newlyCreatedUser` before removing it (step 3).
