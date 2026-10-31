---
model: opus
effort: medium
pr: batch
automerge: false
---

# Adding a sign-in factor needs no re-authentication, so a hijacked session can lock out or keep the account

## Problem

Found by the security scan of 2026-10-31 (finding AA2).

**Severity: low.** It needs a session that is already hijacked: a stolen access or refresh token, or an unattended signed-in browser. With that, any signed-in user's account is exposed. No such hijack path is known today. The finding is about what a hijacked session can do once it exists.

Changing the password (`handlers/auth/service.go:3233` `ChangePassword`) and the sign-in email (`handlers/auth/email_change.go:230-238` `verifyEmailChangePassword`) both re-check the current password. Adding a sign-in factor does not:

- `POST /auth/2fa/setup` and `POST /auth/2fa/confirm` (`app/server.go:843-844`). `Setup2FA` (`service.go:4690`) and `Confirm2FA` (`service.go:4726`) check only the session's user UID and that TOTP is not yet enabled. `Confirm2FA` validates a code from the secret it just handed out, then returns the recovery codes to the caller.
- `POST /auth/passkeys/register/begin` and `/finish` (`app/server.go:853-854`, `passkey_service.go:192`, `:238`) carry only the session's user UID.

The codebase already treats these routes as sign-in factors: they are on the PAT denylist (`handlers/auth/pat_guard.go:50-58`) and the impersonation denylist. A plain session token passes both guards.

Impact:

- **TOTP: a silent lockout with no recovery.**
  - The hijacker enables 2FA with their own authenticator. No email is sent: the only factor notice template is `passkey-added.html`.
  - The victim's next sign-in stops at the 2FA challenge. A password reset does not clear TOTP.
  - `DELETE /auth/2fa` (`service.go:4898`) needs a current code, and the recovery codes went to the hijacker.
  - No admin or super-admin path clears a user's 2FA: the only writes of `totp_enabled` are `Confirm2FA`, `Disable2FA` and the demo reset job. Recovery means editing the database.
  - The hijacker cannot sign in either without the password. The impact is the loss of the account.
- **Passkey: persistence past every revocation.**
  - A passkey sign-in skips the password and TOTP (documented, passkey = MFA).
  - A passkey the hijacker registers therefore keeps them in after the victim resets their password, which revokes the sessions.
  - Mitigated by the `passkey-added` email (`handlers/auth/passkey_notice.go:29`), and the victim can delete it.
- **Variant, account with no password (signed up through an identity provider):** `ChangePassword` deliberately ignores `currentPassword` and *sets* a first password (`service.go:3224-3226`). A hijacked session of such an account can give itself a password sign-in. A `password-changed` email is sent.

## Proposal

1. Add one re-authentication helper in `handlers/auth`, used by every route that adds or replaces a sign-in factor:
   - an account with a password: the current password, checked through the shared change-password counter (`bumpChangePasswordCounter`), like `verifyEmailChangePassword`;
   - an account without a password: a recent primary sign-in (see Open questions).
2. Require it on `POST /auth/2fa/setup` (the secret is only handed out after re-authentication; `confirm` then needs the same proof or a short-lived setup token bound to it), and on `POST /auth/passkeys/register/begin`. `finish` is already bound to `begin` by the sealed session.
3. Apply the same rule to the first-password case of `ChangePassword` (`service.go:3224-3226`).
4. Send a `totp-enabled` notice email when 2FA is turned on, mirroring `passkey_notice.go`, so the change is visible like a passkey addition.
5. dash0: the 2FA setup, passkey registration and set-password dialogs ask for the current password (or send the user through a fresh sign-in when they have none).
6. Update `web/docs/docs` (account security page) and the OpenAPI spec for the new request field and error code.

## Tests

- `handlers/auth` regression tests (fail before the fix):
  - a session without the current password gets 401/403 from `2fa/setup` and `passkeys/register/begin`, and no TOTP secret is stored;
  - a wrong current password is refused and bumps the change-password counter; at the cap the route answers 429;
  - a passwordless account without a recent sign-in cannot set a first password.
- Positive controls:
  - with the right current password, setup, confirm and passkey registration succeed as today;
  - a passwordless account signed in within the window can set its first password, so SSO users are not locked to their IdP.
- Enabling 2FA enqueues the `totp-enabled` email to the account's address.
- The PAT and impersonation denylist tests still pass, unchanged.
- dash0 Playwright: the 2FA setup flow asks for the password, a wrong one shows the error, and the right one goes through.

## Open questions

- How does an account with no password re-authenticate? **Recommended:** require a primary sign-in (federated or passkey) within the last 10 minutes, read from a sign-in time carried by the session. If the session has no such time, the user signs in again through their provider. Do not accept a refresh-rotated token's issue time.
- Should a super admin get a "reset 2FA" action for a locked-out user? **Recommended:** yes, in a separate spec: audited, with an email to the user, refused under impersonation and PATs. It is the recovery path missing today whatever the cause.
