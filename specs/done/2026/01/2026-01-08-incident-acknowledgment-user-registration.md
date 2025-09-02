# Incident Acknowledgment: User Registration & Activity Logging

## Overview

When a user acknowledges an incident via Slack, the system should:
1. Register the user in the organization if they don't already exist (using existing `user_providers` table)
2. Add a message/event recording "@user acknowledged the incident"

This ensures all incident responders are tracked in the system and provides a clear audit trail of incident acknowledgments.

## Current State

The current `AcknowledgeIncidentFromSlack` in `back/internal/handlers/incidents/service.go:624` only:
- Updates the incident's `acknowledged_at` timestamp
- Creates an event with `slack_user_id` and `slack_username` in the payload
- Does NOT link to an actual user in the system
- Does NOT add the Slack user to the organization

## Proposed Changes

### 1. Reuse Existing User Lookup Pattern

The authentication flow in `back/internal/handlers/auth/slack_service.go` already implements the exact pattern we need:

```go
// findOrCreateUser finds or creates user by Slack identity.
func (s *SlackOAuthService) findOrCreateUser(
    ctx context.Context, userInfo *OpenIDUserInfo, teamID, teamName string,
) (*models.User, error) {
    // 1. Check by Slack user ID first (via user_providers)
    provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, userInfo.Sub)
    if err == nil && provider != nil {
        return s.db.GetUser(ctx, provider.UserUID)
    }

    // 2. Check by email (if available)
    // 3. Create new user if not found
    // 4. Link Slack provider via user_providers table
}
```

This uses the existing **`user_providers`** table:
- `provider_type`: `'slack'`
- `provider_id`: Slack user ID (e.g., `U123ABC`)
- `metadata`: JSON with `team_id`, `team_name`

### 2. Fetch Slack User Profile

The interactive payload from Slack button clicks only provides:
- `user.id` (Slack user ID)
- `user.username` (display name)

To create a proper user record, we need to fetch additional profile data:

```go
// GetUserInfo fetches detailed user profile from Slack API.
func (c *Client) GetUserInfo(ctx context.Context, userID string) (*SlackUserInfo, error) {
    // Call users.info API
    // Returns: email, real_name, profile picture, etc.
}
```

**Required OAuth scope**: `users:read` (already in use), `users:read.email` (may need to add)

### 3. Updated Acknowledgment Flow

Modify `handleAcknowledgeIncident` in `back/internal/integrations/slack/interactions.go:211`:

```go
func (h *Handler) handleAcknowledgeIncident(
    ctx context.Context, interaction *Interaction, action *InteractionAction,
) (*MessageResponse, error) {
    incidentUID := action.Value

    // Get connection to find organization
    conn, err := h.svc.GetConnectionByTeamID(ctx, interaction.Team.ID)
    // ...

    // NEW: Find or create user from Slack identity
    user, wasCreated, err := h.svc.FindOrCreateUserFromSlack(
        ctx,
        conn.OrganizationUID,
        interaction.Team.ID,
        interaction.Team.Name,
        interaction.User.ID,
        interaction.User.Username,
    )
    if err != nil {
        slog.WarnContext(ctx, "Failed to find/create user, proceeding without user link",
            "error", err,
            "slack_user_id", interaction.User.ID,
        )
        // Fall back to current behavior (no user link)
    }

    // NEW: Ensure user is member of organization (if user was found/created)
    if user != nil {
        if err := h.svc.EnsureMembership(ctx, conn.OrganizationUID, user.UID); err != nil {
            slog.WarnContext(ctx, "Failed to ensure membership", "error", err)
        }
    }

    // Acknowledge the incident (now with user UID if available)
    incident, err := h.svc.incidentsService.AcknowledgeIncidentFromSlack(
        ctx,
        conn.OrganizationUID,
        incidentUID,
        interaction.User.ID,
        interaction.User.Username,
        user, // NEW: pass user for proper linking
    )
    // ...
}
```

### 4. Service Method for User Lookup

Add to Slack integration service (`back/internal/integrations/slack/service.go`):

