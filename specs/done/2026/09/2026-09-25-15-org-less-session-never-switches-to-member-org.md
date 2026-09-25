---
model: opus
effort: high
---

# A federated login from another org's login page kicks a real member out with "session expired"

## Problem

Seen in prod on 2026-09-25 at 05:59 UTC, on mobile:

1. The user is on `/d/orgs/demo/login` and clicks **Google**. Their Google
   account is a member of `acmetech`, not of `demo`.
2. The callback refuses admission to `demo` and hands out an **org-less**
   session through `pendingMembershipRedirect`
   ([join_policy.go](server/internal/handlers/auth/join_policy.go)): access
   token only, no refresh token by design, landing on
   `/d/no-org?membershipPending=demo`.
3. The user goes back. The login page sees an authenticated session, picks an
   accessible org from `/auth/me`'s `organizations` (`acmetech`), and navigates
   to `/d/orgs/acmetech`.
4. The org-switch guard in [$org.tsx](web/dash0/src/routes/orgs/$org.tsx)
   (`needsOrgSwitch`) requires `auth.org !== null`. The session has no org, so
   no `switchOrg()` runs. Every org-scoped request answers **403**, the live
   socket gets 4403 and calls `refreshWithOutcome()`, and with no refresh token
   that escalates: `[auth] token refresh failed: no-refresh-token`, session
   cleared, redirect to `/orgs/acmetech/login?session_expired=true`.
5. The user ends up on `/no-org` again and clicks "Sign out and use another
   account".

A legitimate member could not reach their own organization, and was told
their session had expired one second after signing in.

There are two defects:

- **Frontend:** an org-less session never re-mints for an org it belongs to.
- **Backend:** the callback turns a login *attempted from* org A into an
  org-less session, even when the user is an admitted member of org B. For a
  password login, a wrong org is simply an error. For a federated login, the
  identity is proven, so the useful answer is a session on an org they belong
  to.

## Proposal

### Frontend (required)

1. `needsOrgSwitch`: drop the `auth.org !== null` term. An org-less session
   whose `organizations` list contains the URL's org must `switchOrg()` before
   children render, exactly like a cross-org session. Check first that
   `POST /api/v1/auth/switch-org` accepts an org-less access token (add a
   backend test either way). If it doesn't, make it: the endpoint only needs
   the user identity plus a membership check.
2. After a successful switch, the session holds a refresh token scoped to the
   target org. Check that `setSession` stores it (it does for normal switches).
3. `live-socket.ts`: an org-less session must not dial the org socket at all.
   The gate that holds children back until the switch finishes should already
   cover this once (1) is fixed. Assert it in a test.

### Backend (decide during implementation)

When a federated login is refused admission to the org it was started from,
and the user is an admitted member of at least one other org, answer with a
normal org-scoped session (with refresh token) on that org, and keep
`membershipPending=<refused slug>` so `/no-org` or the dashboard can still show
the "request sent" notice. Keep today's org-less path for users with zero
admitted memberships. If this changes behaviour the spec
`2026-09-08-01` relies on, stop and ask rather than override it.

## Tests

- Backend: `switch-org` with an org-less token for an org the user belongs to
  → 200, with a refresh token; for an org they don't belong to → 403.
- Backend: federated callback, refused on org A and member of org B → redirect
  lands on org B with a full session and `membershipPending=A`; member of no
  org → org-less path unchanged (positive control).
- Playwright: fake provider login from `/orgs/<other>/login` for a member of
  `acme` → ends on `/orgs/acme` authenticated, with no `session_expired=true`
  navigation and no `token refresh failed` console error.

## Implementation Plan

