# Third-Party Authentication (Google, Microsoft, GitHub)

## Overview

Add OAuth 2.0 authentication for **Google**, **Microsoft**, and **GitHub** — extending the existing Slack OAuth that's already fully implemented.

The Slack implementation (`slack.go`, `slack_service.go`) establishes the patterns: OAuth state via `state_entries`, user lookup/creation via `user_providers`, org mapping via `organization_providers`, membership via `organization_members`, and token generation via `GenerateTokensForOAuth()`. The new providers follow the same architecture.

**What already exists** (from Slack implementation):
- `user_providers` table — links users to external identities (multi-provider per user)
- `organization_providers` table — links orgs to external provider identities
- `organization_members` table — membership with roles (admin/user/viewer)
- `state_entries` table — OAuth CSRF state with TTL
- `GenerateTokensForOAuth()` — generates JWT + refresh token for OAuth logins
- `SlackOAuthHandler` / `SlackOAuthService` — reference implementation
- `ProviderType` constants: `google`, `github`, `microsoft` already defined in `models/auth.go`
- Global users (no `auth_provider_uid`, no org on user)

**What needs to be built**:
1. Google OAuth handler + service
2. Microsoft OAuth handler + service
3. GitHub OAuth handler + service
4. Config: env vars for each provider's client ID / secret
5. Frontend: "Sign in with ..." buttons on login page
6. Identity management API (list/unlink linked providers)

## Architecture

Each provider follows the same pattern as Slack:

```
┌──────────────┐     ┌──────────────────┐     ┌─────────────────┐
│  Handler     │────>│  Service          │────>│  Auth Service   │
│  (HTTP layer)│     │  (business logic) │     │  (token gen)    │
│              │     │                   │     │                 │
│ Login()      │     │ GenerateOAuthState│     │ GenerateTokens  │
│ Callback()   │     │ ValidateOAuthState│     │   ForOAuth()    │
│              │     │ HandleCallback()  │     │                 │
│              │     │ findOrCreateOrg() │     │                 │
│              │     │ findOrCreateUser()│     │                 │
│              │     │ ensureMembership()│     │                 │
└──────────────┘     └──────────────────┘     └─────────────────┘
```

### Key Difference from Slack: Org-Scoped Login

Slack creates organizations automatically (Slack team = org). For Google/Microsoft/GitHub, the user selects an org first, then signs in. The OAuth flow is org-scoped:

```
GET /api/v1/auth/google/login?org=acme&redirect_uri=/dashboard
```

The `org` slug is stored in the OAuth state. On callback, the user is added to that org (if auto-provisioning is enabled).

**Slack**: Provider creates the org (team ID → org mapping)
**Google/Microsoft/GitHub**: User chooses the org, provider authenticates them into it

## Provider Details

| Provider | Auth URL | Token URL | Scopes | User Info | ID Field |
|----------|----------|-----------|--------|-----------|----------|
| **Google** | `accounts.google.com/o/oauth2/v2/auth` | `oauth2.googleapis.com/token` | `openid email profile` | OIDC `id_token` or `/userinfo` | `sub` |
| **Microsoft** | `login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize` | `login.microsoftonline.com/{tenant}/oauth2/v2.0/token` | `openid email profile` | OIDC `id_token` or `/userinfo` | `sub` (or `oid`) |
| **GitHub** | `github.com/login/oauth/authorize` | `github.com/login/oauth/access_token` | `read:user user:email` | `api.github.com/user` | `id` (numeric) |

## Configuration

Add provider configs alongside existing `SlackConfig`:

```go
// In config.go

type Config struct {
    // ... existing fields ...
    Google    GoogleOAuthConfig    `koanf:"google"`
    Microsoft MicrosoftOAuthConfig `koanf:"microsoft"`
    GitHub    GitHubOAuthConfig    `koanf:"github"`
}

type GoogleOAuthConfig struct {
    ClientID     string `koanf:"client_id"`
    ClientSecret string `koanf:"client_secret"`
}

type MicrosoftOAuthConfig struct {
    ClientID     string `koanf:"client_id"`
    ClientSecret string `koanf:"client_secret"`
    Tenant       string `koanf:"tenant"` // default: "common"
}

type GitHubOAuthConfig struct {
    ClientID     string `koanf:"client_id"`
    ClientSecret string `koanf:"client_secret"`
}
```

### Environment Variables

