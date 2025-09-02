# Discord OAuth2 User Authentication

## Overview

This spec defines the OAuth2 flow for authenticating users via Discord. It mirrors the existing Slack OAuth implementation (`handlers/auth/slack.go` + `slack_service.go`). Users can log in with their Discord account, and their Discord server (guild) maps to a SolidPing organization.

## API Endpoints

### `GET /api/v1/auth/discord/login`

Initiates the OAuth2 flow by redirecting to Discord's authorization page.

**Query Parameters:**
- `redirect_uri` (optional): Frontend URL to redirect after auth (default: configured frontend URL)

**Response:** HTTP 302 redirect to Discord OAuth2 URL

**Discord OAuth2 URL format:**
```
https://discord.com/oauth2/authorize?
  client_id={DISCORD_CLIENT_ID}&
  redirect_uri={CALLBACK_URL}&
  response_type=code&
  scope=identify+email+guilds&
  state={STATE_TOKEN}
```

### `GET /api/v1/auth/discord/callback`

Handles the OAuth2 callback from Discord.

**Query Parameters:**
- `code`: Authorization code from Discord
- `state`: State token for CSRF protection
- `error` (optional): Error code if auth failed
- `error_description` (optional): Human-readable error message

**Success Response:** HTTP 302 redirect to frontend with tokens
```
{redirect_uri}?access_token={JWT}&refresh_token={REFRESH_TOKEN}
```

**Error Response:** HTTP 302 redirect to frontend with error
```
{redirect_uri}?error={ERROR_CODE}&error_description={MESSAGE}
```

## Implementation Details

### 1. Discord API Calls

#### Token Exchange
```
POST https://discord.com/api/oauth2/token
Content-Type: application/x-www-form-urlencoded

client_id={CLIENT_ID}&
client_secret={CLIENT_SECRET}&
grant_type=authorization_code&
code={AUTH_CODE}&
redirect_uri={CALLBACK_URL}
```

**Response:**
```json
{
  "access_token": "6qrZcUqja7812RVdnEKjpzOL4CvHBFG",
  "token_type": "Bearer",
  "expires_in": 604800,
  "refresh_token": "D43f5y0ahjqew82jZ4NViEr2YafMKhue",
  "scope": "identify email guilds"
}
```

#### Fetch User Info
```
GET https://discord.com/api/users/@me
Authorization: Bearer {ACCESS_TOKEN}
```

**Response:**
```json
{
  "id": "80351110224678912",
  "username": "Nelly",
  "global_name": "Nelly",
  "email": "nelly@example.com",
  "verified": true,
  "avatar": "8342729096ea3675442027381ff50dfe"
}
```

#### Fetch User Guilds
```
GET https://discord.com/api/users/@me/guilds
Authorization: Bearer {ACCESS_TOKEN}
```

**Response:**
```json
[
  {
    "id": "80351110224678912",
    "name": "My Server",
    "icon": "8342729096ea3675442027381ff50dfe",
    "owner": true,
    "permissions": "36953089"
  }
]
```

### 2. Guild-to-Organization Mapping

Discord guilds map to SolidPing organizations, same as Slack teams.

**Challenge:** A Discord user can be in many guilds. We need a strategy to select which guild becomes the organization.

**Approach:** If the user belongs to exactly one guild that already has a SolidPing organization, use that. Otherwise:
1. If the user has no guild with a linked org → prompt them to select a guild (redirect with guild selection)
2. If the user has multiple guilds with linked orgs → use the most recently used one, allow switching via `switch-org`

For first-time setup (no org exists yet): use the first guild where the user is the owner, or let them pick.

### 3. Database Operations

#### Find or Create User

```go
// 1. Check if Discord user is already linked
provider, err := db.GetUserProviderByProviderID(ctx, models.ProviderTypeDiscord, discordUserID)
if err == nil && provider != nil {
    user, _ := db.GetUser(ctx, provider.UserUID)
    return user, nil
}

// 2. Check if user exists by email
user, err := db.GetUserByEmail(ctx, discordEmail)
if err != nil {
    // 3. Create new user
    user = models.NewUser(discordEmail, discordGlobalName)
    user.AvatarURL = fmt.Sprintf("https://cdn.discordapp.com/avatars/%s/%s.png", discordUserID, discordAvatar)
    user.EmailVerifiedAt = &now  // Discord verifies email
    db.CreateUser(ctx, user)
}

// 4. Link Discord provider to user
provider = models.NewUserProvider(user.UID, models.ProviderTypeDiscord, discordUserID)
provider.Metadata = models.JSONMap{
    "username":    discordUsername,
    "global_name": discordGlobalName,
}
db.CreateUserProvider(ctx, provider)
```

#### Find or Create Organization

