---
model: opus
effort: high
---

# Sign-in-with-Slack does not grant access to the org linked to the user's Slack workspace

## Problem

Both Slack entry points resolve the target organization from the Slack **team
ID** via `organization_providers` (single source of truth), so the *routing*
part of "sign in from Slack → your workspace's org" already works:

- Sign-in-with-Slack: `findOrCreateOrganization`
  (`server/internal/handlers/auth/slack_service.go:385`)
- App install: `findOrCreateOrganizationByTeamID`
  (`server/internal/integrations/slack/service.go:594`)

But the *admission* outcomes diverge, and the sign-in path does **not**
guarantee access:

1. **Sign-in-with-Slack** funnels through the shared admission chokepoint
   `CompleteOrgLogin` → `JoinOrgViaLogin`
   (`server/internal/handlers/auth/join_policy.go:68`). For an **existing**
   linked org, a fellow workspace member with no outstanding invite and no
   `registration.email_pattern` match falls to rule 5: **no membership**, a
   `membership_requests` row, and an org-less pending session. Yet Slack has
   just *attested* that this user is a member of the very workspace the org is
   linked to — the OAuth token exchange and `openid.connect.userInfo` team
   claims can only succeed for a member of that workspace. From the user's
   perspective: "I signed in with Slack, SolidPing knows my workspace, and I
   still can't get into my team's org without an admin clicking approve."

2. **App install** (`HandleOAuthCallback` →
   `ensureOrganizationMembership`,
   `server/internal/integrations/slack/service.go:718`) does the opposite: it
   **unconditionally** creates a membership (user role; admin when the org is
   empty). This is exactly the kind of per-connector `ensureMembership` copy
   the chokepoint was built to eliminate (see the doc comment at
   `join_policy.go:45`), and it bypasses both the org's admission rules and
   the `MaxUsers` entitlement cap (`CheckMembershipSlot`) entirely.

So the same workspace member is refused by the sign-in door and waved through
by the install door. Neither matches the intended product behavior: *being a
member of the linked Slack workspace should be sufficient to access the
matching org*, through the normal chokepoint.

## Proposal

Introduce **workspace attestation** as a first-class admission rule, and route
both Slack paths through the same chokepoint.

1. **New admission rule in `JoinOrgViaLogin`** (between the invite rule 3 and
   the email-pattern rule 4): if the login flow carries a provider attestation
   `{provider: slack, providerID: teamID}` and `organization_providers` links
   that exact `(ProviderTypeSlack, teamID)` to the org being joined, admit the
   user as `MemberRoleUser` via `createLoginMembership` (which already
   enforces the `MaxUsers` cap; on cap failure fall through to the
   membership-request path rather than erroring the login).
   - Plumb the attestation via a new optional argument/options struct on
     `JoinOrgViaLogin`/`CompleteOrgLogin` — non-Slack connectors pass none and
     see zero behavior change. Verify the link server-side against
     `organization_providers`; never trust a client-supplied team ID.

2. **Org-level opt-out**: honor a per-org parameter
   `registration.slack_workspace_auto_join` (bool, default `true` — the org
   exists *because* of this workspace link, so auto-join is the sensible
   default; follows the dotted param-key convention). When `false`, skip the
   new rule and fall through to today's behavior (email pattern / membership
   request).

3. **Unify the install path**: replace
   `integrations/slack.ensureOrganizationMembership` with the chokepoint
   (`CompleteOrgLogin` with the same Slack attestation). Outcomes converge:
   first user of a fresh org still bootstraps as admin (existing rule 2),
   workspace members join as user, the `MaxUsers` cap is finally enforced on
   installs, and an org that opted out yields the pending flow (the install
   callback must then hand the pending org-less session to the dashboard the
   same way the sign-in callback does — reuse the `Pending` /
   `pendingMembershipRedirect` plumbing).
   - Careful: the channel-edit-page install variant (`targetOrgSlug` set)
     targets an org the *already-authenticated* user belongs to; rule 1
     (existing member) makes it a no-op there, but the fallback branch (slug
     not found → resolve by workspace) must still carry the attestation.

