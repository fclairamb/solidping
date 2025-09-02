# Slack OAuth Integration - User & Organization Creation

## Overview

Extend the existing Slack OAuth flow (`/api/v1/integrations/slack/oauth`) to automatically create users and organizations when a user installs the SolidPing Slack app.

Currently, the OAuth flow requires an existing organization. This change allows new users to install the app and have both their user account and organization created automatically based on their Slack workspace.

## Current Behavior

The existing `HandleOAuthCallback` in `back/internal/integrations/slack/service.go`:
1. Exchanges the OAuth code for tokens
2. Parses state to get `{org_identifier}_{nonce}` — **state is required**
3. **Fails if organization doesn't exist** (`ErrOrganizationNotFound`)
4. **Fails if state is missing or invalid** (`ErrInvalidState`)
5. Creates/updates an `IntegrationConnection` for the Slack workspace

## New Behavior

When the OAuth callback is received:

### 1. Exchange OAuth Code for Tokens (existing)

No changes needed - already implemented in `ExchangeCode()`.

### 2. Fetch Installer's User Info from Slack

Call `users.info` API with the `authed_user.id` to get the installer's details:

```go
// Add to client.go
func (c *Client) GetUserInfoWithEmail(ctx context.Context, userID string) (*UserInfo, error) {
    var result struct {
        OK   bool     `json:"ok"`
        User UserInfo `json:"user"`
    }
    if err := c.callAPI(ctx, "users.info", map[string]any{
        "user": userID,
    }, &result); err != nil {
        return nil, err
    }
    return &result.User, nil
}

// Add to types.go
type UserInfo struct {
    ID       string `json:"id"`
    TeamID   string `json:"team_id"`
    Name     string `json:"name"`
    RealName string `json:"real_name"`
    Profile  struct {
        Email   string `json:"email"`
        Image48 string `json:"image_48"`
    } `json:"profile"`
}
```

**Required Slack scope**: `users:read.email` (already in the existing scope list)

### 3. Find or Create Organization

```go
// Try to find existing org by Slack Team ID
conn, err := s.db.GetIntegrationConnectionByProperty(ctx, "slack", "team_id", oauthResp.Team.ID)
if conn != nil {
    // Organization exists, get it
    org, _ = s.db.GetOrganization(ctx, conn.OrganizationUID)
} else {
    // Create new organization from Slack team
    slug := generateUniqueSlug(ctx, oauthResp.Team.Name)
    org = models.NewOrganization(slug)
    s.db.CreateOrganization(ctx, org)
}
```

#### Slug Generation Algorithm

Generate a unique organization slug from the Slack team name:

1. **Normalize**: Convert to lowercase, replace spaces with hyphens
2. **Filter**: Keep only characters matching `[a-z0-9-]+`
3. **Trim**: Remove leading/trailing hyphens, collapse multiple hyphens
4. **Uniqueness**: If slug exists, append incrementing number (2, 3, 4...)

```go
func generateUniqueSlug(ctx context.Context, teamName string) string {
    // Normalize: lowercase and replace spaces with hyphens
    base := strings.ToLower(teamName)
    base = strings.ReplaceAll(base, " ", "-")

    // Filter: keep only [a-z0-9-]
    var filtered strings.Builder
    for _, r := range base {
        if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
            filtered.WriteRune(r)
        }
    }
    base = filtered.String()

    // Trim: remove leading/trailing hyphens, collapse multiple hyphens
    base = strings.Trim(base, "-")
    for strings.Contains(base, "--") {
        base = strings.ReplaceAll(base, "--", "-")
    }

    // Fallback if empty
    if base == "" {
        base = "org"
    }

    // Check uniqueness, append number if needed
    slug := base
    suffix := 2
    for {
        _, err := s.db.GetOrganizationBySlug(ctx, slug)
        if err != nil {
            // Slug is available
            return slug
        }
        slug = fmt.Sprintf("%s%d", base, suffix)
        suffix++
    }
}
```

**Examples:**

| Team Name | Generated Slug |
|-----------|----------------|
| `Acme Corp` | `acme-corp` |
| `Acme Corp` (if exists) | `acme-corp2` |
| `Acme Corp` (if acme-corp2 exists) | `acme-corp3` |
| `My Company!!! 🚀` | `my-company` |
| `---Test---` | `test` |
| `123 Startup` | `123-startup` |
| `🎉🎉🎉` | `org` (fallback) |

