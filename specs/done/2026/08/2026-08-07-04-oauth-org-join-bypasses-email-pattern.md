---
model: opus
effort: high
---

# Org-scoped social OAuth login bypasses `registration.email_pattern` — any authenticated account can join any org from its login page

## Problem

Every social/SSO login flow takes an `org` slug in the login URL
(`GET /api/v1/auth/<provider>/login?org=<slug>`), bakes it into the OAuth
state, and on callback **unconditionally adds the authenticated user to that
org** via a per-service `ensureMembership` helper:

```go
// server/internal/handlers/auth/microsoft_service.go:179 (HandleCallback)
member, err := s.ensureMembership(ctx, org.UID, user.UID)
```

`ensureMembership` (e.g. [`microsoft_service.go:331-373`](../../server/internal/handlers/auth/microsoft_service.go))
checks only for an existing membership and the `MaxUsers` cap. It never
consults the org's `registration.email_pattern` parameter, never looks for an
invitation, and never goes through the membership-request approval flow. The
pattern is only applied by `autoJoinMatchingOrgs`
([`service.go:2286`](../../server/internal/handlers/auth/service.go)) — a
*separate* mechanism that joins the user to *additional* matching orgs after
the unconditional join has already happened.

**Consequence: in SaaS mode, anyone with any Microsoft/Google/GitHub/…
account can join any organization** simply by initiating OAuth from that
org's login page (the slug is attacker-controlled in the URL). They land in
the org as a `user`-role member with access to checks, results, incidents,
and the members list.

This is not theoretical — it happened on the k8xp instance on 2026-08-07:

- Org `stonaltech` has `registration.email_pattern = @stonal\.com$`.
- `patrice.cavezzan@stonal.onmicrosoft.com` (Microsoft Graph profile with an
  empty `mail` field, so the code fell back to the `userPrincipalName` —
  [`microsoft_service.go:156-159`](../../server/internal/handlers/auth/microsoft_service.go))
  clicked "Sign in with Microsoft" on the stonaltech login page at
  15:08 UTC and became a member despite not matching the pattern.
- Earlier gmail.com members of the same org joined through the same hole via
  the Google/GitHub/Discord flows.

All nine flows share the copy-pasted pattern — each file has its own
`ensureMembership` whose unconditional `NewOrganizationMember(...)` sits at
(all paths under `server/internal/handlers/auth/`):

| Service | membership creation |
|---|---|
| `microsoft_service.go` | 364 |
| `google_service.go` | 349 |
| `github_service.go` | 408 |
| `gitlab_service.go` | 373 |
| `discord_service.go` | 483 |
| `slack_service.go` | 502 |
| `oidc_service.go` | 450 |
| `saml_service.go` | 734 |
| `ldap_service.go` | 493 |

All of them use **global instance config** (`s.cfg.Microsoft`, `s.cfg.OIDC`,
`s.cfg.SAML`, …), so none of these are org-configured IdPs where implicit
membership could be argued intentional.

(The Slack *integration* join in
`server/internal/integrations/slack/service.go:745` is a different,
workspace-gated mechanism — only members of the org's connected Slack
workspace pass — and is out of scope.)

## Proposal

Replace the nine per-service `ensureMembership` copies with **one shared
join-policy method** on the auth `Service`, and make that method enforce the
org's admission rules:

```go
// JoinOrgViaLogin decides what an authenticated user gets when they complete
// a login flow initiated from org's login page.
func (s *Service) JoinOrgViaLogin(ctx context.Context, org *models.Organization, user *models.User) (member *models.OrganizationMember, pending bool, err error)
```

Admission rules, in order:

1. **Existing member** → return the membership (pure login, unchanged).
2. **Org bootstrap**: org has zero members → join as `admin` (preserves
   current first-user behavior and self-hosted onboarding).
3. **Valid invitation** exists for the user's email → join with the invited
   role, consume the invite (mirrors `AcceptInvite` semantics).
4. **Email matches `registration.email_pattern`** (validated via
   `validateAutoJoinRegex`, same defensive skip as
   [`autoJoinMatchingOrgs`](../../server/internal/handlers/auth/service.go)) →
   join as `user`, subject to `CheckMembershipSlot`.
5. **Otherwise** → do *not* create a membership. Create (or reuse) a
   `membership_requests` row (the table and admin approval UI already exist)
   and report `pending=true`.

