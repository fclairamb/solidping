---
model: opus
effort: high
---

# Accepting an invitation for an existing account logs in as that account, with the invite token as the only proof

## Problem
Found by the security scan of 2026-10-11 (finding C3). **Critical:** takeover of any existing account, with no password and no TOTP.
- **Who:** any org admin, in every deployment mode. On SaaS anyone can sign up and create an org (`POST /api/v1/orgs`, `server/internal/app/server.go:905`), so it is anyone.
- **Impact:** a super-admin victim means full instance compromise.
- Reproduced end to end by the scan.

**The flaw.** `POST /api/v1/auth/accept-invite` is public (`server/internal/app/server.go:764`).
- `AcceptInvite` (`server/internal/handlers/auth/service.go:3849-3983`) looks up the invited email. Only the new-user branch (`:3871-3890`) reads `req.Password`.
- When the email already has an account, the code goes straight to the membership (`:3892-3929`) and mints that user's refresh and access tokens (`:3942-3982`).
- The "already a member" branch (`:3893-3896`) mints a session too.
- The invite token is the only proof, and the inviter holds it: `CreateInvitation` returns it in clear (`InviteResponse.Token` and `InviteURL`, `service.go:3580-3594`). This is by design, so the link can be shared by hand when email is off.

**Attack.** An evilco admin invites alice@acme.com and accepts with the returned token. They receive alice's session, then `switch-org` into each of her orgs.

**dash0 today** (`web/dash0/src/routes/invite.$token.tsx`):
- With a stored token, it shows one "Accept" button that posts the invite token alone (`handleAcceptAuthenticated`, `:41-57`).
- Without one, it shows the create-account form (`:206-245`).
- Both calls go through `useAcceptInvite` with `skipAuth: true` (`web/dash0/src/api/hooks.ts:4597-4606`), so the server never learns who is signed in.
- `acceptInviteSession` (`web/dash0/src/contexts/AuthContext.tsx:505`) then replaces the current session with whichever account the invite targets.
- A logged-out existing user who types any password into the create form gets their own session: the password is never checked.

**The CLI is the same.** `sp auth accept-invite` (`server/pkg/cli/auth_selfservice.go:373-420`) calls it through the unauthenticated `publicAPIClient`.

**Tests pin the flaw.** `TestAcceptInviteEnforcesMaxUsers` and `TestAcceptInviteReturnsOrganizations` (`server/internal/handlers/auth/service_test.go:1394-1480`) accept for a pre-existing user with no credential.

## Proposal
**Server**
1. **Optional authentication on the route.** Wrap `POST /auth/accept-invite` (`server.go:764`) in a new `func (m *AuthMiddleware) AuthIfPresented(next httpx.HandlerFunc) httpx.HandlerFunc` in `server/internal/middleware/auth.go`:
   - no credential: go straight to the handler;
   - any credential: go through `RequireAuth`.
   - Key it on `PresentsCredentials` (`middleware/auth.go:626-632`), the same shape as `mcp.AllowAnonymousHandshake` (`server/internal/mcp/anonymous.go:54-66`).
   - A credential that is present but invalid gets RequireAuth's 401, never an anonymous fallback.
   - RequireAuth's gates then apply for free: forced rotation, the demo guard and impersonation.
2. **`AcceptInvite` takes the caller.**
   - New signature: `AcceptInvite(ctx, req, callerUID string)`, where `""` means anonymous. The handler (`handler.go:1126-1150`) passes `claims.UserUID` from `getClaimsFromContext` when present.
   - The cases:

     | Invited email | Caller | Result |
     |---|---|---|
     | has an account | anonymous | new `ErrInviteLoginRequired`: **401 `INVITE_LOGIN_REQUIRED`** |
     | has an account | another user | new `ErrInviteEmailMismatch`: **403 `INVITE_EMAIL_MISMATCH`** |
     | has an account | the invited user | today's membership path (seat cap, audit, invite deletion), then today's session, scoped to the invited org |
     | has no account | anonymous | today's new-user path, unchanged (password of at least 8, `EmailVerifiedAt` set) |
     | has no account | signed in | `ErrInviteEmailMismatch`: the signed-in person is not the invitee |

   - Compare user UIDs, never email strings.
   - Run every check before any write. A refused attempt creates no membership and leaves the invite in place.
   - The session for the invited user still goes through `startSession`, which already refuses an impersonated request (`service.go:870-878`).
   - Map both errors in `handleInvitationError` (`handler.go:1279-1305`), and add both codes to `server/internal/handlers/base/base.go`.
