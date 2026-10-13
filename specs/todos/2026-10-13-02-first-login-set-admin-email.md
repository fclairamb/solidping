---
model: opus
effort: high
---

# Ask for a real email on the forced first-login password screen, while the seeded admin still has the default address

## Problem

The first start of an empty database seeds `admin@solidping.io` as a super admin with
`must_change_password` (`server/internal/jobs/jobtypes/job_startup.go:340-371`, address from
`defaults.Email`, `server/internal/defaults/defaults.go:22`). The first login lands on the forced
rotation screen (`web/dash0/src/routes/change-password.tsx`), which asks for a new password only.
Many self-hosters rotate the password and never change the email.

That leaves a super admin on an address nobody on the install controls:

- Password reset (`POST /auth/forgot-password`, `service.go:2805`) mails a reset link to
  `admin@solidping.io`. The domain's MX is the maintainer's mail server, not the operator's, and the
  endpoint is public.
- Every install that keeps the default shares the same super-admin login name.
- The email change shipped in spec 2026-09-30-08 (Account > Profile, `SP_ADMIN_EMAIL`) works, but the
  docs only ask users to use it "after first login" and nothing prompts them.

The forced rotation screen is the one moment the user's attention is guaranteed and cannot be skipped,
and it already collects the current password, which an email change requires. It is the right place
to ask for the email.

**Scope: that screen only.** No persistent banner, no migration that forces existing installs through
the screen (a new blocking condition in `RequireAuth` would lock out their tokens and scripts on
upgrade). Installs that already got past first login are out of scope.

## Proposal

1. **Signal.** Add `mustChangeEmail bool` (`json:"mustChangeEmail,omitempty"`) to `UserInfo`
   (`server/internal/handlers/auth/service.go:296-318`), set in `newUserInfo` (`service.go:~340`) to
   `user.MustChangePassword && strings.EqualFold(user.Email, defaults.Email)`. It is computed from the
   row, never keyed on "is the seeded admin" beyond the address comparison. It therefore rides on login
   responses and `/auth/me` like `mustChangePassword`. A seed with `SP_ADMIN_EMAIL` set, an operator-forced
   reset on an ordinary user, or an account that already has a real address never sees it. Update
   `UserInfo` in `server/internal/app/openapi/openapi.yaml` (next to `mustChangePassword`, ~10603 and
   ~18334) and run `make generate`.

2. **One call.** `ChangePasswordRequest` (`service.go:3144`, OpenAPI `ChangePasswordRequest` at
   `openapi.yaml:10747`) gains an optional `email`. In `ChangePassword` (`service.go:3175`), when `email`
   is non-empty:
   - Accept it only while the signal in step 1 holds for the caller. Otherwise answer
     `400 VALIDATION_ERROR` (email change elsewhere goes through `PATCH /auth/me`, which re-verifies the
     password and is the better-tested path; this field must not become a second way in).
   - `normalizeEmail` (`email_change.go:58`), then refuse an address equal (case-insensitively) to
     `defaults.Email` with `400 VALIDATION_ERROR`, then `ensureEmailAvailable` (`email_change.go:~96`,
     `409 CONFLICT`, plus `db.IsUniqueViolation` for the race).
   - Write `Email`, `ClearEmailVerifiedAt`, `PasswordHash` and `MustChangePassword=false` in the **same**
     `UpdateUser` as today (`service.go:3225-3230`), so a crash can never leave a new password with the
     old address, or the reverse. The password check, rate limit and "must differ" rules run first and
     are unchanged, so a wrong current password never changes the email.
   - Record `auth.email_changed` the way `recordEmailChanged` (`email_change.go:153`) does, with
     `changed_by = self`.
   - Omitting `email` keeps today's behaviour exactly (curl, CLI and API users are not broken).
   - The "password changed" confirmation email (`ChangePassword` tail) must go to the **new** address
     when one was set, not to the default one. Check the order of operations there.
   - Keep the existing refusals: demo user, impersonation tokens (see `## To verify`).