```bash
# Existing
SP_SERVER__BASE_URL=https://solidping.k8xp.com  # Already exists

# Google
SP_GOOGLE__CLIENT_ID=123456.apps.googleusercontent.com
SP_GOOGLE__CLIENT_SECRET=GOCSPX-...

# Microsoft
SP_MICROSOFT__CLIENT_ID=...
SP_MICROSOFT__CLIENT_SECRET=...
SP_MICROSOFT__TENANT=common                          # default: "common"

# GitHub
SP_GITHUB__CLIENT_ID=Iv1.abc123
SP_GITHUB__CLIENT_SECRET=...
```

A provider is available if its `client_id` and `client_secret` are both non-empty.

## Database Changes

**None required.** The existing schema handles everything:

| Need | Existing table | How |
|------|---------------|-----|
| OAuth CSRF state | `state_entries` | Key: `oauth_state:{provider}:{nonce}`, TTL: 10 min |
| User ↔ provider link | `user_providers` | `provider_type` = `google`/`microsoft`/`github` |
| Org ↔ provider link | `organization_providers` | For Microsoft tenant → org mapping (optional) |
| Membership | `organization_members` | Role-based org access |
| Tokens | `user_tokens` | JWT + refresh via `GenerateTokensForOAuth()` |

## OAuth Flow

### Google / GitHub (Org-Scoped)

```
Browser                     SolidPing Backend              Identity Provider
  │                              │                              │
  │  1. GET /auth/google/login   │                              │
  │     ?org=acme                │                              │
  │     &redirect_uri=/dashboard │                              │
  │─────────────────────────────>│                              │
  │                              │  2. Store state with org     │
  │  3. 302 → provider           │                              │
  │<─────────────────────────────│                              │
  │                              │                              │
  │  4. User authenticates       │                              │
  │────────────────────────────────────────────────────────────>│
  │<────────────────────────────────────────────────────────────│
  │                              │                              │
  │  5. GET /auth/google/callback│                              │
  │     ?code=...&state=...      │                              │
  │─────────────────────────────>│                              │
  │                              │  6. Exchange code            │
  │                              │─────────────────────────────>│
  │                              │  7. Get user profile         │
  │                              │─────────────────────────────>│
  │                              │                              │
  │                              │  8. Lookup user_providers    │
  │                              │  9. Create user if needed    │
  │                              │  10. Ensure membership       │
  │                              │  11. GenerateTokensForOAuth  │
  │                              │                              │
  │  12. 302 → redirect_uri      │                              │
  │      ?access_token=...       │                              │
  │      &refresh_token=...      │                              │
  │      &org=acme               │                              │
  │<─────────────────────────────│                              │
```

### Microsoft (Optional: Tenant → Org Mapping)

Microsoft can optionally work like Slack — mapping Azure AD tenants to orgs via `organization_providers`:
- If `organization_providers` has a row for `(microsoft, tenant_id)` → use that org
- Otherwise → require `org` param like Google/GitHub

### OAuth State

Stored in `state_entries` table (same pattern as Slack):

```go
const oauthStatePrefix = "oauth_state:google:"  // or microsoft:, github:

state := OAuthState{
    Nonce:       nonce,
    RedirectURI: redirectURI,
    OrgSlug:     orgSlug,     // NEW: which org to join (not in Slack flow)
    CreatedAt:   time.Now().Unix(),
}
```

## REST API

### OAuth Endpoints (Public, no auth)

#### Initiate Login
```
GET /api/v1/auth/{provider}/login?org={org_slug}&redirect_uri={url}
```

| Provider | Path |
|----------|------|
| Google | `GET /api/v1/auth/google/login` |
| Microsoft | `GET /api/v1/auth/microsoft/login` |
| GitHub | `GET /api/v1/auth/github/login` |

**Query Parameters**:
| Param | Required | Description |
|-------|----------|-------------|
| `org` | Yes | Organization slug to authenticate into |
| `redirect_uri` | No | Frontend URL to redirect after auth (default: `/`) |

**Response**: `302 Redirect` to provider authorization URL

**Behavior**:
1. Verify provider is configured (has credentials)
2. Verify org exists
3. Generate state, store in `state_entries` with org slug + redirect URI
4. Redirect to provider's authorization URL

#### OAuth Callback
```
GET /api/v1/auth/{provider}/callback?code={code}&state={state}
```

**Response**: `302 Redirect` to frontend with tokens

**Behavior**:
1. Validate + consume state from `state_entries`
2. Extract org slug from stored state
3. Exchange code for access token
4. Fetch user profile (email, name, external ID)
5. Find or create user via `user_providers` lookup
6. Ensure membership in org (first user = admin, others = user)
7. `GenerateTokensForOAuth()` → JWT + refresh token
8. Redirect: `{redirect_uri}?access_token={jwt}&refresh_token={refresh}&org={slug}`