Handler behavior for `pending=true`: the user is authenticated (their user
row and provider link exist) but has no membership, so do not mint an
org-scoped session for the target org. Redirect to the existing
"membership pending / request access" surface with an explicit query flag
instead of the org dashboard, mirroring how the membership-request flow
already presents a pending state.

Notes / constraints:

- `autoJoinMatchingOrgs` stays as-is (it is correct); this spec only fixes
  the unconditional direct join.
- Keep the `MaxUsers` cap enforcement in the shared method (single place).
- The duplicate-account aspect of the incident (Graph `mail` empty → UPN
  fallback creates a second user instead of linking the existing
  `@stonal.com` account) is a **separate problem** — note it in code near the
  fallback but do not expand scope here.
- No config flag to disable the gate: an org with an empty
  `registration.email_pattern` simply has no rule-4 path, so unknown users
  fall through to a membership request — which is the safe default in both
  SaaS and self-hosted modes (bootstrap is covered by rule 2).

### Tests

Table-driven tests on the shared method plus at least one end-to-end callback
test per representative provider (Microsoft + one other). Must prove the
negatives, with positive controls:

- Non-matching email + no invite → membership request created, **no**
  `organization_members` row, callback redirects to the pending surface
  (negative), while a matching email in the same test table joins (control).
- Pattern that would match but is unsafe (fails `validateAutoJoinRegex`) →
  treated as absent.
- Zero-member org → OAuth user becomes admin (bootstrap preserved).
- Existing member logs in unchanged; org at `MaxUsers` cap → no join.
- Invited email joins with the invited role and the invite is consumed.

## Implementation Plan

1. **`join_policy.go` (new, package `auth`)** — the single admission chokepoint.
   - `func (s *Service) JoinOrgViaLogin(ctx, org *models.Organization, user *models.User) (*models.OrganizationMember, bool, error)`
     applying, in order: (0) super admin → `(nil, false, nil)` implicit access, no
     membership row; (1) existing member → return it; (2) zero-member org →
     `admin` (bootstrap); (3) valid invitation for the email → invited role +
     invite consumed; (4) email matches the org's `registration.email_pattern`
     (skipped when `validateAutoJoinRegex` rejects it, same defensive skip as
     `autoJoinMatchingOrgs`) → `user`; (5) otherwise → no membership, create or
     re-open a `membership_requests` row, `pending=true`.
     `CheckMembershipSlot` (MaxUsers) is enforced once here, before any
     membership creation.
   - `func (s *Service) CompleteOrgLogin(ctx, org, user) (*ProviderLoginResult, error)`
     — the shared tail every provider callback now runs: join decision →
     `autoJoinMatchingOrgs` → tokens. On `pending` it mints an **org-less**
     access token (no refresh token, empty org slug/role — same shape as the
     no-org password login) and sets `Pending: true`.
   - `pendingMembershipRedirect(orgSlug, accessToken, expiresIn)` → the shared
     `/dash0/no-org?...&membershipPending=<slug>` redirect target.

2. **Nine call sites** — delete every per-service `ensureMembership` /
   `ensureLDAPMembership` copy and route through the shared method:
   `microsoft`, `google`, `github`, `gitlab`, `discord`, `slack`, `oidc`,
   `saml` services call `CompleteOrgLogin` and gain a `Pending bool` on their
   result struct; `ldap_service.go` calls `JoinOrgViaLogin` directly (its
   pending case simply leaves the user with no membership, which
   `resolveOrgPreference` already renders as a no-org login).

3. **Handlers** (`microsoft.go`, `google.go`, `github.go`, `gitlab.go`,
   `discord.go`, `slack.go`, `oidc.go`, `saml.go`): when `result.Pending`,
   redirect to the pending surface instead of the org dashboard.

4. **dash0** `routes/no-org.tsx`: read the `membershipPending` search param and
   render an alert naming the org whose login was completed without membership.

5. **Code note** near the Microsoft Graph `mail`→UPN fallback flagging the
   separate duplicate-account problem (no behavior change).

6. **Tests** — `join_policy_test.go`: table-driven over the admission rules
   (non-matching email → request + no member row, matching email control,
   unsafe pattern treated as absent, zero-member bootstrap admin, existing
   member unchanged, MaxUsers cap, invitation role + consumption), plus
   end-to-end callback assertions for Microsoft and Google (pending result,
   no membership row, membership request created) and a redirect-shape test
   for `pendingMembershipRedirect`.