**Conflict check with 2026-09-08-01 (redirect to an accessible org): none.** That spec
is frontend-only (`pickAccessibleOrg`, the login-page redirect, the org layout's
non-member redirect) and explicitly expects no backend change. Nothing here changes
the membership ordering, `pickAccessibleOrg`, or the non-member redirect. The backend
fallback picks its org with the same rule that spec's §3 settled on (last accessed,
then most recently joined), and the frontend change only touches the *member* branch
(`needsOrgSwitch`), which is disjoint from `needsAccessibleOrgRedirect` (a URL org in
`organizations` always resolves to itself in `pickAccessibleOrg`).

### Backend
1. `join_policy.go` — `CompleteOrgLogin`: when org A refuses the login (`pending`), look
   for an admitted membership (`fallbackMemberOrg`). If there is one, mint a normal
   org-scoped session on it through `GenerateTokensForOAuth` (refresh token, same
   `created_with.method` shape, `auth.login_succeeded` on that org) and return it with
   `Pending=true`, `PendingOrgSlug=<A or "" when rule 6 suppressed the request>`, and a
   new `FallbackOrgSlug=<B>`. Zero admitted memberships keeps today's org-less
   `pendingSession`.
2. Which org B: the org of the user's most recent refresh token **if still a member**,
   else `ListMembersByUser[0]` (most recently joined), skipping memberships whose org is
   gone. Mirrors `resolveDefaultOrg` and spec 2026-09-08-01 §3, but checks membership
   (a stale refresh-token row for an org the user left must not mint a session there).
   Runs after `autoJoinMatchingOrgs`, so an org the same login just auto-joined counts.
   A lookup failure degrades to the org-less session (today's behaviour), never fails
   the login.
3. `ProviderOutcome` gains `FallbackOrgSlug`; `handoffSession` scopes a pending
   outcome's session to it (org-less when empty) and still carries
   `MembershipPending`. Every provider service (google, github, gitlab, microsoft,
   discord, slack, oidc, saml) and the Slack app-install callback thread it through.
4. Tests: `CompleteOrgLogin` refused on A + member of B → refresh-token row on B with
   `created_with.method`, access token scoped to B, `PendingOrgSlug=A`; zero
   memberships → org-less (positive control); several memberships → most recent
   refresh-token org wins, else most recently joined; a stale refresh token for an org
   left is ignored. Handoff: pending + fallback → redirect carries
   `membershipPending=A`, redeemed session is scoped to B with a refresh token, and the
   exchange answers `organization=B`. `switch-org` with an org-less access token → 200
   with a refresh token for a member org, 401 `INVALID_CREDENTIALS` for a non-member
   (the endpoint's existing anti-enumeration answer, unchanged).

### Frontend
5. `$org.tsx` — move the switch predicate into a pure `lib/org-switch.ts`
   (`needsOrgSwitch`) without the `auth.org !== null` term, so an org-less session whose
   `organizations` contains the URL's org re-mints via `switchOrg()` before children
   (and `LiveEventsProvider`'s socket) mount. Unit-test it, including the org-less case.
6. `AuthContext.applyLoginResponse` / `validateSession`: an org-less response clears
   `auth.org` (and the stored org) instead of keeping a stale slug from an earlier
   session, which would otherwise read as "already scoped to this org" and skip the
   switch — the same kick-out through a different door.
7. `auth.complete.tsx` + `lib/auth-handoff.ts`: when the exchanged session is scoped to
   an org and names a `membershipPending` org, show a toast that the request to that
   org was sent (new locale key in en/fr/de/es), since the landing is the dashboard, not
   `/no-org`.
8. E2E: (a) `sso-handoff.spec.ts` — an ordinary member of `acc-*` signs in through the
   fake OIDC provider from org `test` → lands on `/orgs/acc-*`, refresh token stored,
   handoff URL carries `membershipPending=test`, no `session_expired`, no
   `token refresh failed`; the existing outsider test stays as the org-less control.
   (b) new `org-less-session-switch.spec.ts` — an org-less session (no refresh token)
   that belongs to an org opens `/orgs/<it>` → switch-org, dashboard renders, refresh
   token stored, no 403, no `session_expired`, no `token refresh failed`.
