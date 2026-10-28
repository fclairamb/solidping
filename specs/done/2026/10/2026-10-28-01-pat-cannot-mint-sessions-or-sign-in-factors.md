---
model: opus
effort: high
---

# An org-bound PAT can mint a session in the owner's other orgs and add sign-in factors

## Problem

Found by the security scan of 2026-10-28 (finding X1).

**Severity: high.** Anyone holding a single unscoped personal access token can trigger it. The leak might be a CI secret, a token pasted in a script, or a device-flow token phished for one org (the V1 scenario). The result is a session in every org the owner belongs to, plus sign-in factors that outlive the token.

A PAT is bound to the org it was created in, and the server enforces that binding. `ValidatePATToken` puts that org in `claims.OrgSlug` (`server/internal/handlers/auth/service.go:1659-1674`). `AuthorizeOrgAccess` refuses any other org (`server/internal/handlers/auth/orgaccess.go:63-65`). The device-flow consent page has the user pick the org the token is for. The `/auth/*` routes behind plain `RequireAuth` (`server/internal/app/server.go:833-869`) do not respect that binding:

1. **`POST /auth/switch-org` turns the PAT into a full session in any org of its owner.**
   - The handler passes only `claims.UserUID` (`server/internal/handlers/auth/handler.go:509-533`).
   - `SwitchOrg` checks membership in the target org and mints an access token plus a refresh token there (`service.go:2152-2200`).
   - Reproduced on a local test-mode server: a PAT created in org `test3` (role `user`) got 403 on `test2` routes. `switch-org` to `test2` then returned an access and refresh token with role `admin` in `test2`, and that token read `test2` checks (200).
   - The refresh token keeps the session alive after the PAT is revoked.
2. **`POST /auth/device/consent` mints a PAT for any org of the owner.**
   - `RespondToDeviceConsent` takes the org from the request body (`server/internal/handlers/auth/device_service.go:272-288`).
   - `CreatePAT` checks only membership (`service.go:2033-2043`), never the caller's own `claims.OrgSlug`.
   - So the holder opens a device request, approves it with the PAT, and receives a PAT for another org.
3. **`POST /auth/passkeys/register/begin` and `/finish` add a passkey to the account.**
   - Neither step needs anything beyond `RequireAuth` (`server/internal/handlers/auth/passkey_handler.go:64-103`, `passkey_service.go:192-233`).
   - The attacker controls their client, so a script can complete the WebAuthn ceremony with a software key.
   - Passkey sign-in counts as MFA and skips TOTP (`passkey_service.go:408`). It signs into every org of the account.
   - It survives PAT revocation and password reset, and no email tells the owner that a passkey was added.
4. **Other account-security routes accept a PAT too:**
   - `POST /auth/change-password` sets a password with no current password when the account has none, i.e. an SSO-only user (`service.go:3290-3299`).
   - `POST /auth/2fa/setup` and `/confirm` enable 2FA on an account without it, with the holder's own secret.
   - `DELETE /auth/2fa`: since #693 every call spends the shared login 2FA budget, wrong code or not (`service.go:4870-4878`, key shared with `Verify2FA` at `:4769`). A holder who sends 5 wrong codes every 15 minutes keeps the owner's TOTP and recovery-code sign-in at 429 indefinitely. That stops the owner from signing in to revoke the stolen token, which is the remediation the V1 approval email asks for.

Precedent: impersonation tokens are already denied exactly this class of routes by `impersonationForbiddenRoutes` (`server/internal/handlers/auth/impersonation_guard.go:49-91`). `startSession` also refuses to mint any session from an impersonated request (`service.go:926-934`). A PAT gets neither guard. The MCP-credential guard (`middleware/auth.go:77-86`, spec 2026-10-16-12) states the same rule for MCP tokens: they "must not mint a PAT or a session".

Not in scope: a PAT minting another PAT in **its own** org (`POST /orgs/:org/tokens`), with or without an expiry. The dashboard offers "never expires", and the new token is bound to the same org with the same role.

## Proposal

1. Mark PAT-derived claims so that guards can recognise them. One option is an unexported field or a `TokenKind` set in `ValidatePATToken` (`service.go:1659`). Reusing the `pat_` prefix check from `middleware/auth.go:215` at the guard is acceptable too.
2. Add a PAT denylist next to the impersonation one, and enforce it in `RequireAuth` (`server/internal/middleware/auth.go:55-130`) with a 403 and a dedicated code (e.g. `PAT_FORBIDDEN`). Start from `impersonationForbiddenRoutes` and keep at least:
   - `POST /auth/switch-org`, `POST /orgs` (org creation mints an owner session), `POST /auth/accept-invite`;
   - `POST /auth/change-password`, `PATCH /auth/me` when it changes the sign-in email;
   - `POST /auth/2fa/setup`, `POST /auth/2fa/confirm`, `DELETE /auth/2fa`;
   - `POST /auth/passkeys/register/begin`, `POST /auth/passkeys/register/finish`;
   - `POST /auth/device/consent`;
   - the Discord identity link and the Telegram/Discord paging links. A bound Discord identity signs in (same reasoning as the impersonation list).
3. Defence in depth at the choke point: make `startSession` (`service.go:918`) refuse when the request's credential is a PAT, the way it refuses impersonation. A session-minting route added later then cannot reopen this.
4. CLI (`server/pkg/cli/auth.go:342-390`): `sp auth switch-org` on a PAT login must print a clear message instead of a bare 403. The message should say "this token is bound to org X; create a token in the target org or log in with `sp auth login`".
5. Send an email to the account owner when a passkey is registered. Reuse the security-notice layout of `device-token-created.html`.
6. Docs: `web/docs/docs/cli.md` and the API tokens page say a PAT is bound to one org and cannot change the account's sign-in methods.

## Tests

- `server/internal/handlers/auth` (both engines), table-driven over the denylist. With a PAT created in org A, for a user who is admin of orgs A and B:
  - **Regression (fails before the fix):**
    - `switch-org` to B returns 403 and mints no refresh token (row count unchanged);
    - device consent with `org: B` mints no PAT;
    - passkey `register/begin` returns 403;
    - `DELETE /auth/2fa` returns 403 and does not touch the 2FA attempt counter.
  - **Positive controls:**
    - the same calls made with a session JWT from a password login still succeed;
    - the PAT still reads org A routes;
    - the PAT can still create a PAT in org A.
- A route-table test, like the impersonation and MCP ones, proving that every denylist entry names a real route.
- `startSession` unit test: called in a PAT request context, it returns the new error.
- CLI test: `switch-org` with a PAT config prints the guidance message.
- Email: registering a passkey enqueues the notice for the owner; registering with a session still works.

## Open questions

- Should a PAT be allowed to call `switch-org` at all, for the CLI convenience? **Recommended: no.** A token is bound to one org on purpose: the device consent page asks the user which org it is for. The CLI asks for a new token instead.
- Should a session's `DELETE /auth/2fa` keep sharing the login budget (spec 2026-10-27-02)? **Recommended: yes.** Once PATs are refused there, only a session can spend it, and a session holder can already do worse. A separate counter key would reopen the parallel-budget question.

## Resolved open questions

- A PAT must not call `switch-org` at all; the CLI asks for a new token. (autopilot: recommended answer)
- A session's `DELETE /auth/2fa` keeps sharing the login budget. (autopilot: recommended answer)
