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
```

**Setup:**
1. Go to [Azure App Registrations](https://portal.azure.com/#blade/Microsoft_AAD_RegisteredApps)
2. Register a new application
3. Add redirect URI: `{SP_BASE_URL}/api/v1/auth/microsoft/callback`
4. Create a client secret under "Certificates & secrets"

### Slack

"Sign in with Slack" reuses the same Slack app you configure for [notifications](/configuration/notifications#slack).

```bash
SP_SLACK_CLIENT_ID=1234567890.1234567890123
SP_SLACK_CLIENT_SECRET=your-client-secret
```

**Setup:**
1. In your [Slack app](https://api.slack.com/apps), enable OpenID Connect / "Sign in with Slack"
2. Add redirect URL: `{SP_BASE_URL}/api/v1/auth/slack/callback`

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
