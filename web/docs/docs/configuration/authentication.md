---
sidebar_position: 4
title: Authentication
---

# Authentication

SolidPing supports multiple authentication methods for user sign-in.

## Email/Password

The default authentication method. Users register with an email and password.

### Restricting Registration

Limit who can register by email pattern:

```bash
# Only allow company emails
SP_AUTH_REGISTRATION_EMAIL_PATTERN=@yourcompany\.com$

# Allow multiple domains
SP_AUTH_REGISTRATION_EMAIL_PATTERN=@(yourcompany|partner)\.com$
```

### Limiting Unconfirmed Sign-Ups per IP

Each sign-up sends a confirmation email to the address typed in the form. To stop a bot from using the form to send email to strangers, you can cap the sign-ups that come from one client IP:

```bash
# Unconfirmed sign-ups allowed per client IP over 24 hours (0 turns the limit off)
SP_AUTH_REGISTRATION_MAX_PENDING_PER_IP=10
```

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_AUTH_REGISTRATION_MAX_PENDING_PER_IP` | `0` (`10` in SaaS mode) | Sign-ups from one client IP that may wait for email confirmation, counting those submitted in the last 24 hours. While it is on, one client IP may also sign up at most once a minute. `0` turns both limits off. |

- The limit is off by default on a self-hosted instance and set to `10` in SaaS mode (`SP_DEPLOYMENT_MODE=saas`).
- Past either limit, the sign-up answers `429 RATE_LIMITED` and sends no email. The once-a-minute refusal carries a `Retry-After: 60` header.
- Confirming a sign-up frees its slot, so several colleagues behind one office network get through as long as they confirm.
- Signing up again with an address that is already waiting for confirmation does not take another slot. It sends a new link, at most once a minute.
- The limit is also the `auth.registration_max_pending_per_ip` system parameter. A stored value applies at the next start, and the environment variable wins over it.
- The client IP follows `SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES`. Behind a reverse proxy, set it, or every visitor shares the proxy's address.
- In test run mode (`SP_RUNMODE=test`) the limit is off unless the variable is set.

### JWT Configuration

```bash
# Set a strong secret for JWT signing (auto-generated if not set)
SP_AUTH_JWT_SECRET=your-secure-random-secret-at-least-32-chars
```

:::warning Production
Always set `SP_AUTH_JWT_SECRET` in production. If left unset, a random secret is generated once and stored in the database, so it is stable across restarts, but it is lost if the database is recreated, invalidating all existing sessions.
:::

### Password Hashing

You can choose the password-hashing algorithm and its cost parameters. The defaults reproduce SolidPing's historical profile exactly, so upgrading the binary changes nothing until you reconfigure it.

Two algorithms ship:

- **`argon2id`** (default) — memory-hard, the modern recommendation.
- **`bcrypt`** — CPU-hard with a tiny constant memory footprint.

```bash
# Algorithm: "argon2id" (default) or "bcrypt"
SP_AUTH_PASSWORD_ALGORITHM=argon2id

# argon2id cost parameters
SP_AUTH_PASSWORD_ARGON2_MEMORY=65536   # KiB (default 65536 = 64 MiB)
SP_AUTH_PASSWORD_ARGON2_TIME=3         # iterations (default 3)
SP_AUTH_PASSWORD_ARGON2_THREADS=4      # parallelism (default 4)
SP_AUTH_PASSWORD_ARGON2_KEY_LENGTH=32  # output bytes (default 32)
SP_AUTH_PASSWORD_ARGON2_SALT_LENGTH=16 # salt bytes (default 16)
SP_AUTH_PASSWORD_ARGON2_AUTO_PROFILE=true # small-host profile (default true)

# bcrypt cost (only used when algorithm is "bcrypt")
SP_AUTH_PASSWORD_BCRYPT_COST=12        # range 10–31 (default 12)