**Error Redirect**: `{redirect_uri}?error={code}&error_description={msg}`

### Available Providers (Public, no auth)
```
GET /api/v1/auth/providers
```

Returns which providers are configured (have credentials). Used by login page to show buttons.

**Response**:
```json
{
  "data": [
    {"type": "slack", "name": "Slack"},
    {"type": "google", "name": "Google"},
    {"type": "github", "name": "GitHub"}
  ],
  "emailPasswordEnabled": true
}
```

### Identity Management (Authenticated)

#### List My Identities
```
GET /api/v1/orgs/{org}/auth/identities
```

Returns the current user's linked providers.

**Response**:
```json
{
  "data": [
    {
      "uid": "...",
      "providerType": "google",
      "providerID": "1234567890",
      "metadata": {"email": "user@gmail.com"},
      "createdAt": "2026-01-15T10:00:00Z"
    },
    {
      "uid": "...",
      "providerType": "github",
      "providerID": "octocat",
      "metadata": {"login": "octocat"},
      "createdAt": "2026-02-01T14:30:00Z"
    }
  ]
}
```

#### Link New Identity
```
GET /api/v1/auth/{provider}/link?redirect_uri=/settings
```

Requires authentication (cookie or token). Initiates OAuth flow that links a new provider to the **current user** instead of creating a new account.

#### Unlink Identity
```
DELETE /api/v1/orgs/{org}/auth/identities/{identity_uid}
```

**Response**: `204 No Content`

**Validation**: Cannot unlink the last identity if the user has no password (prevents lockout).

## Backend Implementation

### New Files

```
back/internal/handlers/auth/
├── google.go              # Google OAuth handler
├── google_service.go      # Google OAuth service
├── microsoft.go           # Microsoft OAuth handler
├── microsoft_service.go   # Microsoft OAuth service
├── github.go              # GitHub OAuth handler
├── github_service.go      # GitHub OAuth service
├── identities.go          # Identity management handler (list, unlink)
├── providers_available.go # GET /auth/providers endpoint
```

### Service Pattern (follows Slack exactly)

Each provider has:
- **Handler**: HTTP layer (redirect, callback, error handling)
- **Service**: Business logic (state management, code exchange, user provisioning)

```go
// Example: GoogleOAuthService (same structure as SlackOAuthService)

type GoogleOAuthService struct {
    db          db.Service
    cfg         *config.Config
    authService *Service
}

func (s *GoogleOAuthService) HandleCallback(ctx context.Context, code, orgSlug string) (*OAuthResult, error) {
    // 1. Exchange code for tokens (Google-specific HTTP call)
    tokenResp, err := s.exchangeCode(ctx, code)

    // 2. Fetch user profile (Google userinfo endpoint)
    profile, err := s.fetchUserProfile(ctx, tokenResp.AccessToken)

    // 3. Verify org exists
    org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)

    // 4. Find or create user (via user_providers table)
    user, err := s.findOrCreateUser(ctx, profile)

    // 5. Ensure membership
    member, err := s.ensureMembership(ctx, org.UID, user.UID)

    // 6. Generate tokens
    return s.authService.GenerateTokensForOAuth(ctx, user, org, string(member.Role))
}
```

### Shared OAuth State

The `OAuthState` struct gains an `OrgSlug` field for org-scoped providers:

```go
type OAuthState struct {
    Nonce       string `json:"nonce"`
    RedirectURI string `json:"redirectUri"`
    OrgSlug     string `json:"orgSlug,omitempty"` // New: for Google/Microsoft/GitHub
    CreatedAt   int64  `json:"createdAt"`
}
```

State key pattern: `oauth_state:{provider}:{nonce}` (TTL: 10 minutes)

### Route Registration

