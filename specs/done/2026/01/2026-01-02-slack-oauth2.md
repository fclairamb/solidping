# Slack OAuth User Authentication

## Overview

This spec defines the OAuth flow for authenticating users via Slack. This is different from the existing Slack workspace integration (`internal/integrations/slack/`) which handles bot-level access. This flow authenticates individual users using their Slack identity.

## API Endpoints

### `GET /api/v1/auth/slack/login`

Initiates the OAuth flow by redirecting to Slack's authorization page.

**Query Parameters:**
- `redirect_uri` (optional): Frontend URL to redirect after auth (default: configured frontend URL)

**Response:** HTTP 302 redirect to Slack OAuth URL

**Slack OAuth URL format:**
```
https://slack.com/oauth/v2/authorize?
  client_id={SLACK_CLIENT_ID}&
  user_scope=openid,email,profile&
  redirect_uri={CALLBACK_URL}&
  state={STATE_TOKEN}
```

### `GET /api/v1/auth/slack/callback`

Handles the OAuth callback from Slack.

**Query Parameters:**
- `code`: Authorization code from Slack
- `state`: State token for CSRF protection
- `error` (optional): Error code if auth failed

**Success Response:** HTTP 302 redirect to frontend with tokens
```
{redirect_uri}?access_token={JWT}&refresh_token={REFRESH_TOKEN}
```

**Error Response:** HTTP 302 redirect to frontend with error
```
{redirect_uri}?error={ERROR_CODE}&error_description={MESSAGE}
```

## Implementation Details

### 1. Slack API Calls

#### Token Exchange
```
POST https://slack.com/api/oauth.v2.access
Content-Type: application/x-www-form-urlencoded

client_id={CLIENT_ID}&
client_secret={CLIENT_SECRET}&
code={AUTH_CODE}&
redirect_uri={CALLBACK_URL}
```

**Response:**
```json
{
  "ok": true,
  "authed_user": {
    "id": "U0123456789",
    "scope": "openid,email,profile",
    "access_token": "xoxp-...",
    "token_type": "user"
  },
  "team": {
    "id": "T0123456789",
    "name": "Workspace Name"
  }
}
```

#### Fetch User Info
```
GET https://slack.com/api/openid.connect.userInfo
Authorization: Bearer {USER_ACCESS_TOKEN}
```

**Response:**
```json
{
  "ok": true,
  "sub": "U0123456789",
  "email": "user@example.com",
  "email_verified": true,
  "name": "John Doe",
  "picture": "https://avatars.slack-edge.com/..."
}
```

### 2. Database Operations

#### Find or Create User

```go
// 1. Check if Slack user is already linked
provider, err := db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, slackUserID)
if err == nil && provider != nil {
    // User exists, fetch and return
    user, _ := db.GetUser(ctx, provider.UserUID)
    return user, nil
}

// 2. Check if user exists by email
user, err := db.GetUserByEmail(ctx, slackEmail)
if err != nil {
    // 3. Create new user
    user = models.NewUser(slackEmail, slackName)
    user.AvatarURL = slackPicture
    user.EmailVerifiedAt = &now  // Slack verifies email
    db.CreateUser(ctx, user)
}

// 4. Link Slack provider to user
provider = models.NewUserProvider(user.UID, models.ProviderTypeSlack, slackUserID)
provider.Metadata = models.JSONMap{
    "team_id":   slackTeamID,
    "team_name": slackTeamName,
}
db.CreateUserProvider(ctx, provider)
```

#### Find or Create Organization

```go
// 1. Look for org linked to this Slack team
// Query organization_providers or check existing IntegrationConnection
org, err := db.GetOrganizationBySlackTeamID(ctx, slackTeamID)
if err != nil {
    // 2. Create new organization
    slug := slugify(slackTeamName)  // e.g., "acme-corp"
    org = models.NewOrganization(slug)
    db.CreateOrganization(ctx, org)

    // 3. Store Slack team mapping (new table or use existing pattern)
    // Option A: Add to organization metadata
    // Option B: Create organization_providers table
}
```

#### Add User to Organization

```go
// Check if user is already a member
member, err := db.GetOrganizationMember(ctx, org.UID, user.UID)
if err != nil {
    // Add as regular user
    member = models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleUser)
    member.JoinedAt = &now
    db.CreateOrganizationMember(ctx, member)
}
```

### 3. State Management