3. **No notice to the default address.** In `applyEmailChange` (`email_change.go:134-144`), skip the
   old-address "email changed" notice when the old address equals `defaults.Email` (it would be mailed to
   the maintainer's domain, and the new address of every self-hoster would leak to it). Log at debug
   instead of the existing warning. Applies to both the first-login path and Account > Profile.

4. **Dashboard.** In `web/dash0/src/routes/change-password.tsx`, when `user.mustChangeEmail`, render an
   email field and a "confirm email" field above the password fields. No hint text.
   what they sign in with from now on. Empty by default (never prefill the default address). Both are
   optional in the sense that leaving them empty submits no `email`, and the screen then behaves as
   today. Validate that the two match and that the value is not the default address before submitting;
   show the server's 400/409 inline in the existing `forced-password-error` alert. Pass it through
   `changePassword()` (`web/dash0/src/api/password.ts`). Add `mustChangeEmail?: boolean` to the user
   types in `web/dash0/src/contexts/AuthContext.tsx` (~195 and ~227). Use `data-testid`s
   `forced-email`, `forced-email-confirm`. Mobile layout unchanged (single column). Add the keys to
   all four locales (`auth.json` en/fr/de/es).

5. **Docs.** Update the "change the seeded admin email" lines in
   `web/docs/docs/installation/docker.md:145`, `web/docs/docs/intro.md:101` and
   `web/docs/docs/configuration/authentication.md:116-131` to say the first-login screen asks for it.
   Add a changelog entry per `wiki/conventions/changelog.md`. Optionally mention the email prompt in the
   `logFirstLogin` line (`server/internal/app/ready.go:44-60`).

## Tests

- `server/internal/handlers/auth` (table-driven, `t.Parallel()`, `testify/require`), next to the existing
  change-password tests:
  - flagged user on the default address, correct current password, valid new email: password, email and
    cleared `must_change_password` land together; `email_verified_at` is cleared; login works with the
    new email and the new password and fails with the old email; other sessions are revoked, the
    caller's survives; `auth.email_changed` is recorded; no notice is enqueued to the default address;
    the password-changed confirmation targets the new address.
  - same request with a wrong current password: 401 `INVALID_CURRENT_PASSWORD`, the email is unchanged.
  - email equal to the default (any case): 400, nothing changed. Invalid format: 400. Address already
    held by another user, different case: 409, password not rotated.
  - `email` sent by a user who is not in the signal state (already has a real address, or no
    `must_change_password`): 400, nothing changed.
  - `email` omitted: identical to today's behaviour (regression).
  - `/auth/me` and login responses carry `mustChangeEmail` for the seeded default and omit it when
    `SP_ADMIN_EMAIL` was set at seed time.
  - impersonation token and demo user are refused.
  - `applyEmailChange` from Account > Profile with the default as old address enqueues no email; with
    any other old address it still does (positive control).
- `web/dash0` unit test for `change-password.tsx`: email fields only render with `mustChangeEmail`;
  mismatch and default-address errors block submit; empty fields submit no `email`.
- `web/dash0/e2e`: fresh database, log in as the default admin, set a new password and a new email on the
  forced screen, land on the dashboard, sign out, sign in with the new email. Negative control: a run
  with `SP_ADMIN_EMAIL` set shows no email field.
- i18n parity test (`bun run test:unit`) for the new keys.

## To verify

- Whether `POST /auth/change-password` is already denied for impersonation tokens
  (`auth.IsImpersonationAllowed`) and the demo user (`demo_guard.go`); add it to the denied list if not.
- Where the "password changed" confirmation email is enqueued in `ChangePassword` (after
  `service.go:3240`) and which address it reads, to make it follow the new email.
- `ensureEmailAvailable`'s exact line and signature; reuse it as is.

## Resolved open questions

1. **Confirm field:** render a "confirm email" field that must match the email field, and show no hint text about the address being the sign-in name. Drop the hint from Proposal step 4 (keep only the two fields and their labels).
2. **Required or optional:** the email stays optional on the screen. Leaving both fields empty submits no `email` and the screen behaves as today. The server accepts omission.