# Re-hash existing passwords on next sign-in (default true)
SP_AUTH_PASSWORD_REHASH_ON_LOGIN=true
```

### Small hosts

On a host with 2 GiB of memory or less (the cgroup limit when one is set, else
the host RAM), a single-process install (`SP_NODE_ROLE=all`, the default) uses
the OWASP minimum argon2id profile `m=19456, t=2, p=1` instead of
`m=65536, t=3, p=4`. The rules:

- It applies only when no argon2 cost field (`memory`, `time` or `threads`) is
  set in `config.yml`, an `SP_AUTH_PASSWORD_ARGON2_*` variable or Server
  Settings. An explicit value is never replaced.
- `SP_AUTH_PASSWORD_ARGON2_AUTO_PROFILE=false` (`auth.password.argon2.auto_profile`)
  turns it off and keeps 64 MiB.
- Split-role deployments (`SP_NODE_ROLE` other than `all`) never use it, so
  nodes of different sizes do not rehash a user back and forth.
- The server logs it once at boot. Existing 64 MiB hashes are re-minted at
  19 MiB on the next login, unless `SP_AUTH_PASSWORD_REHASH_ON_LOGIN=false`.

### Editing from the dashboard

Super-admins can change all of the above from **Server Settings → Password
Hashing** (`/orgs/{org}/server/hashing`) instead of editing YAML or environment
variables — pick the algorithm, set its cost parameters (recommended-profile
preset buttons are provided for argon2id), and toggle re-hash on sign-in.
Changes are validated immediately and **take effect after a server restart**,
matching every other credential setting. A field left blank inherits the server
default. Values configured via YAML/env are not shown in the form (only stored
overrides appear). An out-of-range value is rejected at save, and a malformed
stored value can never prevent startup — the policy re-resolve at boot is
non-fatal and keeps the prior policy.

**Transparent rehash-on-login.** Stored hashes are self-identifying, so changing the algorithm or its cost parameters never invalidates existing passwords — old hashes keep verifying. On a user's next successful login, if their stored hash no longer matches the configured policy it is transparently re-hashed and persisted. There is no forced password reset and no background migration: users who never log in keep their old (still-valid) hash. This upgrade is gated by `SP_AUTH_PASSWORD_REHASH_ON_LOGIN` (default `true`); set it to `false` to leave existing hashes untouched so only new passwords (new users, password changes/resets) use the new profile.

**Recommended profiles:**

| Algorithm | Parameters | Memory/login | Note |
|---|---|---|---|
| argon2id (default) | `m=65536, t=3, p=4` | 64 MiB | current; RFC 9106 memory-constrained profile |
| argon2id (lighter) | `m=19456, t=2, p=1` | 19 MiB | OWASP; drops 4-thread CPU contention |
| argon2id (min) | `m=9216, t=4, p=1` | 9 MiB | OWASP floor; still GPU-hostile |
| bcrypt | `cost=12` | ~4 KiB | constant; not memory-hard (weaker vs GPU/ASIC) |

:::note bcrypt and long passwords
The `bcrypt` algorithm pre-hashes passwords as `base64(sha256(password))` before hashing. This sidesteps bcrypt's 72-byte input limit and its truncation at embedded NUL bytes — SolidPing's bcrypt hashes are produced and consumed only by SolidPing, so this is fully self-consistent.
:::

:::warning Validation
The server validates the hashing policy at startup and **fails fast** on a misconfiguration (unknown algorithm; bcrypt cost outside `10–31`; argon2id memory below the `8192` KiB floor). Values below the OWASP-recommended floors are allowed but warn-logged. There is never a silent fallback.
:::

## Sign-up and organizations

Every check, status page and alert lives in an organization. When `auth.auto_personal_org` is on, a user who signs in and belongs to none is placed this way, in order:

1. a pending invitation for their email: they join that organization;
2. an organization whose registration email pattern matches their email: they join it;
3. a membership request they already sent: they wait for its answer on the "no organization" page;
4. otherwise SolidPing creates a personal organization for them ("Alice's organization", in their language) and makes them its owner. They land on its dashboard, with no form to fill.

The "no organization" page then only offers to join an existing organization when the user came through a join link or has a membership request. A user gets one personal organization: if they delete it, the next sign-in shows that page.

With the setting off, such a user lands on the "no organization" page, where they create an organization or ask to join one.

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_AUTH_AUTO_PERSONAL_ORG` | `true` when `SP_DEPLOYMENT_MODE=saas`, `false` otherwise | Create a personal organization for a user who signs in belonging to none. Also the `auth.auto_personal_org` system parameter, applied at startup |