```go
// FindOrCreateUserFromSlack finds or creates a user based on Slack identity.
// Returns the user, whether it was created, and any error.
func (s *Service) FindOrCreateUserFromSlack(
    ctx context.Context,
    orgUID, teamID, teamName, slackUserID, slackUsername string,
) (*models.User, bool, error) {
    // 1. Check user_providers for existing Slack user link
    provider, err := s.db.GetUserProviderByProviderID(ctx, models.ProviderTypeSlack, slackUserID)
    if err == nil && provider != nil {
        user, err := s.db.GetUser(ctx, provider.UserUID)
        if err == nil {
            return user, false, nil
        }
    }

    // 2. Try to fetch email from Slack API (best effort)
    var email string
    client, err := s.GetClient(ctx, teamID)
    if err == nil {
        userInfo, err := client.GetUserInfo(ctx, slackUserID)
        if err == nil && userInfo.Email != "" {
            email = userInfo.Email

            // Check if user exists by email
            user, err := s.db.GetUserByEmail(ctx, email)
            if err == nil {
                // Link Slack provider to existing user
                provider := models.NewUserProvider(user.UID, models.ProviderTypeSlack, slackUserID)
                provider.Metadata = models.JSONMap{
                    "team_id":   teamID,
                    "team_name": teamName,
                }
                _ = s.db.CreateUserProvider(ctx, provider) // Best effort
                return user, false, nil
            }
        }
    }

    // 3. Create new user
    user := models.NewUser(email) // email may be empty
    if email == "" {
        // Use a placeholder email format for Slack-only users
        user.Email = fmt.Sprintf("%s@slack.placeholder", slackUserID)
    }
    user.Name = slackUsername

    if err := s.db.CreateUser(ctx, user); err != nil {
        return nil, false, fmt.Errorf("failed to create user: %w", err)
    }

    // 4. Link Slack provider
    provider = models.NewUserProvider(user.UID, models.ProviderTypeSlack, slackUserID)
    provider.Metadata = models.JSONMap{
        "team_id":   teamID,
        "team_name": teamName,
    }
    if err := s.db.CreateUserProvider(ctx, provider); err != nil {
        return nil, false, fmt.Errorf("failed to create user provider: %w", err)
    }

    return user, true, nil
}

// EnsureMembership ensures a user is a member of an organization.
// If not already a member, adds them with 'viewer' role.
func (s *Service) EnsureMembership(ctx context.Context, orgUID, userUID string) error {
    // Check existing membership
    _, err := s.db.GetMemberByUserAndOrg(ctx, userUID, orgUID)
    if err == nil {
        return nil // Already a member
    }

    // Create membership with viewer role (minimal permissions)
    member := models.NewOrganizationMember(orgUID, userUID, models.MemberRoleViewer)
    return s.db.CreateOrganizationMember(ctx, member)
}
```

### 5. Updated Event with User Reference

Modify `AcknowledgeIncident` in `back/internal/handlers/incidents/service.go`:

```go
// AcknowledgeIncidentFromSlack marks an incident as acknowledged via Slack.
func (s *Service) AcknowledgeIncidentFromSlack(
    ctx context.Context,
    orgUID, incidentUID, slackUserID, slackUsername string,
    user *models.User, // NEW: optional user reference
) (*models.Incident, error) {
    req := &AcknowledgeIncidentRequest{
        IncidentUID:   incidentUID,
        SlackUserID:   slackUserID,
        SlackUsername: slackUsername,
        Via:           "slack",
    }

    // Link to user if available
    if user != nil {
        req.AcknowledgedBy = user.UID
    }

    return s.AcknowledgeIncident(ctx, orgUID, req)
}
```

Update the event creation to include a human-readable message:

```go
// In AcknowledgeIncident, update the event payload:
event := models.NewEvent(orgUID, models.EventTypeIncidentAcknowledged, models.ActorTypeUser)
event.IncidentUID = &incident.UID

// Determine display name
displayName := req.SlackUsername
if req.AcknowledgedBy != "" {
    event.ActorUID = &req.AcknowledgedBy
    // Could also fetch user.Name here if needed
}

event.Payload = models.JSONMap{
    "message":        fmt.Sprintf("@%s acknowledged the incident", displayName),
    "via":            req.Via,
    "slack_user_id":  req.SlackUserID,
    "slack_username": req.SlackUsername,
    "user_uid":       req.AcknowledgedBy, // May be empty
}
```

