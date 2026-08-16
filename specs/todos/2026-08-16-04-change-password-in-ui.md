---
model: opus
effort: high
---

# A logged-in user cannot change their password from the dashboard

## Problem

There is no way to change your password while logged in. The only path to a new
password is the *forgotten*-password flow: log out, go to
[`forgot-password.tsx`](web/dash0/src/routes/forgot-password.tsx), wait for an
email, click the link, land on
[`reset-password.$token.tsx`](web/dash0/src/routes/reset-password.$token.tsx).
That is a poor experience for the ordinary "I want to rotate my password" case,
and it is a hard blocker for any user whose mailbox is slow, filtered, or simply
different from the address on the account.

Concretely, today:

- The backend exposes only the two unauthenticated endpoints —
  `POST /api/v1/auth/request-password-reset` and `POST /api/v1/auth/reset-password`
  ([server.go:552](server/internal/app/server.go:552)). There is **no**
  authenticated change-password endpoint on `rootAuthProtected`
  ([server.go:570](server/internal/app/server.go:570)).
- The account security page already **fetches** `hasPassword` from
  `GET /api/v1/auth/me` and declares it in its `MeSecurity` interface
  ([account.security.tsx:40](web/dash0/src/routes/orgs/$org/account.security.tsx:40)),
  but never renders anything with it. The page shows only Passkeys and TOTP
  cards ([account.security.tsx:153-269](web/dash0/src/routes/orgs/$org/account.security.tsx:153)).
  The field is dead weight that was clearly meant for exactly this.
- Users who signed up through SSO/OAuth (`hasPassword == false`) have no way to
  *set* a password at all, so they are permanently locked into their identity
  provider.

## Proposal

Add an authenticated **Password** card to the account security page, backed by a
new `POST /api/v1/auth/change-password` endpoint. Model it on `ResetPassword`
([service.go:2539](server/internal/handlers/auth/service.go:2539)) so the
post-change side effects (revocation, confirmation email) stay identical
regardless of which path rotated the password.

### Backend

Register `rootAuthProtected.POST("/change-password", authHandler.ChangePassword)`
in [server.go](server/internal/app/server.go:570), next to the other `/me` and
2FA protected routes.

Request body (camelCase, per the repo API conventions):

```json
{ "currentPassword": "…", "newPassword": "…" }
```

Service behaviour, `(s *Service) ChangePassword(ctx, userUID, req)`:

1. Load the user; reject with `NOT_FOUND` if absent.
2. **If the user already has a password** (`user.PasswordHash != nil && != ""`):
   `currentPassword` is required and must satisfy `passwords.Verify`. On
   mismatch return `UNAUTHORIZED` with a distinct machine code
   (`INVALID_CURRENT_PASSWORD`) so the UI can point the error at the right
   field. Always run the verify even when the field is empty, to keep the
   timing flat.
3. **If the user has no password** (SSO-only): `currentPassword` is ignored and
   the call *sets* an initial password. This is the "set a password" case and
   must be reachable — otherwise SSO users stay locked to their IdP.
4. Validate `newPassword` against `minPasswordLength`
   ([service.go:2090](server/internal/handlers/auth/service.go:2090)) —
   reuse the same constant and the same message shape as `ResetPassword`, do
   not fork a second rule.
5. Reject a `newPassword` identical to the current one (`VALIDATION_ERROR`);
   silently accepting a no-op rotation is misleading.
6. Hash with `passwords.Hash` and persist via `db.UpdateUser`.
7. Revoke the user's other refresh tokens with `revokeRefreshTokensForUser`,
   mirroring the reset flow's comment about preserving `TokenTypePAT`. **The
   caller's own session must survive** — a user changing their password from
   the settings page should not be bounced to the login screen. Either
   re-issue the caller's tokens in the response, or exclude
   `Claims.RefreshUID` from the revocation sweep; pick one and cover it with a
   test that asserts the caller's session still works and a second session's
   does not.
8. Send the existing `password-changed.html` confirmation email
   ([service.go:2594](server/internal/handlers/auth/service.go:2594)) — same
   template, same `ChangedAt` payload.

Rate-limit the endpoint per user so it can't be used to brute-force the current
password from a stolen but unprivileged session token; the reset flow's
`bumpCounter` helper ([service.go:2496](server/internal/handlers/auth/service.go:2496))
is the pattern to follow.

### Frontend

Add a **Password** card to
[`account.security.tsx`](web/dash0/src/routes/orgs/$org/account.security.tsx),
placed above the Passkeys card, using the primitives from the design reference
([design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx)) —
`Card` / `CardHeader` / `CardTitle` with a `KeyRound`-family icon, and the
standard form + `Button` pattern. It must be usable on mobile.

Drive it off the already-fetched `me.hasPassword`:

- `hasPassword === true` → three fields: current password, new password,
  confirm new password. Title "Change password".
- `hasPassword === false` → two fields (new + confirm), no current-password
  field. Title "Set a password", with a line of copy explaining the account
  currently signs in through an identity provider.

Client-side: confirm-match check and the min-length check before submitting;
map `INVALID_CURRENT_PASSWORD` to an inline error on the current-password field
rather than a page-level `Alert`; `toast.success` on completion and
`refreshAll()` so `hasPassword` flips. Add `data-testid` attributes consistent
with the neighbouring cards (`password-current-input`,
`password-new-input`, `password-confirm-input`, `password-submit-button`).

Translations go in all four locales under `web/dash0/src/locales/{en,fr,de,es}`
in the `account:security.password.*` namespace, alongside the existing
`security.passkeys.*` / `security.totp.*` keys.

### Docs & tests

- Document the endpoint in
  [`openapi.yaml`](server/internal/app/openapi/openapi.yaml) next to
  `/api/v1/auth/reset-password` ([openapi.yaml:664](server/internal/app/openapi/openapi.yaml:664)),
  including the new error code.
- Go table-driven tests in `server/internal/handlers/auth/` covering: happy
  path, wrong current password, missing current password when one is set,
  new password too short, new == current, the SSO set-password path, the
  caller-session-survives / other-sessions-revoked split, and the rate limit.
- A Playwright E2E in `web/dash0/e2e/` in the style of the existing
  `account-*.spec.ts` files: change the password, confirm the success toast,
  then log in again with the new password.