3. **Impersonation.** Add `{POST, "/api/v1/auth/accept-invite"}` to `impersonationForbiddenRoutes` (`server/internal/handlers/auth/impersonation_guard.go:41-80`). A membership created under impersonation is the admin joining an org as the target. `TestImpersonationDenylistNamesRealRoutes` (`server/internal/app/impersonation_test.go:269`) checks that the entry names a real route.
4. **OpenAPI** (`server/internal/app/openapi/openapi.yaml:1226-1247`):
   - document the optional `bearerAuth`, the 401 `INVITE_LOGIN_REQUIRED` and the 403 `INVITE_EMAIL_MISMATCH`;
   - regenerate `server/pkg/client`;
   - update `wiki/api-specification/auth.md` if it describes the endpoint.
5. **CLI** (`server/pkg/cli/auth_selfservice.go:373-420`):
   - Send the stored credential when there is one: use the authenticated client instead of `publicAPIClient`.
   - On `INVITE_LOGIN_REQUIRED`, print that the invitation is for an existing account: run `sp auth login`, then retry.
   - On `INVITE_EMAIL_MISMATCH`, print which account the CLI is signed in as.

**dash0**

6. **`useAcceptInvite`** (`web/dash0/src/api/hooks.ts:4597-4606`): drop `skipAuth` when a token is stored, so the bearer goes out and the normal 401 refresh applies. Keep `skipAuth: true` for the logged-out create-account call.
7. **`web/dash0/src/routes/invite.$token.tsx`:**
   - **Logged out:**
     - Keep the create-account form, and add an "Already have an account? Sign in to accept" link to `/login`, with this page as `returnTo`.
     - When the form gets a 401 `INVITE_LOGIN_REQUIRED`, go to that same login URL with a short notice instead of an error: "An account already exists for this email. Sign in to accept the invitation."
   - **Logged in:**
     - Keep the Accept button.
     - On a 403 `INVITE_EMAIL_MISMATCH`, show "This invitation is for {masked email}; you are signed in as {current email}", with a "Sign out and continue" button (log out, then reload `/invite/$token`).
   - Add the strings to `web/dash0/src/locales/{en,fr,de,es}/auth.json`, next to `invite.creatingAccountFor`.
8. **Come back to the invite after login.**
   - `/invite/$token` is org-less, so `resolveDestination` (`web/dash0/src/lib/login-destination.ts:46-75`) drops it today. It only honours `/orgs/<resolved org>/…`, the OAuth authorize path and `/device`.
   - Add `isInviteReturnTo(returnTo, basepath)` and an `inviteReturnTo(basepath, token)` builder next to `isDeviceVerificationReturnTo` and `deviceVerificationReturnTo` (`:97-131`). `isInviteReturnTo` matches exactly `{basepath}/invite/<one segment>`, with no query and no extra path.
   - Honour it without the org-match rule, like `/device`.
   - The password, 2FA, passkey and SSO-handoff paths already go through `resolveDestination` (`routes/orgs/$org/login.tsx:265`, `:314`, `:320`, `:588`; `lib/auth-handoff.ts:73`; `lib/oauth-handoff.ts:120`).
   - **Zero-org users don't, and must.** The two `noOrg` branches (`routes/orgs/$org/login.tsx:256-259` and `:296-297`) navigate to `/no-org` and ignore `returnTo`. A user who left every org is exactly the one being invited back. Honour an invite `returnTo` there before falling back to `/no-org`.
   - Back on the invite page, now signed in, the user clicks Accept.

**Not covered: the new-user path.** Whoever holds the token can still create the invitee's account with a password of their choice, and the account is marked verified (`service.go:3881-3885`). The inviter holds the token too. That is a pre-account-takeover question in the N6 family, not C3: see Open question 2.

