---
model: opus
effort: medium
---

# Let a user change their own email address, and a super-admin change anyone's

GitHub Issue: https://github.com/fclairamb/solidping/issues/463

## Source

A self-hosted user (v0.36.1, Docker, SQLite) reports that the seeded super-admin `admin@solidping.io`
cannot have its email changed, short of editing the database. New admin users can be created,
but the initial account's address is stuck. They ask whether the feature is missing.

The report is right: no API endpoint and no dashboard page lets anyone change an email address,
their own or someone else's.

## Problem

- The first-start seed creates `defaults.Email` (`admin@solidping.io`, a constant in
  `server/internal/defaults/defaults.go:22`) with `SuperAdmin = true` and `MustChangePassword = true`
  (`server/internal/jobs/jobtypes/job_startup.go:338-365`). Nothing makes that email configurable.
- `PATCH /api/v1/auth/me` (`server/internal/app/server.go:786`, handler
  `server/internal/handlers/auth/handler.go:291`) only accepts `name`: `UpdateProfileRequest`
  (`server/internal/handlers/auth/service.go:479-481`) and `UpdateProfile` (`service.go:1750-1752`).
  The OpenAPI schema matches (`server/internal/app/openapi/openapi.yaml`, `UpdateProfileRequest`).
- Updating an org member (`server.go:1676`) only changes `role`
  (`server/internal/handlers/members/service.go:101-103`).
- The super-admin user directory is read-only: `GET /api/v1/system/users` plus impersonation
  (`server.go:1745-1752`, `server/internal/handlers/system/users.go`).
- dash0's `web/dash0/src/routes/orgs/$org/account.profile.tsx` never shows the email.
- Storage is already ready: `models.UserUpdate.Email` (`server/internal/db/models/auth.go:161`) is
  persisted by `server/internal/db/sqlite/sqlite.go:780` and `server/internal/db/postgres/postgres.go:854`,
  but no handler sets it. Emails are unique case-insensitively among non-deleted users
  (`server/internal/db/sqlite/migrations/001_v0_1_0.up.sql:49`).
- The only workarounds today are editing the DB, or making a new user `owner` and leaving the seeded
  account as the only super-admin (super-admin can't be granted by any code path besides the seed).

## Why a spec

- It needs new or changed API surface (`PATCH /auth/me` gains `email`, a new
  `PATCH /system/users/{uid}`), an OpenAPI change and `make generate`.
- It needs UI on two pages.
- Correctness hinges on authentication: the email is the login identifier, so changing it is an
  account-takeover vector if it doesn't require re-authentication.

## Proposal

### Self-service: `PATCH /api/v1/auth/me`

- `UpdateProfileRequest` gains optional `email` and `currentPassword`.
- When `email` is present and differs (case-insensitively) from the current one:
  - Normalize it the same way login and registration do (trim, lowercase if that is the existing rule), and validate
    the format. On failure return `400 VALIDATION_ERROR`.
  - If the account has a password, require `currentPassword` and verify it with the same code path as
    `ChangePassword` (`service.go:3112`). If it is missing or wrong, return `403`, reusing the existing invalid-current-password code.
  - If another non-deleted user already has the address, return `409 CONFLICT`. Also map the DB
    unique-constraint error to 409 for the race.
  - Save the email and clear `email_verified_at`.
  - Keep the current session. Revoke the user's other sessions and refresh tokens, the same way a password change does.
  - Send a notice to the **old** address saying the email was changed, when email sending is configured.
    If it isn't, log a warning.
- Accounts without a password (OAuth/OIDC/LDAP-only) can't change their email here. Return `403` with a clear
  detail telling them to change it at their identity provider.
- Refuse the change for impersonation tokens. Check the rule in `auth.IsImpersonationAllowed`, and add the
  route to the denied list if it isn't already there.
- Refuse the change for the demo user, the same way other self-service mutations do.

### Super-admin: `PATCH /api/v1/system/users/{uid}`

- The body is `{ "email": "..." }` for now. The request type must leave room for more fields later.
- Apply the same normalization, validation and 409 rules. Clear `email_verified_at` and revoke the target's sessions.
- No password is required from the target, because the caller is a super-admin.
- Record the change in the audit/event log if one exists for system actions.
- This endpoint is what fixes the reporter's case. A super-admin can also change their own email through it,
  though the self-service route works for them too.

### Dashboard

- `account.profile.tsx`: show the email, with an "Change email" form that asks for the new email and
  the current password. Show the 409 and 403 errors inline. The page must work on mobile.
- The system users page: add a row action (a `Pencil` ghost icon button) that leads to a dedicated
  edit route `/system/users/$uid`, not a modal, per the frontend conventions. It holds the email field.
- Add locale keys to every locale.

### Tests

- Go tests, table-driven:
  - self change succeeds with the correct password and clears `email_verified_at`
  - a wrong or missing password gives 403
  - a duplicate address, in a different case, gives 409
  - an invalid format gives 400
  - an OAuth-only account gives 403
  - an impersonation token is refused
  - the old sessions are revoked while the current one survives
  - the super-admin endpoint works, and a non-super-admin caller gets 403
  - login works with the new email and fails with the old one
- Playwright: change email from the profile page, log out, and log in with the new address.

### Docs

- Update the API reference through OpenAPI, and the user-facing account page in `web/docs/`.
- Add a line to the self-hosting docs: "change the seeded admin email from Account > Profile after
  first login".

## Open questions

1. **Verify the new address before switching?** Recommended: **no**. Switch immediately, clear
   `email_verified_at`, and notify the old address. Self-hosted installs often have no SMTP, and a
   confirmation link would make the feature unusable there. Trade-off: a typo can lock a user out,
   but the super-admin endpoint can fix it.
2. **Add `SP_ADMIN_EMAIL` / `SP_ADMIN_PASSWORD` for the first-start seed?** Recommended: **yes, email only**
   (`SP_ADMIN_EMAIL`, default `admin@solidping.io`). It is read in `job_startup.go:338` when no org
   exists. It helps new installs only, and the password stays `solidpass` + `must_change_password`.
   Trade-off: one more config key, but it goes through the koanf manual env reader for multi-word keys.

## Resolved open questions

Decided on the user's behalf during an unattended `/implement-todos` run (2026-09-30), taking the
spec's recommended answers. Listed in the run report for review.

1. **Do not verify the new address before switching.** Switch immediately, clear `email_verified_at`,
   and notify the old address when email sending is configured.
2. **Add `SP_ADMIN_EMAIL` (email only)** for the first-start seed, default `admin@solidping.io`, read in
   `job_startup.go` when no org exists, wired through the koanf manual env reader. No `SP_ADMIN_PASSWORD`:
   the seed password stays `solidpass` with `must_change_password`.

## Closing the issue

This spec closes #463. The implementing PR body **must** carry one `Closes #463` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is closed and, if
the merge did not close it, close it by hand:

    gh issue close 463 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