## Changing an email address

A user changes their sign-in email from **Account > Profile**: enter the new address and the current password. A super admin changes anyone's email from **Server > Users**, with the pencil icon on the user's row, without the user's password.

When an email changes:

- the new address is marked unverified,
- every other session of the user is signed out (the session that made the change stays signed in),
- the previous address gets a notice, when [email sending](/configuration/notifications) is configured,
- `auth.email_changed` is recorded in the audit log of each organization the user belongs to.

An account without a password (it signs in through OAuth, OIDC, SAML or LDAP) cannot change its email in SolidPing. Change it at the identity provider, or ask a super admin. An address already used by another account is refused.

Over the API: `PATCH /api/v1/auth/me` with `{"email": "...", "currentPassword": "..."}`, or, as a super admin, `PATCH /api/v1/system/users/{uid}` with `{"email": "..."}`.

### Email of the first admin

On the first start of an empty database, SolidPing creates a super admin named `admin@solidping.io`. Set `SP_ADMIN_EMAIL` to seed it with your own address instead. It is only read when no organization exists yet. The password is still `solidpass` and must be changed at first login.

While that admin still signs in with `admin@solidping.io`, the first-login "set a new password" screen also asks for an email address and its confirmation. Fill both to change the address in the same step as the password, or leave them empty to keep it. The new address is unverified, `auth.email_changed` is recorded, and nothing is sent to `admin@solidping.io` (that address is not yours). Over the API, add `"email"` to the `POST /api/v1/auth/change-password` body; it is refused with `400` once the account has another address.

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_ADMIN_EMAIL` | `admin@solidping.io` | Email of the super admin created on the first start |

## OAuth Providers

SolidPing supports OAuth2 authentication with major identity providers. Set both `_CLIENT_ID` and `_CLIENT_SECRET` to enable each provider.

### Google

```bash
SP_GOOGLE_CLIENT_ID=your-client-id.apps.googleusercontent.com
SP_GOOGLE_CLIENT_SECRET=your-client-secret
```

**Setup:**
1. Go to [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
2. Create a new OAuth 2.0 Client ID
3. Set authorized redirect URI to `{SP_BASE_URL}/api/v1/auth/google/callback`

### GitHub

```bash
SP_GITHUB_CLIENT_ID=your-github-client-id
SP_GITHUB_CLIENT_SECRET=your-github-client-secret
```

**Setup:**
1. Go to [GitHub Developer Settings](https://github.com/settings/developers)
2. Create a new OAuth App
3. Set authorization callback URL to `{SP_BASE_URL}/api/v1/auth/github/callback`

### GitLab

```bash
SP_GITLAB_CLIENT_ID=your-gitlab-client-id
SP_GITLAB_CLIENT_SECRET=your-gitlab-client-secret
```

**Setup:**
1. Go to GitLab → Preferences → Applications
2. Create a new application
3. Set redirect URI to `{SP_BASE_URL}/api/v1/auth/gitlab/callback`
4. Select scopes: `read_user`, `openid`

### Microsoft

```bash
SP_MICROSOFT_CLIENT_ID=your-microsoft-client-id
SP_MICROSOFT_CLIENT_SECRET=your-microsoft-client-secret
SP_MICROSOFT_TENANT_ID=common
```

`SP_MICROSOFT_TENANT_ID` selects the Entra (Azure AD) tenant embedded in the
authorize/token URLs (`https://login.microsoftonline.com/{tenant}/oauth2/v2.0/...`).
Accepted values: your directory (tenant) **GUID**, a **verified domain**
(e.g. `contoso.onmicrosoft.com`), or one of `common` / `organizations` /
`consumers`. Leave it unset (or empty) to default to `common` (multi-tenant).
Set it to your tenant for a single-tenant app registration, which rejects
`common` sign-ins. This is also settable from **Server Settings → Authentication**
and takes effect after a server restart.