```go
// 1. Look for org linked to this Discord guild
orgProvider, err := db.GetOrganizationProvider(ctx, models.ProviderTypeDiscord, guildID)
if err == nil {
    org, _ := db.GetOrganization(ctx, orgProvider.OrganizationUID)
    return org, nil
}

// 2. Create new organization
slug := slugify(guildName)
org = models.NewOrganization(slug)
db.CreateOrganization(ctx, org)

// 3. Store Discord guild mapping
orgProvider = models.NewOrganizationProvider(org.UID, models.ProviderTypeDiscord, guildID)
orgProvider.Metadata = models.JSONMap{"guild_name": guildName}
db.CreateOrganizationProvider(ctx, orgProvider)
```

### 4. State Management

Reuse the existing `state_entries` table (same as Slack):

```go
state := &models.StateEntry{
    Key:       "oauth_discord:" + nonce,
    Value:     models.JSONMap{"nonce": nonce, "redirect_uri": redirectURI},
    ExpiresAt: time.Now().Add(10 * time.Minute),
}
db.CreateStateEntry(ctx, state)
```

### 5. JWT Token Generation

Reuse existing auth service (identical to Slack flow):

```go
accessToken, _ := authService.GenerateAccessToken(user, org, role)
refreshToken, _ := authService.CreateRefreshToken(ctx, user.UID, org.UID)
```

## Configuration

New `DiscordConfig` struct in `config/`:

```go
type DiscordConfig struct {
    ClientID     string `koanf:"client_id"`
    ClientSecret string `koanf:"client_secret"`
    BotToken     string `koanf:"bot_token"`     // Shared with integration spec
    RedirectURL  string `koanf:"redirect_url"`  // OAuth callback URL
}
```

Added to `Config` struct:
```go
Discord DiscordConfig `koanf:"discord"`
```

**Environment variables:**
```bash
SP_DISCORD_CLIENT_ID=123456789012345678
SP_DISCORD_CLIENT_SECRET=abcdef...
SP_DISCORD_BOT_TOKEN=MTIz...  # Bot token (shared with notification integration)
SP_DISCORD_REDIRECT_URL=https://app.solidping.com/api/v1/auth/discord/callback
```

## Provider Type

Add to existing provider types:

```go
const ProviderTypeDiscord = "discord"
```

This is used in both `organization_providers` and `user_providers` tables (no schema migration needed, just a new constant).

## Error Handling

| Scenario | Error Code | HTTP Status |
|----------|------------|-------------|
| Invalid/expired state | `INVALID_STATE` | 400 |
| Discord API error | `OAUTH_FAILED` | 400 |
| Email not verified in Discord | `EMAIL_NOT_VERIFIED` | 400 |
| Token exchange failed | `TOKEN_EXCHANGE_FAILED` | 400 |
| No guild available | `NO_GUILD_AVAILABLE` | 400 |
| User creation failed | `INTERNAL_ERROR` | 500 |

## Files to Create/Modify

### New Files
- `back/internal/handlers/auth/discord.go` — OAuth handlers (Login, Callback)
- `back/internal/handlers/auth/discord_service.go` — OAuth business logic
- `back/internal/config/discord_oauth.go` — DiscordConfig struct

### Modified Files
- `back/internal/handlers/auth/routes.go` — Add `/auth/discord/login` and `/auth/discord/callback` routes
- `back/internal/config/config.go` — Add `Discord DiscordConfig` field to Config struct
- `back/internal/db/models/user.go` — Add `ProviderTypeDiscord` constant
- `apps/dash0/` — Add "Login with Discord" button on auth page

## Sequence Diagram

```
User                    Frontend                  Backend                   Discord
 |                         |                         |                        |
 |-- Click "Login with Discord" -->                  |                        |
 |                         |-- GET /auth/discord/login -->                    |
 |                         |<-- 302 Redirect --------|                        |
 |<-- Redirect to Discord -|                         |                        |
 |                         |                         |                        |
 |-- Authorize App -------------------------------------------------->       |
 |<-- Redirect with code ----------------------------------------------------|
 |                         |                         |                        |
 |-- GET /auth/discord/callback?code=xxx ----------->|                        |
 |                         |                         |-- POST oauth2/token -->|
 |                         |                         |<-- tokens -------------|
 |                         |                         |-- GET users/@me ------>|
 |                         |                         |<-- user data ----------|
 |                         |                         |-- GET users/@me/guilds>|
 |                         |                         |<-- guilds -------------|
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

1. **State parameter**: Always validate to prevent CSRF attacks (reuse state_entries)
2. **HTTPS only**: All OAuth endpoints must use HTTPS in production
3. **Email verification**: Only accept users with `verified: true` from Discord
4. **Scope minimization**: Only request `identify`, `email`, `guilds`
5. **Bot token security**: Bot token is sensitive — stored in config, never exposed to frontend

## Discord Application Setup

To use Discord OAuth2, create a Discord application at https://discord.com/developers/applications:

1. Create a new application
2. Go to OAuth2 → Add redirect URL matching `SP_DISCORD_REDIRECT_URL`
3. Copy Client ID and Client Secret
4. Go to Bot → Create a bot (needed for the notification integration)
5. Copy the Bot Token
6. Enable "Server Members Intent" and "Message Content Intent" under Privileged Gateway Intents

---

Status: Draft
Created: 2026-03-22