### 4. Find or Create User

```go
// Check if user already exists by email
user, err := s.db.GetUserByEmail(ctx, userInfo.Profile.Email)
if err != nil {
    // Create new user
    user = models.NewUser(userInfo.Profile.Email, userInfo.RealName)
    user.AvatarURL = userInfo.Profile.Image48
    user.EmailVerifiedAt = &now  // Slack has verified the email
    s.db.CreateUser(ctx, user)
}

// Link Slack identity via user_providers
provider, _ := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, userInfo.ID)
if provider == nil {
    provider = models.NewUserProvider(user.UID, models.ProviderTypeSlack, userInfo.ID)
    provider.Metadata = models.JSONMap{
        "team_id":   oauthResp.Team.ID,
        "team_name": oauthResp.Team.Name,
    }
    s.db.CreateUserProvider(ctx, provider)
}
```

### 5. Add User to Organization

```go
// Check if user is already a member
member, err := s.db.GetOrganizationMember(ctx, org.UID, user.UID)
if err != nil {
    // Determine role: first user becomes admin, others are regular users
    role := models.MemberRoleUser
    memberCount, _ := s.db.CountOrganizationMembers(ctx, org.UID)
    if memberCount == 0 {
        role = models.MemberRoleAdmin
    }

    member = models.NewOrganizationMember(org.UID, user.UID, role)
    member.JoinedAt = &now
    s.db.CreateOrganizationMember(ctx, member)
}
```

### 6. Create Integration Connection (existing)

No changes needed - continue creating/updating the `IntegrationConnection` as before.

### 7. Redirect with Authentication

After successful installation, redirect to frontend with authentication tokens:

```go
// Generate JWT tokens for the user
accessToken, _ := authService.GenerateAccessToken(user, org, member.Role)
refreshToken, _ := authService.CreateRefreshToken(ctx, user.UID, org.UID)

// Redirect to frontend with tokens
redirectURL := fmt.Sprintf("/integrations/slack/%s?success=true&access_token=%s&refresh_token=%s",
    conn.UID, accessToken, refreshToken)
http.Redirect(w, r, redirectURL, http.StatusFound)
```

## State Parameter Changes

The `state` parameter is now **optional**.

### Behavior by State Value

| State Value | Behavior |
|-------------|----------|
| Empty/missing | Create new org from Slack team, create user, add as admin |
| `{org_identifier}_{nonce}` | Find existing org, create user if needed, add to org |

### When State is Empty or Missing

If no state parameter is provided (e.g., user installs directly from Slack App Directory):
1. Organization is created from `oauthResp.Team.ID` and `oauthResp.Team.Name`
2. User is created from installer's Slack profile
3. User becomes the organization admin (first user)

```go
// In HandleOAuthCallback
var org *models.Organization

if state == "" {
    // No state: create new organization from Slack team
    org, err = s.findOrCreateOrganizationByTeamID(ctx, oauthResp.Team.ID, oauthResp.Team.Name)
} else {
    // State provided: parse and find existing org (current behavior)
    parts := strings.SplitN(state, "_", 2)
    if len(parts) >= 1 {
        orgIdentifier := parts[0]
        org, err = s.findOrganizationByIdentifier(ctx, orgIdentifier)
        if err != nil {
            // Fallback: create from team if org not found
            org, err = s.findOrCreateOrganizationByTeamID(ctx, oauthResp.Team.ID, oauthResp.Team.Name)
        }
    }
}
```

### CSRF Considerations

Without a state parameter, CSRF protection is reduced. However:
- The OAuth flow still requires user interaction with Slack
- The callback only creates resources, doesn't modify existing ones
- Each Slack team can only have one organization (idempotent)

## Files to Modify

### `back/internal/integrations/slack/service.go`

Update `HandleOAuthCallback`:
- Remove state validation requirement (make it optional)
- Add user info fetching
- Add org creation logic when state is missing
- Add user creation logic
- Add organization membership logic
- Update redirect to include auth tokens