```go
// In server.go — alongside existing Slack routes

// Google OAuth
if s.config.Google.ClientID != "" {
    googleService := auth.NewGoogleOAuthService(s.dbService, s.config, s.authService)
    googleHandler := auth.NewGoogleOAuthHandler(googleService, s.config)
    googleAuth := api.NewGroup("/auth/google")
    googleAuth.GET("/login", googleHandler.Login)
    googleAuth.GET("/callback", googleHandler.Callback)
}

// Microsoft OAuth
if s.config.Microsoft.ClientID != "" {
    msService := auth.NewMicrosoftOAuthService(s.dbService, s.config, s.authService)
    msHandler := auth.NewMicrosoftOAuthHandler(msService, s.config)
    msAuth := api.NewGroup("/auth/microsoft")
    msAuth.GET("/login", msHandler.Login)
    msAuth.GET("/callback", msHandler.Callback)
}

// GitHub OAuth
if s.config.GitHub.ClientID != "" {
    ghService := auth.NewGitHubOAuthService(s.dbService, s.config, s.authService)
    ghHandler := auth.NewGitHubOAuthHandler(ghService, s.config)
    ghAuth := api.NewGroup("/auth/github")
    ghAuth.GET("/login", ghHandler.Login)
    ghAuth.GET("/callback", ghHandler.Callback)
}

// Available providers endpoint
providersHandler := auth.NewProvidersHandler(s.config)
api.GET("/auth/providers", providersHandler.List)

// Identity management (authenticated)
orgProtected.GET("/auth/identities", identitiesHandler.List)
orgProtected.DELETE("/auth/identities/:identityUid", identitiesHandler.Unlink)
```

## Frontend

### Login Page

```
┌──────────────────────────────┐
│        Sign in to            │
│        SolidPing             │
│                              │
│  ┌────────────────────────┐  │
│  │    Sign in with Slack  │  │
│  └────────────────────────┘  │
│  ┌────────────────────────┐  │
│  │    Sign in with Google │  │
│  └────────────────────────┘  │
│  ┌────────────────────────┐  │
│  │    Sign in with GitHub │  │
│  └────────────────────────┘  │
│                              │
│  ──────── or ────────────    │
│                              │
│  Email:    [              ]  │
│  Password: [              ]  │
│                              │
│  [        Sign in         ]  │
└──────────────────────────────┘
```

1. Call `GET /api/v1/auth/providers` on load
2. Show buttons for configured providers
3. Slack button: `GET /api/v1/auth/slack/login?redirect_uri=/dashboard` (no org needed)
4. Other buttons: `GET /api/v1/auth/{provider}/login?org={slug}&redirect_uri=/dashboard`

### Settings Page: Linked Accounts

```
┌──────────────────────────────────────────┐
│  Linked Accounts                         │
│                                          │
│  Slack     user@slack.com       [Unlink] │
│  Google    user@gmail.com       [Unlink] │
│                                          │
│  [+ Link GitHub account]                │
│  [+ Link Microsoft account]             │
└──────────────────────────────────────────┘
```

## Security Considerations

1. **CSRF Protection**: `state_entries` with 10-min TTL, one-time use (same as Slack)
2. **Secrets**: Client secrets in env vars / config, never in DB or API responses
3. **External ID**: Use provider's stable user ID (`sub`, `id`), not email
4. **Email verification**: Require verified email from providers where available
5. **Unlink protection**: Cannot unlink last identity when user has no password
6. **Callback URLs**: Must be registered with each provider's OAuth app

## Error Codes

| Code | HTTP | Description |
|------|------|-------------|
| `PROVIDER_NOT_CONFIGURED` | 404 | Provider credentials not set |
| `INVALID_STATE` | 400 | Invalid or expired OAuth state |
| `TOKEN_EXCHANGE_FAILED` | 502 | Failed to exchange code with provider |
| `EMAIL_NOT_VERIFIED` | 403 | Email not verified by provider |
| `OAUTH_FAILED` | 502 | Generic OAuth failure |
| `IDENTITY_ALREADY_LINKED` | 409 | External identity linked to another user |
| `CANNOT_UNLINK_LAST` | 400 | Cannot unlink only identity without password |

## Testing Strategy

### Integration Tests (with mocked HTTP)
- Full OAuth flow per provider (state → redirect → callback → tokens)
- User creation on first login
- Existing user login (via `user_providers` lookup)
- Email-based user matching (user exists by email, no provider link yet)
- Membership auto-creation (first user = admin)
- State expiry handling
- Error cases (invalid state, provider error, email not verified)

### E2E Tests
- Login page shows correct buttons based on configured providers
- Full flow with real provider credentials (manual testing)
- Link/unlink identity from settings page

## Implementation Phases

### Phase 1: Google + GitHub
- `GoogleOAuthConfig` + `GitHubOAuthConfig` in config
- Google OAuth handler + service
- GitHub OAuth handler + service
- `GET /api/v1/auth/providers` endpoint
- Integration tests with mocked provider responses

### Phase 2: Microsoft + Identity Management
- `MicrosoftOAuthConfig` in config
- Microsoft OAuth handler + service (with tenant support)
- Identity list/unlink API endpoints
- Lockout prevention guard

### Phase 3: Frontend
- Login page: fetch available providers + buttons
- OAuth redirect token handling
- Settings page: linked accounts management

---

**Status**: Draft | **Created**: 2026-02-10