4. **Tests** (`handlers/auth` + `integrations/slack` service tests):
   - Existing linked org + new workspace member signing in → member created
     with role user, org-scoped session (the core fix).
   - Same, with `registration.slack_workspace_auto_join=false` → pending
     request, org-less session (today's behavior preserved on opt-out).
   - Attestation for a *different* team ID than the org's link → not admitted
     (negative control — proves the rule checks the link, not just presence
     of an attestation).
   - Non-Slack federated login into a Slack-linked org → unchanged (no
     attestation, no auto-join).
   - Install path: `MaxUsers` cap reached → no membership, pending request
     (proves the bypass is closed); empty org → installer becomes admin.

### Out of scope / open questions

- **Slack guests**: single/multi-channel guests pass the OAuth as workspace
  members and `openid.connect.userInfo` does not expose guest status. V1
  accepts them (guests are invited by the workspace admin, hence
  semi-trusted). A follow-up could check `users.info`
  (`is_restricted`/`is_ultra_restricted`) through the org's bot token when a
  Slack connection exists, and downgrade guests to the membership-request
  flow.
- Retroactively admitting users whose membership requests are already pending
  is not attempted — they get in on their next Slack sign-in.

## Implementation Plan

1. **`handlers/auth/join_policy.go` — the attestation seam.**
   - `type LoginOption func(*loginOptions)` plus
     `WithSlackWorkspace(teamID string) LoginOption`. Both `JoinOrgViaLogin`
     and `CompleteOrgLogin` gain a trailing `opts ...LoginOption`, so the
     eight non-Slack connectors and LDAP are *literally unchanged* (no
     attestation → `loginOptions{}` → new rule inert).
   - New **rule 4** (after the invitation rule, before
     `registration.email_pattern`): admit as `MemberRoleUser` when
     `slackWorkspaceAdmits` says so.
   - `slackWorkspaceAdmits(ctx, org, teamID) bool` — the whole security
     decision, in one place:
     - empty `teamID` → false (no attestation);
     - org opted out (`registration.slack_workspace_auto_join = false`) → false;
     - `GetOrganizationProviderByProviderID(slack, teamID)` must resolve
       (the query already filters `deleted_at IS NULL`, so a revoked link
       admits nobody) **and** its `OrganizationUID` must equal `org.UID` —
       this is the cross-tenant guard: the decision is driven by the
       team ID Slack itself returned, never by the attacker-controlled `org`
       slug in the login URL.
   - `slackWorkspaceAutoJoinEnabled(ctx, orgUID) bool` — reads the org param,
     defaults to **true**, tolerates bool / string / number encodings.
   - Seat cap: `CheckMembershipSlot` is consulted *before* creating the
     membership; when it refuses, the rule is skipped and the login falls
     through to the email-pattern / membership-request path instead of
     erroring (the cap is never bypassed).
   - `RedirectPendingMembership(...)` — exported wrapper over
     `pendingMembershipRedirect` + the access-token cookie, so the Slack
     *install* callback (another package) reuses the exact same pending
     surface as the sign-in callbacks.

2. **`handlers/auth/slack_service.go` (sign-in connector).** Pass
   `WithSlackWorkspace(oauthResp.Team.ID)` into `CompleteOrgLogin` — the team
   ID comes from the OAuth token exchange, i.e. Slack's own attestation, not
   from anything the browser supplied. Endpoint constants become per-service
   fields (`oauthURL`, `userInfoURL`) so tests can drive the *real*
   `HandleCallback` against httptest stand-ins (the Microsoft/Google pattern).

3. **`integrations/slack` (install connector) — close the bypass.** Delete
   `ensureOrganizationMembership` and route the install through
   `CompleteOrgLogin(..., WithSlackWorkspace(team.ID))`. `OAuthResult` gains
   `Pending`; the handler sends a pending install to the shared no-org
   surface instead of minting an exchange code (which requires a refresh
   token an org-less session does not have). Bot-connection creation stays
   unconditional — the workspace install is an org-level artifact and must
   survive even when the installing human is left pending. Same endpoint
   seams as (2) for the end-to-end tests.

4. **Docs.** `web/docs/docs/configuration/authentication.md`: document that
   members of the workspace an org is linked to are admitted on
   sign-in/install, and how to opt out with
   `registration.slack_workspace_auto_join`.

5. **Tests.**
   - `join_policy_test.go`: table-driven `TestJoinOrgViaLoginSlackWorkspace`
     — admitted (positive control) / different team ID / workspace belonging
     to *another* org (cross-tenant negative) / org with no Slack link /
     revoked link / opt-out false / opt-out true / no attestation at all.
     Plus a seat-cap test (no member row, pending request, no error) and
     proof the eight other connectors still behave (existing suite).
   - `slack_service_test.go`: end-to-end `HandleCallback` against fake Slack
     endpoints — workspace member admitted with an org-scoped session;
     opt-out org yields `Pending` + a membership request.
   - `integrations/slack/service_oauth_test.go`: end-to-end
     `HandleOAuthCallback` — empty org bootstraps the installer as admin;
     seat-capped org creates **no** membership and returns `Pending` (the
     regression test for the bypass this spec closes).
