---
model: opus
effort: high
---

# Run an instance with OIDC as the only way in: no local login, admins granted by an IdP group claim, no seeded password admin

GitHub Issue: https://github.com/fclairamb/solidping/issues/942

## Source

A user on the feddit.org project post (comment https://feddit.org/comment/16144435, 2026-11-18),
filed by Florent: automatic user creation via SSO, the option to switch local login off
entirely, entering the OIDC settings before the first start, and an "admin-group claim" so an
IdP group makes its members administrators, which makes an OIDC-only instance possible.

The report is right that two pieces are missing (disabling local login, the admin-group
claim). It is partly wrong on the first-boot point: OIDC can already be configured before the
first start, through the `SP_OIDC_*` env vars. What still breaks an OIDC-only first boot is
the seeded local super admin.

## Problem

What exists:

- **Auto-provisioning**: `OIDCOAuthService.findOrCreateUser`
  (`server/internal/handlers/auth/oidc_service.go:359`) creates the user on the first SSO login
  and links the `oidc` provider. It never touches `SuperAdmin` or any org role: a provisioned
  user gets whatever the org's autojoin/invite rules give, never admin rights.
- **Claim mapping**: `mapClaims` (`oidc_service.go:310`) reads email/name/avatar only, with
  the names from `config.OIDCOAuthConfig` (`server/internal/config/oidc_oauth.go:14-37`). No
  groups claim. LDAP and SAML have no group-to-role mapping either.
- **Pre-boot config**: every OIDC field is a system parameter with an env var
  (`server/internal/systemconfig/systemconfig.go:989-1075`, `SP_OIDC_ENABLED`,
  `SP_OIDC_ISSUER_URL`, ...), with env > DB parameter > default precedence. So point 3 of the
  issue is already possible for OIDC itself.

What is missing:

1. **No switch for local password login.** `POST /api/v1/auth/login`, `/register`,
   `/confirm-registration`, `/request-password-reset`, `/reset-password`
   (`server/internal/app/server.go:826-831`) are always mounted, and `Service.Login`
   (`server/internal/handlers/auth/service.go:751`) always checks the bcrypt hash.
   `ProvidersHandler.ListProviders` (`server/internal/handlers/auth/providers_available.go:48`)
   lists the SSO providers but says nothing about the password form, so the login page
   (`web/dash0/src/routes/login.tsx`) always shows it.
2. **No admin-group claim.** Nothing maps an IdP group to `users.super_admin` or to an org
   role (`models.MemberRoleOwner`/admin).
3. **The first boot seeds a local password admin.** `job_startup.go:340-393` creates a
   `SuperAdmin` with a password (`seedAdminPassword`,
   `server/internal/jobs/jobtypes/job_startup_admin_password.go:40`: `SP_ADMIN_PASSWORD`, the
   public default in test/demo, a random one otherwise) as `owner` of the default org. On an
   OIDC-only instance that account is either a dormant credential or, if local login is off,
   unusable, and nobody else is admin until someone edits the DB.

## Why a spec

- Correctness hinges on **authorization scope**: a claim granting super admin is a privilege
  escalation path if it is read from the wrong token, trusted from an unverified source, or
  never revoked.
- New **system parameters** (config/API surface) and a change to the public providers
  endpoint consumed by the frontend.
- Several subsystems: auth handlers, startup seeding job, OIDC service, system config, login UI.
- **Product decisions** the reporter didn't settle (see Open questions).

## Proposal

### 1. `auth.local_login.enabled` (env `SP_LOCAL_LOGIN_ENABLED`, default `true`)

- New system parameter in `systemconfig.go`, applied to a new `config.AuthConfig` field.
- When `false`: `Login`, `Register`, `ConfirmRegistration`, `RequestPasswordReset`,
  `ResetPassword` answer `403` with a dedicated error code (`LOCAL_LOGIN_DISABLED`, added to
  `docs/apis/005_errors.md` if present); password change on the profile is hidden/refused. The
  routes stay mounted (a runtime DB toggle must not need a restart). LDAP bind falls under the
  same switch (it is a password form too) unless Open question 2 decides otherwise.
- Passkeys, the OAuth/OIDC/SAML providers, API tokens, device flow and the MCP OAuth server
  keep working.
- **Lock-out guard**: refuse to set it to `false` (super-admin config UI and API) unless at
  least one SSO provider is configured and enabled; at runtime, if it is `false` but no SSO
  provider is available, log a loud warning at boot and keep local login on (fail open for
  availability, documented). Recommended over failing closed; see Open question 1.
- `ListProviders` gains `localLogin: bool`; the login page hides the email/password form, the
  "forgot password" and "sign up" links when it is false (`data-testid` on each), and, with a
  single SSO provider, shows only its button.

### 2. Admin-group claim

New OIDC parameters (env + DB, same pattern as `KeyOIDCEmailClaim`):

- `auth.oidc.groups_claim` (`SP_OIDC_GROUPS_CLAIM`, default `groups`): claim holding an array
  of strings (also accept a single string, and a space-separated string).
- `auth.oidc.super_admin_groups` (`SP_OIDC_SUPER_ADMIN_GROUPS`): comma-separated group names
  whose members are super admins. Empty = feature off (today's behaviour).
- `auth.oidc.org_admin_groups` (`SP_OIDC_ORG_ADMIN_GROUPS`): comma-separated groups granting the
  `admin` role in the org being logged into (`orgSlug` of `HandleCallback`), joining the user
  to it if needed.

Behaviour, in `HandleCallback` after `findOrCreateUser`:

- Read groups only from the **validated ID token** (already verified by `providerAndVerifier`),
  optionally merged with the userinfo endpoint response; never from a query parameter or state.
- **Sync on every login**, both ways, but only for flags the claim itself granted: record the
  source (`super_admin_source = 'oidc'` or a `user_providers` attribute; see Open question 3)
  so that removing the user from the IdP group revokes super admin at the next login, while a
  super admin granted locally is never demoted by the claim.
- Never demote the last super admin of the instance; log and keep it.
- Audit-log every grant and revoke (existing `audit.go` helpers) and emit the change in the
  login response diagnostics.
- Same mapping for SAML (attribute) in a later spec; out of scope here.

### 3. OIDC-only first boot

- When local login is disabled at first boot **and** `auth.oidc.super_admin_groups` is set,
  `job_startup.go` does not create the password super admin: it creates the default org with
  no member, and the first OIDC user in a super-admin group becomes its `owner` (and super
  admin) on login. Log the expected bootstrap at boot ("waiting for the first SSO login from
  group X").
- Otherwise keep today's seeding unchanged.
- Document the env-only OIDC-only setup (docker-compose example) in the self-hosting docs and
  `wiki/` page for SSO.

### Tests

- Go: `Login`/`Register`/password-reset return 403 `LOCAL_LOGIN_DISABLED` when off, 200 when on;
  passkey login still works when off.
- Go: lock-out guard (no SSO provider = local login stays on, warning logged; API refuses
  the toggle).
- Go: `mapClaims` groups parsing (array, string, space-separated, missing, wrong type).
- Go: grant on first login, revoke on removal from group, local super admin untouched, last
  super admin never demoted, org admin role in the login org.
- Go: startup job skips the password admin in OIDC-only mode, keeps it otherwise; first group
  user becomes owner of the default org.
- Playwright: login page without the password form when `localLogin` is false.

## Open questions

1. **Local login off but no working SSO provider**: fail open (keep local login, warn) or
   fail closed (instance unreachable)? *Recommended: fail open at boot, refuse the toggle in
   the API.* Trade-off: fail open can silently re-enable a password path an operator wanted
   gone; fail closed can lock everybody out after an IdP misconfiguration.
2. **Does the switch also cover LDAP?** *Recommended: yes, a separate
   `auth.ldap.enabled` already exists for operators who want LDAP only.* Trade-off: an
   operator using LDAP + OIDC with "local login off" might expect LDAP to stay.
3. **Where to record that super admin came from the claim**: a new column on `users`
   (migration) or an attribute on the `user_providers` row? *Recommended: a nullable
   `super_admin_source` column, explicit and queryable.* Trade-off: a migration on `users`
   (both Postgres and SQLite).
4. **Break-glass access** on an OIDC-only instance when the IdP is down: rely on
   `SP_LOCAL_LOGIN_ENABLED=true` + `SP_ADMIN_PASSWORD` recovery at restart? *Recommended: yes,
   document it; no hidden back door.*

## Closing the issue

This spec closes #942. The implementing PR body **must** carry a `Closes #942` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is closed
and, if the merge did not close it, close it by hand:

    gh issue close 942 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