**Setup:**
1. Go to [Azure App Registrations](https://portal.azure.com/#blade/Microsoft_AAD_RegisteredApps)
2. Register a new application
3. Add redirect URI: `{SP_BASE_URL}/api/v1/auth/microsoft/callback`
4. Create a client secret under "Certificates & secrets"
5. For a single-tenant app, set `SP_MICROSOFT_TENANT_ID` to your directory (tenant) ID
6. For a multi-tenant app (`common` / `organizations` / `consumers`), open
   **Token configuration → Add optional claim → ID** and add the `email` and
   `xms_edov` claims.

**Which email SolidPing trusts.** Signing in with Microsoft links to an existing
account with the same email, or creates one, only when SolidPing can trust the
address:

- **Pinned tenant** (`SP_MICROSOFT_TENANT_ID` set to a GUID or domain): the
  Microsoft Graph `mail` (or the user principal name) is trusted. Only your own
  directory's users can sign in, and its admins are yours. Nothing else to configure.
- **Multi-tenant** (`common`, `organizations`, `consumers`, or unset): Graph `mail`
  is a directory attribute any tenant admin can set to any address, so it is never
  used. The email comes from the ID token, and only when its `xms_edov` claim
  (email domain owner verified) is `true`. Without the step 6 optional claims, users
  whose Microsoft account is already linked still sign in, but every first sign-in
  is refused with `EMAIL_NOT_VERIFIED`. The server logs a warning with the tenant
  (`tid`) and object (`oid`) IDs.

### Slack

"Sign in with Slack" reuses the same Slack app you configure for [notifications](/configuration/notifications#slack).

```bash
SP_SLACK_CLIENT_ID=1234567890.1234567890123
SP_SLACK_CLIENT_SECRET=your-client-secret
```

**Setup:**
1. In your [Slack app](https://api.slack.com/apps), enable OpenID Connect / "Sign in with Slack"
2. Add redirect URL: `{SP_BASE_URL}/api/v1/auth/slack/callback`

#### Workspace members join their organization automatically

An organization created from a Slack workspace stays linked to that workspace.
Slack only completes the OAuth exchange for a member of the workspace, so
SolidPing treats a successful "Sign in with Slack" (or app install) as proof of
membership: the user joins the linked organization as a regular **user** and
lands straight in the dashboard, without an admin approving a request first.

The link is matched on the workspace ID Slack returns — never on a workspace
name or on the organization named in the login URL — so members of one
workspace can never reach another workspace's organization. Everything else
still applies: an organization at its member limit, or one whose workspace link
was removed, falls back to the normal membership-request flow, and the first
person in an empty organization still becomes its admin.

Single- and multi-channel **guests** of the workspace are admitted as well, as
Slack does not expose guest status during sign-in.

##### Turning it off

Auto-join can be disabled per organization with the
`registration.slack_workspace_auto_join` parameter. When it is `false`,
workspace members need an invitation, a matching `registration.email_pattern`,
or an approved membership request, exactly as before.

There is **no API or dashboard control for this parameter yet** — a settings-UI
toggle is a follow-up. Today it is written directly in the database, in the
organization-scoped `parameters` table (`organization_uid` = the organization's
`uid`, `key` = `registration.slack_workspace_auto_join`, `value` = the JSON
object `{"value": false}`):

```sql
-- PostgreSQL; on SQLite replace gen_random_uuid() with any unique UID string.
INSERT INTO parameters (uid, organization_uid, key, value)
VALUES (
  gen_random_uuid(),
  (SELECT uid FROM organizations WHERE slug = 'your-org'),
  'registration.slack_workspace_auto_join',
  '{"value": false}'
);
```

The parameter is read on every Slack sign-in, so the change takes effect
immediately — no restart. If it holds a value SolidPing cannot read as a
boolean (`"off"`, `"no"`, …), auto-join is treated as **disabled** and a
warning naming the organization and the value is logged, so a typo'd switch
never silently lets people in.

### Discord

```bash
SP_DISCORD_CLIENT_ID=your-discord-client-id
SP_DISCORD_CLIENT_SECRET=your-discord-client-secret
```

**Setup:**
1. Go to the [Discord Developer Portal](https://discord.com/developers/applications)
2. Create an application and open **OAuth2**
3. Add redirect URL: `{SP_BASE_URL}/api/v1/auth/discord/callback`

Set `SP_DISCORD_ENABLED=true` too: the login routes are not mounted without it.

If you also want the Discord bot (the **Install Discord bot** button), the same
application needs a second redirect URI and a few more settings. See
[Discord application setup](notifications.md#discord-application-setup).

### After the provider sends you back

Whatever the provider, the callback never puts your session in the URL. It
stores the session server-side under a one-time code and sends the browser to
`{SP_BASE_URL}/d/auth/complete?code=…`. The dashboard trades that code for the
session with `POST /api/v1/auth/handoff/exchange`, then continues to the page
you started from.

The code works once and expires after 60 seconds. Only its SHA-256 is stored.
So a sign-in URL that ends up in browser history, a proxy log or a screenshot
cannot be replayed. If you reverse-proxy SolidPing, nothing needs to change:
`/d/auth/complete` is an ordinary dashboard page.

### Provider switches

Each provider also has an `_ENABLED` switch. The login routes of a provider are mounted only when it is on and its client ID is set.

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_GOOGLE_ENABLED` | `false` | Turns on Google sign-in |
| `SP_GITHUB_ENABLED` | `false` | Turns on GitHub sign-in |
| `SP_GITLAB_ENABLED` | `false` | Turns on GitLab sign-in |
| `SP_MICROSOFT_ENABLED` | `false` | Turns on Microsoft sign-in |
| `SP_SLACK_ENABLED` | `false` | Turns on Sign in with Slack |
| `SP_SLACK_SOCKET_MODE_ENABLED` | `false` | Receive Slack events over Socket Mode instead of HTTPS webhooks (needs `SP_SLACK_ENABLED`). See [notifications](notifications.md) |
| `SP_SLACK_APP_TOKEN` | empty | Slack app-level token (`xapp-...`) used by Socket Mode. Secret |

## Session lifetime

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_AUTH_SESSION_MAX_DURATION` | `0` (unlimited) | Hard cap on a session's lifetime, in seconds, counted from login. An organization can override it |

## LDAP / Active Directory

SolidPing can check passwords against an LDAP directory: lldap, OpenLDAP, FreeIPA or Active Directory. There is one global connector for the whole server.

**How it works.** There is no separate LDAP button: users type their identifier and password in the normal sign-in form.

- `POST /api/v1/auth/login` checks the local password first. It falls back to LDAP only for a user who has no local password.
- SolidPing searches the directory with the service account (`bind_dn`; an anonymous search when it is blank), then binds as the entry it found with the submitted password. An empty password is never sent.
- A successful bind creates the user on first sign-in. If an account with the same email already exists and has no local password (it came from OIDC, SAML or an earlier LDAP sign-in), the directory identity is linked to it.
- An account that already has a local password can never be taken over through LDAP: it keeps signing in with its local password.
- Every failure (directory unreachable, unknown user, wrong password, ambiguous filter) answers the same `401`, so the form does not reveal who exists in the directory.

### Configuring it

Three places, with this precedence: **environment variable > dashboard (database) > `config.yml`**.

- Environment variables, in the table below.
- The dashboard: **Server settings > Authentication > LDAP** (`/orgs/{org}/server/auth`, super admin only). The page flags a setting that an environment variable overrides.
- The `ldap:` block of `config.yml`, shown at the end of this page.

A change takes effect after a server restart.

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_LDAP_ENABLED` | `false` | Turns LDAP sign-in on |
| `SP_LDAP_SERVER_URL` | empty | Directory address, e.g. `ldaps://dc.acme.com:636` or `ldap://lldap:3890` |
| `SP_LDAP_START_TLS` | `false` | Upgrade a plain `ldap://` connection to TLS before binding. Ignored for `ldaps://` |
| `SP_LDAP_INSECURE_SKIP_VERIFY` | `false` | Skip TLS certificate verification. Local testing only |
| `SP_LDAP_BIND_DN` | empty | Service account used to search for the user. Blank means an anonymous search |
| `SP_LDAP_BIND_PASSWORD` | empty | Password of the service account. **Secret** |
| `SP_LDAP_BASE_DN` | empty | Where the search starts, e.g. `ou=people,dc=acme,dc=com` |
| `SP_LDAP_USER_FILTER` | `(mail=%s)` | Search filter, with exactly one `%s` for the submitted identifier |
| `SP_LDAP_EMAIL_ATTRIBUTE` | `mail` | Attribute read as the user's email |
| `SP_LDAP_NAME_ATTRIBUTE` | `cn` | Attribute read as the user's display name |

### TLS

Credentials cross the wire in clear text over plain `ldap://`. Use `ldaps://` (usually port 636), or `ldap://` with `SP_LDAP_START_TLS=true`. `SP_LDAP_INSECURE_SKIP_VERIFY=true` accepts any certificate and is meant for testing against a self-signed directory only.

### The user filter

`user_filter` must contain exactly one `%s`. SolidPing escapes the submitted identifier (RFC 4515) before substituting it, so a user cannot inject filter syntax. Composite filters are fine:

```text
(&(objectClass=person)(mail=%s))
```

The filter has to match one entry. If it matches several, sign-in fails. Whatever it matches on is what the user types in the form: `(mail=%s)` means an email address, `(uid=%s)` a username.

### Example: lldap

```bash
SP_LDAP_ENABLED=true
SP_LDAP_SERVER_URL=ldap://lldap:3890
SP_LDAP_BIND_DN=uid=admin,ou=people,dc=acme,dc=com
SP_LDAP_BIND_PASSWORD=the-lldap-admin-password
SP_LDAP_BASE_DN=ou=people,dc=acme,dc=com
SP_LDAP_USER_FILTER=(mail=%s)   # or (uid=%s) to sign in with the username
```

lldap exposes `mail` and `cn`, so the attribute defaults work. When you use `(uid=%s)`, the user still needs a `mail` value in lldap, because that is what SolidPing stores.

### Example: Active Directory

```bash
SP_LDAP_ENABLED=true
SP_LDAP_SERVER_URL=ldaps://dc.acme.com:636
SP_LDAP_BIND_DN=CN=solidping,OU=Service Accounts,DC=acme,DC=com
SP_LDAP_BIND_PASSWORD=the-service-account-password
SP_LDAP_BASE_DN=DC=acme,DC=com
SP_LDAP_USER_FILTER=(sAMAccountName=%s)
SP_LDAP_NAME_ATTRIBUTE=displayName
```

The service account only needs read access to the users. Users then sign in with their `sAMAccountName`; the `mail` attribute must be filled in.

### Troubleshooting

Every failure is a `401` on purpose, so the browser tells you nothing. Set `SP_LOG_LEVEL=debug`: the server then logs `LDAP authentication failed` with the cause (dial error, service account bind refused, no entry found, user bind refused).

Check the directory side with `ldapsearch`, using the same values:

```bash
# 1. Can the service account bind and find the user?
ldapsearch -H ldap://lldap:3890 -D "uid=admin,ou=people,dc=acme,dc=com" -w "$BIND_PASSWORD" \
  -b "ou=people,dc=acme,dc=com" "(mail=alice@acme.com)" mail cn

# 2. Does the user's own password bind? (use the DN returned by step 1)
ldapwhoami -H ldap://lldap:3890 -D "uid=alice,ou=people,dc=acme,dc=com" -w "alice-password"
```

Typical causes: the filter matches no entry or several, the base DN is too narrow, the email attribute is empty, a certificate the server does not trust (see TLS), or the user already has a local password (they must then use it).

## Member roles

Every member of an organization holds one of four roles, ordered
**owner > admin > user > viewer**. They are hierarchical: an owner can do
everything an admin can, an admin everything a user can, and so on.

| Role | What it can do |
|---|---|
| `owner` | Everything an admin can, **plus** deleting the organization, renaming it, and granting or revoking ownership |
| `admin` | Full read/write on the organization's resources, plus member management |
| `user` | Read/write on monitoring resources — checks, incidents, status pages, notifications |
| `viewer` | Read everything, change nothing — except their own notification settings and their own API tokens |

`viewer` is genuinely read-only: every state-changing API call is refused with
`403 FORBIDDEN`, including acknowledging or resolving an incident, which changes
what the whole team is paged about. A viewer may still choose where *they*
personally get notified, and mint their own API token — that token inherits
their role, so it can automate reading and nothing more.

The rule is enforced from the member's current role, not from the token they are
holding: demote someone to `viewer` and their very next request stops writing,
without waiting for a sign-out or a token refresh. It applies to the REST API,
personal access tokens and the [MCP server](../features/mcp.md) alike.

If you want someone to acknowledge incidents, give them `user`.

## Two-Factor Authentication (2FA)

Users can secure their accounts with **TOTP** two-factor authentication (compatible with Google Authenticator, Authy, 1Password, etc.). 2FA is enabled per user from account settings — no server configuration is required.

- Enrollment generates a TOTP secret (shown as a QR code) that the user confirms with a one-time code.
- On confirmation, SolidPing issues **recovery codes** to use if the authenticator device is lost.
- At login, users with 2FA enabled provide a code from their authenticator app (or a recovery code).
- The code is asked on every sign-in: password, LDAP, and every SSO / OAuth connector (Google, GitHub, GitLab, Microsoft, Discord, Slack, OIDC, SAML, and the Slack app install). After the identity provider sends the user back, the dashboard shows the code step, and no session exists until the code is accepted.
- Passkeys are the only sign-in that skips it, because a passkey already is a second factor.
- SolidPing does not rely on the identity provider's own MFA (OIDC `amr`, SAML `AuthnContextClassRef`): a user who enabled 2FA is always asked.

## Configuration File

```yaml
auth:
  jwt_secret: your-secure-random-secret
  registration_email_pattern: "@yourcompany\\.com$"
  registration_max_pending_per_ip: 10 # 0 turns the limit off (default: 10 in SaaS mode, else 0)
  password:
    algorithm: argon2id # or "bcrypt"
    argon2:
      memory: 65536 # KiB
      time: 3
      threads: 4
      key_length: 32
      salt_length: 16
      auto_profile: true # small-host profile, see "Small hosts"
    bcrypt:
      cost: 12

google:
  client_id: your-google-client-id
  client_secret: your-google-client-secret

github:
  client_id: your-github-client-id
  client_secret: your-github-client-secret

gitlab:
  client_id: your-gitlab-client-id
  client_secret: your-gitlab-client-secret

microsoft:
  client_id: your-microsoft-client-id
  client_secret: your-microsoft-client-secret

slack:
  client_id: your-slack-client-id
  client_secret: your-slack-client-secret

discord:
  client_id: your-discord-client-id
  client_secret: your-discord-client-secret

ldap:
  enabled: true
  server_url: ldaps://dc.acme.com:636
  start_tls: false
  insecure_skip_verify: false
  bind_dn: CN=solidping,OU=Service Accounts,DC=acme,DC=com
  bind_password: the-service-account-password
  base_dn: DC=acme,DC=com
  user_filter: "(sAMAccountName=%s)"
  email_attribute: mail
  name_attribute: displayName
```