Store OAuth state in cache/database for CSRF protection:

```go
type OAuthState struct {
    Nonce       string    `json:"nonce"`
    RedirectURI string    `json:"redirect_uri"`
    CreatedAt   time.Time `json:"created_at"`
}

// Generate state
state := base64(json(OAuthState{
    Nonce:       uuid.NewString(),
    RedirectURI: redirectURI,
    CreatedAt:   time.Now(),
}))

// Store in cache with 10-minute TTL
cache.Set("oauth_state:"+nonce, state, 10*time.Minute)
```

### 4. JWT Token Generation

Reuse existing auth service pattern:

```go
// After successful OAuth, generate tokens like regular login
accessToken, _ := authService.GenerateAccessToken(user, org, role)
refreshToken, _ := authService.CreateRefreshToken(ctx, user.UID, org.UID)
```

## New Database Table (Optional)

If organizations need to be linked to Slack teams:

```sql
-- Add to existing migrations
CREATE TABLE organization_providers (
    uid              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_uid UUID NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    provider_type    TEXT NOT NULL CHECK (provider_type IN ('slack', 'google', 'microsoft')),
    provider_id      TEXT NOT NULL,  -- e.g., Slack Team ID
    metadata         JSONB,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (provider_type, provider_id)
);

CREATE INDEX idx_org_providers_org ON organization_providers(organization_uid);
```

## Error Handling

| Scenario | Error Code | HTTP Status |
|----------|------------|-------------|
| Invalid/expired state | `INVALID_STATE` | 400 |
| Slack API error | `OAUTH_FAILED` | 400 |
| Email not verified in Slack | `EMAIL_NOT_VERIFIED` | 400 |
| Slack token exchange failed | `TOKEN_EXCHANGE_FAILED` | 400 |
| User creation failed | `INTERNAL_ERROR` | 500 |

## Configuration

Existing config in `config.SlackConfig`:

```yaml
slack:
  app_id: "A0123456789"
  client_id: "123456789.123456789"
  client_secret: "abc123..."
  signing_secret: "def456..."
```

**Environment variables:**
```bash
SP_SLACK_APP_ID=A0123456789
SP_SLACK_CLIENT_ID=123456789.123456789
SP_SLACK_CLIENT_SECRET=abc123...
SP_SLACK_SIGNING_SECRET=def456...
```

## Files to Create/Modify

### New Files
- `back/internal/handlers/auth/slack.go` - OAuth handlers
- `back/internal/handlers/auth/slack_service.go` - OAuth business logic

### Modified Files
- `back/internal/handlers/auth/routes.go` - Add new routes
- `back/internal/db/service.go` - Add `GetOrganizationBySlackTeamID` if needed
- `back/internal/db/models/organization.go` - Add `OrganizationProvider` model if needed
- `back/internal/db/migrations/` - New migration for `organization_providers` table

## Sequence Diagram

```
User                    Frontend                  Backend                   Slack
 |                         |                         |                        |
 |-- Click "Login with Slack" -->                    |                        |
 |                         |-- GET /auth/slack/login -->                      |
 |                         |<-- 302 Redirect --------|                        |
 |<-- Redirect to Slack ---|                         |                        |
 |                         |                         |                        |
 |-- Authorize App -------------------------------------------------->        |
 |<-- Redirect with code -----------------------------------------------------|
 |                         |                         |                        |
 |-- GET /auth/slack/callback?code=xxx ------------->|                        |
 |                         |                         |-- POST oauth.v2.access -->
 |                         |                         |<-- tokens -------------|
 |                         |                         |-- GET userInfo ------->|
 |                         |                         |<-- user data ----------|
 |                         |                         |                        |
 |                         |                         |-- Find/create user     |
 |                         |                         |-- Find/create org      |
 |                         |                         |-- Link user to org     |
 |                         |                         |-- Generate JWT         |
 |                         |                         |                        |
 |<-- 302 Redirect with tokens ----------------------|                        |
 |-- Store tokens -------->|                         |                        |
```

## Security Considerations

1. **State parameter**: Always validate to prevent CSRF attacks
2. **HTTPS only**: All OAuth endpoints must use HTTPS in production
3. **Token storage**: Store Slack access tokens encrypted if persisted
4. **Scope minimization**: Only request `openid,email,profile` scopes
5. **Email verification**: Only accept users with verified Slack emails