### 6. Slack Thread Reply

After acknowledging, post a thread reply (already partially implemented):

```go
// In handleAcknowledgeIncident, after successful acknowledgment:
if incident.SlackThreadTs != "" {
    displayName := interaction.User.Username
    if user != nil && user.Name != "" {
        displayName = user.Name
    }

    threadMessage := fmt.Sprintf("@%s acknowledged the incident", displayName)

    client.PostMessage(
        incident.SlackChannelID,
        slack.MsgOptionText(threadMessage, false),
        slack.MsgOptionTS(incident.SlackThreadTs),
    )
}
```

## User Roles

Users created through incident acknowledgment receive `viewer` role:

| Role | Description |
|------|-------------|
| `viewer` | Read-only access: view incidents, checks, and dashboards |

This is intentionally restrictive. Users can be upgraded by organization admins.

## Edge Cases

### 1. Slack User Has No Email
- `users:read.email` scope may not be available
- Create user with placeholder email: `{slack_user_id}@slack.placeholder`
- User can update email later if they log in via web

### 2. Email Conflict
- If a user with the same email already exists, link the Slack provider to that user
- This unifies accounts across authentication methods

### 3. Slack API Unavailable
- If fetching user info fails, proceed with acknowledgment
- Create user with just the Slack username (no email)
- Log warning for debugging

### 4. User Already Linked
- If `user_providers` already has this Slack ID, use existing user
- No duplicate records created

### 5. Already a Member
- If user is already an organization member, skip membership creation
- Existing role is preserved (don't downgrade admin to viewer)

## Data Flow Summary

```
Slack Button Click
       │
       ▼
┌─────────────────────────────┐
│ handleAcknowledgeIncident   │
└─────────────────────────────┘
       │
       ▼
┌─────────────────────────────┐
│ FindOrCreateUserFromSlack   │
│  1. Check user_providers    │
│  2. Fetch Slack profile     │
│  3. Check users by email    │
│  4. Create user if needed   │
│  5. Link user_providers     │
└─────────────────────────────┘
       │
       ▼
┌─────────────────────────────┐
│ EnsureMembership            │
│  - Add to org as 'viewer'   │
│    if not already member    │
└─────────────────────────────┘
       │
       ▼
┌─────────────────────────────┐
│ AcknowledgeIncident         │
│  1. Update incident         │
│  2. Create event with:      │
│     - user_uid (linked)     │
│     - message: "@X ack'd"   │
└─────────────────────────────┘
       │
       ▼
┌─────────────────────────────┐
│ Post Slack Thread Reply     │
│  "@UserName acknowledged    │
│   the incident"             │
└─────────────────────────────┘
```

## Testing Checklist

- [ ] New user is created when acknowledging from unknown Slack account
- [ ] Existing user is found via `user_providers` when acknowledging
- [ ] User without email in Slack is handled (placeholder email)
- [ ] User with existing email gets Slack provider linked
- [ ] Organization membership is created with `viewer` role
- [ ] Existing organization member is not modified
- [ ] Event contains `user_uid` when user is linked
- [ ] Event contains `message` with "@username acknowledged the incident"
- [ ] Thread reply is posted with acknowledgment message
- [ ] Failures in user creation don't block incident acknowledgment

## Files to Modify

1. `back/internal/integrations/slack/service.go`
   - Add `FindOrCreateUserFromSlack()`
   - Add `EnsureMembership()`
   - Add `GetUserInfo()` to Client

2. `back/internal/integrations/slack/interactions.go`
   - Update `handleAcknowledgeIncident()` to call new methods

3. `back/internal/handlers/incidents/service.go`
   - Update `AcknowledgeIncidentFromSlack()` signature to accept user
   - Update event payload to include `message` field

---

**Status**: Draft | **Created**: 2026-01-08 | **Related**: `2026-01-07-slack-incident-messages.md`, `2025-12-26-incidents.md`
