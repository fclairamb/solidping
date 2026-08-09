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

### JWT Configuration

```bash
# Set a strong secret for JWT signing (auto-generated if not set)
SP_AUTH_JWT_SECRET=your-secure-random-secret-at-least-32-chars
```

:::warning Production
Always set `SP_AUTH_JWT_SECRET` in production. If left unset, a random secret is generated on each restart, invalidating all existing sessions.
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

# bcrypt cost (only used when algorithm is "bcrypt")
SP_AUTH_PASSWORD_BCRYPT_COST=12        # range 10–31 (default 12)

# Re-hash existing passwords on next sign-in (default true)
SP_AUTH_PASSWORD_REHASH_ON_LOGIN=true
```

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

## Two-Factor Authentication (2FA)

Users can secure their accounts with **TOTP** two-factor authentication (compatible with Google Authenticator, Authy, 1Password, etc.). 2FA is enabled per user from account settings — no server configuration is required.

- Enrollment generates a TOTP secret (shown as a QR code) that the user confirms with a one-time code.
- On confirmation, SolidPing issues **recovery codes** to use if the authenticator device is lost.
- At login, users with 2FA enabled provide a code from their authenticator app (or a recovery code).

## Configuration File

```yaml
auth:
  jwt_secret: your-secure-random-secret
  registration_email_pattern: "@yourcompany\\.com$"
  password:
    algorithm: argon2id # or "bcrypt"
    argon2:
      memory: 65536 # KiB
      time: 3
      threads: 4
      key_length: 32
      salt_length: 16
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
```