Add new helper methods:
- `findOrCreateOrganizationByTeamID(ctx, teamID, teamName) (*models.Organization, error)`
- `findOrganizationByIdentifier(ctx, identifier) (*models.Organization, error)`
- `findOrCreateUser(ctx, userInfo) (*models.User, error)`
- `ensureOrganizationMembership(ctx, orgUID, userUID) (*models.OrganizationMember, error)`
- `generateUniqueSlug(ctx, teamName) string` - converts team name to unique slug

### `back/internal/integrations/slack/types.go`

Add `UserInfo` struct with profile/email fields.

### `back/internal/integrations/slack/client.go`

Add `GetUserInfoWithEmail` method (or extend existing `GetUserInfo`).

### `back/internal/integrations/slack/handler.go`

Inject `auth.Service` dependency for JWT generation.

## Database Operations Needed

Existing methods (no changes):
- `GetOrganizationBySlug`
- `GetOrganization`
- `CreateOrganization`
- `GetUserByEmail`
- `CreateUser`
- `GetUserProviderByProviderID`
- `CreateUserProvider`
- `CreateOrganizationMember`

Potentially new methods:
- `CountOrganizationMembers(ctx, orgUID) (int, error)` - to determine first user

## Error Handling

| Scenario | Action |
|----------|--------|
| Slack API error fetching user info | Return `ErrOAuthFailed` |
| Email not available from Slack | Return error (user must have email in Slack profile) |
| Organization slug conflict | Handled automatically by `generateUniqueSlug` (appends 2, 3, 4...) |
| Database error | Return `ErrInternalError` |

## Sequence Diagram

```
User                    Slack                    Backend                   Database
 |                        |                         |                         |
 |-- Install App -------->|                         |                         |
 |<-- Auth Screen --------|                         |                         |
 |-- Authorize ---------->|                         |                         |
 |                        |-- Callback with code -->|                         |
 |                        |                         |                         |
 |                        |                         |-- Exchange code ------->|
 |                        |<-- tokens --------------|                         |
 |                        |                         |                         |
 |                        |                         |-- users.info ---------->|
 |                        |<-- user details --------|                         |
 |                        |                         |                         |
 |                        |                         |-- Find org by team_id ->|
 |                        |                         |<-- not found -----------|
 |                        |                         |-- Create org ---------->|
 |                        |                         |<-- org ----------------|
 |                        |                         |                         |
 |                        |                         |-- Find user by email -->|
 |                        |                         |<-- not found -----------|
 |                        |                         |-- Create user --------->|
 |                        |                         |<-- user ----------------|
 |                        |                         |                         |
 |                        |                         |-- Create user_provider->|
 |                        |                         |-- Create org_member --->|
 |                        |                         |-- Create connection --->|
 |                        |                         |                         |
 |                        |                         |-- Generate JWT          |
 |                        |                         |                         |
 |<-- Redirect with tokens -------------------------|                         |
```

## Security Considerations

1. **Email verification**: Only accept users with email in their Slack profile
2. **Slug uniqueness**: Automatically handled by appending numbers (2, 3, 4...) on conflict
3. **First user privilege**: First installer becomes org admin, subsequent users are regular members
4. **Token security**: Use secure redirect with short-lived tokens
5. **CSRF mitigation**: State parameter is optional; when missing, flow is still safe because:
   - Requires Slack user interaction (can't be triggered silently)
   - Creates new resources only (no modification of existing data)
   - Idempotent by team_id (same team always maps to same org)

## Testing Scenarios

1. **New installation, no state**: State is empty, creates org from team + user + connection
2. **New installation with state**: State points to non-existent org, falls back to creating from team
3. **Existing org via state**: State points to existing org, creates user + adds to org
4. **Existing org via team_id**: State is empty but team already has org, finds org + creates user
5. **Existing org, existing user**: Just adds/updates connection, no new records
6. **Same user reinstalling**: Updates connection, no duplicate users/providers
7. **Second user from same workspace**: Finds existing org, creates user as regular member (not admin)
8. **Slug collision**: Team name "Acme Corp" when `acme-corp` exists creates `acme-corp2`
9. **Special characters in team name**: `My Company!!! 🚀` becomes `my-company`
10. **Empty slug after filtering**: Team name with only emojis/special chars uses fallback `org`