## Tests
- **`server/internal/handlers/auth/accept_invite_auth_test.go`** (new, package `auth`, `setupAuthTestServiceWithConfig`, `service_test.go:53`). Table-driven. Fixture: an existing alice@acme.com, with a password and TOTP enabled, invited by bob, an evilco admin.
  - **Anonymous accept**, with or without a password in the body: `ErrInviteLoginRequired`, no tokens, no membership, and the invite is still listed (`ListStateEntries` with `inviteKeyPrefix`). This is the regression test: before the fix it returns alice's tokens.
  - **Caller = bob:** `ErrInviteEmailMismatch`, with the same no-write assertions.
  - **Alice already a member, anonymous caller:** `ErrInviteLoginRequired`. Today this case mints a session too.
  - **Positive control, caller = alice:** membership created with the invited role, invite deleted, `LoginResponse.Organization.Slug` is the invited org.
  - **Positive control, new email, anonymous, with a password:** account created and session returned (today's behaviour).
  - **New email, signed-in caller:** `ErrInviteEmailMismatch`.
- **Existing tests to update:** `TestAcceptInviteEnforcesMaxUsers` and `TestAcceptInviteReturnsOrganizations` (`service_test.go:1394-1480`) pass the invitee as the caller. They keep asserting the seat cap and the org list.
- **`server/internal/handlers/auth/handler_test.go`:** the HTTP shapes of 401 `INVITE_LOGIN_REQUIRED` and 403 `INVITE_EMAIL_MISMATCH`, next to `TestAcceptInviteHandlerTooShortPasswordReturnsValidationError` (`:60-100`).
- **Route level, `server/internal/app/`** (style of `impersonation_test.go`):
  - an impersonation token on `POST /api/v1/auth/accept-invite`: 403 `IMPERSONATION_FORBIDDEN`;
  - a garbage bearer: 401, with no anonymous fallback;
  - no credential and a new email: 200.
- **`server/test/integration/auth_selfservice_cli_test.go`:** `TestCLICoverage_InviteAcceptInvite` (`:221`) keeps passing on the new-user path. Add an existing-user case:
  - the public client gets 401;
  - the admin's authenticated client, for an invite addressed to someone else, gets 403.
- **`web/dash0/src/lib/login-destination.test.ts`:**
  - `isInviteReturnTo` accepts `/d/invite/abc`;
  - it rejects `/d/invite/abc/x`, `/d/invite/`, `/d/invite/abc?x=1`, `//evil.example/d/invite/abc` and `https://acme.com/d/invite/abc`;
  - `resolveDestination` returns an invite returnTo whatever the resolved org.
- **`web/dash0/e2e/invitations.spec.ts`:**
  - keep "logged-out visitor sees the join card and can accept as a new user" (`:43`);
  - add "logged-out existing user is sent to login, lands back on the invite and accepts";
  - add "signed in as another account sees the mismatch card".

## To verify
- The SSO handoff for a user with no org (`lib/auth-handoff.ts`, `lib/oauth-handoff.ts`) also keeps an invite `returnTo`. If it lands on `/no-org` instead, fix it there the same way as step 8.
- Can a stale `access_token` cookie reach the logged-out create-account call, after a server-side invalidation the browser never heard about? The cookie is HttpOnly and is sent even with `skipAuth`. With step 1 such a call gets a 401 instead of creating the account. If this happens, the dash0 create-account path should clear the session on that 401 and retry once.

## Open questions
1. **The email-existence signal.** `INVITE_LOGIN_REQUIRED` tells whoever holds the token that the invited email has an account. Accept that, or answer the existing-user and new-user cases identically (always route through login)?
   - Recommended: accept it. The inviter can already learn this through `AddMember` (L6, 2026-10-12), and `GetInviteInfo` stays masked, so hiding it here alone protects nothing.
2. **The new-user path.** Keep letting the token holder set the invitee's first password, or require an email round trip when the inviter could have accepted?
   - Recommended: out of scope here. Spec it together with N6.

## Resolved open questions
Decided 2026-10-16 (all recommendations accepted):
1. Accept the email-existence signal of `INVITE_LOGIN_REQUIRED`; keep `GetInviteInfo` masked.
2. The new-user first-password path is out of scope: spec it together with N6.

## Related
- **L6 (2026-10-12):** `AddMember` adds any platform user by email, and doubles as an email-existence oracle.
- **N6 (2026-10-12):** pending-registration overwrite, the same pre-account-takeover family as the new-user path above.
- **M8 (2026-10-11):** `CreateInvitation` trusts the admin role in the JWT, so a demoted admin can still invite for up to an hour.
