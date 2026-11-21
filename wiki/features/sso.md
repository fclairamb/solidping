# Single sign-on and OIDC-only instances

SolidPing signs users in with a local password (and LDAP), passkeys, and the
federated providers: Google, GitHub, GitLab, Microsoft, Discord, Slack, a
generic OIDC provider and SAML. This page covers how an instance runs with
OIDC as the only way in (spec 2026-11-21-02). The operator docs are in
`web/docs/docs/configuration/authentication.md`, the endpoints in
[api-specification/auth.md](../api-specification/auth.md).

## Auto-provisioning

`OIDCOAuthService.findOrCreateUser` (`server/internal/handlers/auth/oidc_service.go`)
creates the user on their first SSO sign-in and links the `oidc` provider.
Which org they join, and with which role, is the shared admission policy
(`Service.CompleteOrgLogin`, `join_policy.go`), unless the group claim below
says otherwise.

## Local login switch

`auth.local_login.enabled` (env `SP_LOCAL_LOGIN_ENABLED`, default `true`),
resolved per request by `systemconfig.ResolveLocalLogin` (env > DB parameter >
startup config), so a DB toggle needs no restart.

- Off: password and LDAP login, `/register`, `/confirm-registration`,
  `/request-password-reset`, `/reset-password`, `/change-password` answer
  `403 LOCAL_LOGIN_DISABLED` before reading the body. `/accept-invite` answers
  the same when it would create a password account or carries a `password`;
  an invitee signed in through SSO still accepts.
- Passkeys, the federated providers, API tokens, the device flow and the MCP
  OAuth server are not local login and keep working.
- `GET /auth/providers` returns `localLogin: false`: the login page hides the
  password form, the forgot-password and sign-up links.
- **Lock-out guard, fail open**: with no SSO provider configured and enabled,
  local login stays on and the server logs a warning at boot. The system
  parameter API refuses `false` while no SSO provider is enabled.

## Admins from the IdP group claim

OIDC parameters (env + DB):

| Parameter | Env | Default |
|-----------|-----|---------|
| `auth.oidc.groups_claim` | `SP_OIDC_GROUPS_CLAIM` | `groups` |
| `auth.oidc.super_admin_groups` | `SP_OIDC_SUPER_ADMIN_GROUPS` | empty (off) |
| `auth.oidc.org_admin_groups` | `SP_OIDC_ORG_ADMIN_GROUPS` | empty (off) |

The groups are read from the **verified ID token** (an array of strings, a
string, or a space-separated string), merged with the userinfo endpoint's
answer for the same subject and access token. Never from the request or the
OAuth state. `syncGroupRoles` (`oidc_groups.go`) applies them on every login,
after `findOrCreateUser` and before the session is minted.

Every grant records its source, and the claim only ever revokes what it
granted:

| Role | Source column | Grant | Revoke |
|------|---------------|-------|--------|
| Super admin | `users.super_admin_source = 'oidc'` | in a super-admin group | next login out of every super-admin group; never a local super admin (`NULL` source); never the last super admin of the instance |
| Org `admin` | `organization_members.role_source = 'oidc_group'` | in an org-admin group, in the org being signed in to (joins it if needed; a `user`/`viewer` is raised) | next login to that org out of every org-admin group: back to `user`; never an `owner`, never a role set locally, never the last owner/admin of the org |

Any role change made outside the claim (members page, invite) writes
`role_source = NULL`, so a role an admin set by hand becomes local and the
claim no longer touches it.

Each change is logged, audited (`auth.super_admin_changed`, `member.joined`,
`member.role_changed`, with `source: oidc_group`) and returned as login
diagnostics: `roleChanges` on `POST /auth/handoff/exchange` (carried by the
handoff code of the OIDC callback). Values: `super_admin_granted`,
`super_admin_revoked`, `super_admin_kept_last`, `org_owner_granted`,
`org_admin_granted`, `org_admin_revoked`, `org_admin_kept_last`.

SAML attribute mapping is out of scope (later spec).

## OIDC-only first boot

When local login is off at first boot **and** `SP_OIDC_SUPER_ADMIN_GROUPS` is
set, the startup job (`job_startup.go`) seeds no password admin: it creates
the default org with no member and logs "waiting for the first SSO login from
a super-admin group". While super-admin groups are set, the OIDC login skips
the "first member of an empty org becomes owner" rule, so only a super-admin
group member bootstraps it: they become super admin and `owner`. Otherwise the
seeding is unchanged.

Env-only setup, nothing to configure in the dashboard:

```yaml
services:
  solidping:
    image: ghcr.io/fclairamb/solidping:latest
    environment:
      SP_BASE_URL: https://solidping.acme.com
      SP_LOCAL_LOGIN_ENABLED: "false"
      SP_OIDC_ENABLED: "true"
      SP_OIDC_DISPLAY_NAME: Acme SSO
      SP_OIDC_ISSUER_URL: https://idp.acme.com/realms/acme
      SP_OIDC_CLIENT_ID: solidping
      SP_OIDC_CLIENT_SECRET: ${OIDC_CLIENT_SECRET}
      SP_OIDC_SCOPES: openid email profile groups
      SP_OIDC_SUPER_ADMIN_GROUPS: solidping-admins
      SP_OIDC_ORG_ADMIN_GROUPS: solidping-team-leads
```

The IdP client redirect URI is `https://solidping.acme.com/api/v1/auth/oidc/callback`.

## Break-glass access

Restart with `SP_LOCAL_LOGIN_ENABLED=true` and `SP_ADMIN_PASSWORD` set. On an
instance started OIDC-only, that boot creates the first admin
(`SP_ADMIN_EMAIL`) as super admin with that password, to change at first
login. There is no hidden back door.
