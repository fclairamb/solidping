---
model: opus
effort: high
---

# Super admins cannot sign in as another user to reproduce what they see

## Problem
Support and debugging need "what does this user see?". Today a super admin can only reach other orgs (`SwitchOrg` gives them `RoleSuperAdmin` on any org, `server/internal/handlers/auth/service.go:2024`), never see the dashboard as a specific user, with that user's role and membership. The only workaround is a password reset, which locks the real user out and leaves no trail.

Impersonation is an authentication and permission feature: a bug here is a privilege escalation. It must be super-admin only, time-boxed, audited, and impossible to chain or to use for account takeover.

## Proposal
1. **Endpoint** `POST /api/v1/system/users/:uid/impersonate` on the existing super-admin group (`server/internal/app/server.go:1725`, next to `systemActions.GET("/users", ...)` at `:1745`), so it is behind `RequireAuth` + `RequireSuperAdmin`. Body: `{ "orgSlug": "..." }` (optional, defaults to the target's first org). Response: same shape as `LoginResponse` (access token only, **no refresh token**).
2. **Service method** `Service.Impersonate(ctx, actorUID, targetUID, orgSlug, authContext)` in `server/internal/handlers/auth/service.go`, modelled on `SwitchOrg` (`:2024`). Refusals, each a distinct typed error mapped to 403/404:
   - target is a super admin (no lateral moves);
   - target is the actor;
   - the actor's current credential is itself an impersonation token (no chaining);
   - target is the demo user (`users.demo`);
   - target is disabled/deleted or not a member of `orgSlug`.
3. **Token claims**: add `ImpersonatedBy string \`json:"impersonatedBy,omitempty"\`` to `Claims` (`service.go:190`). `UserUID` = target, `Role` = the target's real membership role (never `RoleSuperAdmin`), so all existing permission checks run as the target. Short fixed expiry (recommend 30 min), no refresh: when it expires the admin is back to their own session. Do not create a `user_tokens` refresh row, so the target's session list (`isCurrent`, "sign out others") is untouched.
4. **Middleware** (`server/internal/middleware/auth.go`): when claims carry `ImpersonatedBy`, expose it via a `GetImpersonatorFromContext`, and:
   - `RequireSuperAdmin` must **deny** (the user in context is the target, and must not inherit the actor's super-admin rights; verify `user.SuperAdmin` is read from the target row, which step 2 already guarantees is false);
   - block the credential-changing surface: change-password, 2FA setup/removal, passkey add/remove, PAT/API-token creation, email change, account delete. Answer `403 IMPERSONATION_FORBIDDEN`. Reuse the demo write-guard pattern in `RequireAuth` (`Demo` claim, see `service.go:206`) rather than a second mechanism.
5. **Audit** (`server/internal/audit`, helpers in `server/internal/handlers/auth/audit.go`): record `auth.impersonation_started` (actor = admin, target user, org, IP/UA, expiry) at issue time. Every audit row written while an impersonation token is in use must carry the real actor (`impersonatedBy`) so the trail never attributes admin actions to the target alone. Add `AuthMethodImpersonate` next to `AuthMethodSwitchOrg`.
6. **Dashboard** (`web/dash0`, start from `design-reference.tsx` per CLAUDE.md):
   - `web/dash0/src/routes/orgs/$org/server.users.tsx`: per-row ghost icon button "Impersonate" (super admin only, hidden for super-admin rows and self), with a confirm dialog naming the target and stating that the session is audited and lasts 30 min.
   - Persistent, non-dismissible banner while impersonating ("You are viewing as X. Exit"), mobile-friendly. "Exit" restores the admin's own session: keep the admin's original access token in memory/sessionStorage (never in the URL) and swap it back; expiry does the same.
   - Add `en/fr/es/de` keys in `web/dash0/src/locales/*/server.json`, plus the new hook in `web/dash0/src/api/hooks.ts`.
7. **OpenAPI + docs**: document the endpoint in `server/internal/app/openapi/openapi.yaml`, add a short section to the docs site (security page), and a `CHANGELOG.md` entry per `wiki/conventions/changelog.md`.
8. **Kill switch**: config `auth.impersonation_enabled` (default `true`; when `false` the endpoint answers 404). Remember the koanf multi-word env quirk (memory: `project_koanf_env_quirk`), so add the manual `SP_*` reader.

## Tests
- `server/internal/handlers/auth/impersonate_test.go` (table-driven, SQLite):
  - super admin impersonates a normal member: token has `userUid`=target, `role`=member role (not super admin), `impersonatedBy`=actor, expiry <= 30 min, no refresh token returned, no `user_tokens` row created;
  - **negative**: non-super-admin caller gets 403; unauthenticated gets 401; target is a super admin, target is self, target is demo user, target not a member of `orgSlug`, unknown target: each refused with the right status;
  - **negative**: chaining, an impersonation token calling the endpoint again is refused;
  - kill switch off returns 404.
- `server/internal/middleware/impersonation_test.go`: an impersonation token calling a `RequireSuperAdmin` route gets 403 even though the actor is a super admin (positive control: the actor's own token passes); change-password, 2FA, passkey, PAT creation, email change and account deletion return `IMPERSONATION_FORBIDDEN`; an ordinary read (list checks) works and sees only the target's org data.
- Audit test: `auth.impersonation_started` row exists with actor and target; a write done under the token records `impersonatedBy` on its audit row.
- Postgres variant of the service test (`*_postgres_test.go`, same pattern as `register_anti_enum_postgres_test.go`).
- `web/dash0/e2e/`: super admin impersonates from the users table, banner shows, exit returns to the admin; a normal user never sees the button.
- `web/dash0` unit test: all four locales carry the new keys.

## To verify
- The exact audit API to stamp `impersonatedBy` on every downstream row (`server/internal/audit/audit.go`: `ActorFromContext` looks like the hook).
- Whether PAT-validated claims and `Claims` copies elsewhere (`generateAccessToken`, `service.go` ~`:2176`) need the new field threaded through.
- The list of credential-changing routes to block (grep `server.go` around `rootAuthProtected`, `:785`).

## Open questions
- Should impersonation be usable when the target has 2FA enabled? Recommended: yes, the admin's own authentication is what gates it (the admin already passed their own 2FA), and the target's 2FA is not bypassed for their real sessions.
- Should the target be notified (email) that they were impersonated? Recommended: no for v1, the audit log is the record; revisit if enterprise customers ask.

## Resolved open questions
- Impersonation is allowed when the target has 2FA enabled. The admin's own authentication gates it, and the target's 2FA is not bypassed for their real sessions.
- The target is not notified by email in v1. The audit log is the record.
