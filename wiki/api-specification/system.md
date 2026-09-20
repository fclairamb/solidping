# System

Server-wide configuration and operations. Everything here is **super-admin**
unless stated otherwise. Job observability under `/api/v1/system` is documented
in [jobs.md](jobs.md).

## Regions

### GET /api/v1/regions
List all available global regions. Auth: public

### GET /api/v1/orgs/:org/regions
List regions relevant to the organization. Auth: required

Private regions are created and managed separately — see [agents.md](agents.md).

## Public configuration

### GET /api/v1/config
Browser-safe public configuration read by the dashboard at boot, before login.
Auth: **public** (no token).

Deliberately a general-purpose blob, not a per-feature endpoint: new public
flags are added as sibling properties instead of minting new routes.

Contract:

- It exposes **only** non-secret values. No system parameter marked `secret`
  ever appears here — `posthog.personal_api_key` in particular is never read by
  the handler.
- A disabled feature **omits** its configuration fields rather than returning
  empty strings, so an operator can tell "unconfigured" from "configured with a
  blank value" at a glance.

```json
{ "posthog": { "enabled": true, "projectApiKey": "phc_…", "host": "https://eu.i.posthog.com" } }
```

With PostHog unconfigured (the default for every self-hosted install) the
response is exactly:

```json
{ "posthog": { "enabled": false } }
```

`posthog.enabled` in this response is the *resolved* state
(`posthog.enabled == true && posthog.project_api_key != ""`), which is the same
rule the backend capture client and the dashboard apply.

The document also carries the instance-level messaging capability flags, each
resolved the same way:

```json
{
  "whatsapp": { "enabled": true },
  "telegram": { "enabled": true, "botUsername": "solidping_bot" }
}
```

`telegram.enabled` is `telegram.enabled && bot_token != "" && bot_username != ""`
— the username is part of the rule because without it the dashboard cannot build
a connect link, so the feature would be half-on. `botUsername` is emitted **only
while enabled**; the bot token and the webhook secret never appear here.

## System parameters

### GET /api/v1/system/parameters
List all system parameters. Auth: super-admin

Response: `{ "data": [ … ], "envOverrides": ["posthog.host", …] }`.
`envOverrides` lists the parameter keys whose effective value is currently
forced by an `SP_*` environment variable (env beats the database), so the
Server Settings UI can warn that an edit to those keys will not take effect.
Only key names are listed — never values.

### GET /api/v1/system/parameters/:key
Get a system parameter by key. Auth: super-admin

### PUT /api/v1/system/parameters/:key
Set a system parameter. Auth: super-admin

### DELETE /api/v1/system/parameters/:key
Delete a system parameter. Auth: super-admin

### GET /api/v1/system/parameters/email_inbox/public
Public projection of the `email_inbox` parameter. Auth: **required only** — any
authenticated user, deliberately *not* super-admin. It exposes only
`addressDomain`, so the dashboard can render per-check email addresses without
surfacing the rest of the JMAP credentials. Registered on its own group ahead
of the super-admin group (`server/internal/app/server.go`).

## Email

### POST /api/v1/system/test-email
Send a test email to verify email configuration. Auth: super-admin

### GET /api/v1/system/email-inbox/config
Get the inbound email (JMAP) configuration. Auth: super-admin

### GET /api/v1/system/email-inbox/status
Get the inbound email connection/sync status. Auth: super-admin

### POST /api/v1/system/email-inbox/test
Test the inbound email connection. Auth: super-admin

### POST /api/v1/system/email-inbox/sync
Force an inbound email sync now. Auth: super-admin

## Operations

### GET /api/v1/system/activation
Activation funnel counters across the server. Auth: super-admin

### GET /api/v1/system/scheduling/lane-load
Current load per scheduling lane — used to diagnose an overloaded or unbalanced
scheduler. Auth: super-admin

## Users

### GET /api/v1/system/users
Search and page the global user directory. Auth: super-admin

The instance-level counterpart of a per-org member list: every user account,
regardless of which organization (if any) it belongs to, with its memberships
attached. Read-only — no write action lives here (promoting to super admin,
forcing a password reset, deleting or impersonating a user are all
out of scope for this endpoint).

Query parameters:

- `q` — case-insensitive substring match on email OR name. A literal `%` or
  `_` in the query is matched literally, never as a SQL wildcard.
- `limit` — page size, 1–200, default 50. Values above 200 are clamped, not
  rejected.
- `offset` — result offset.

Response:

```json
{
  "data": [
    {
      "uid": "…",
      "email": "alice@acme.com",
      "name": "alice",
      "avatarUrl": "",
      "superAdmin": false,
      "demo": false,
      "emailVerified": true,
      "totpEnabled": false,
      "mustChangePassword": false,
      "hasPassword": true,
      "lastActiveAt": "2026-09-18T10:12:00Z",
      "createdAt": "2026-08-01T09:00:00Z",
      "orgs": [
        { "uid": "…", "slug": "acmetech", "name": "Acme", "role": "admin", "joinedAt": "…" }
      ]
    }
  ],
  "total": 1
}
```

Each row is an explicit allow-list projection of the user record — it never
carries `passwordHash`, `totpSecret` or `totpRecoveryCodes`. `hasPassword` is
`passwordHash != nil`, i.e. whether the account can authenticate locally at
all versus being SSO/OAuth-only. `orgs` excludes a soft-deleted user (the row
itself is absent), a soft-deleted membership, and a membership whose
organization is soft-deleted; a user with no org has `orgs: []`.
