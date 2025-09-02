# Global User Identity with Organization Memberships

## Overview

Refactor the user model from **per-organization users** to **global users with organization memberships**. This enables single sign-on experience across organizations, industry-standard multi-org access, and cleaner SSO/OAuth integration.

**Current State**: Users are duplicated per organization (`users.organization_uid` FK). Same email in multiple orgs = multiple accounts with separate passwords.

**Target State**: Global `users` table (no org FK) with `organization_members` junction. One account, multiple org memberships with per-org roles.

## Motivation

| Use Case | Current Model | Target Model |
|----------|---------------|--------------|
| MSP managing multiple clients | Multiple accounts needed | One account, switch orgs |
| Consultant invited to client monitoring | Creates new account each time | Existing account, add membership |
| Enterprise with prod/staging/regional orgs | Multiple logins | One login, multiple memberships |
| SSO integration | Complex per-org linking | Authenticate once, authorize per org |

**Industry Standard**: GitHub, Slack, Datadog, PagerDuty all use this pattern.

## Database Schema

### Tables

#### users

Global user accounts for authentication. No longer tied to a single organization.

```sql
CREATE TABLE users (
    uid               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email             text NOT NULL,
    name              text,
    avatar_url        text,
    password_hash     text,
    email_verified_at timestamptz,
    super_admin       boolean,
    last_active_at    timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);

CREATE UNIQUE INDEX users_email_idx ON users (lower(email)) WHERE deleted_at IS NULL;

COMMENT ON TABLE users IS 'Global user accounts for authentication. One account per email across all organizations.';
COMMENT ON COLUMN users.email IS 'Globally unique email address (case-insensitive).';
COMMENT ON COLUMN users.password_hash IS 'Argon2id hash. NULL for SSO-only users.';
COMMENT ON COLUMN users.email_verified_at IS 'Timestamp when email was verified. NULL if not verified.';
COMMENT ON COLUMN users.super_admin IS 'Super admin can access and manage all organizations.';
```

#### user_providers

Links users to external authentication providers (OAuth, SSO).

```sql
CREATE TABLE user_providers (
    uid               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_uid          uuid NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    provider_type     text NOT NULL CHECK (provider_type IN ('google', 'github', 'gitlab', 'microsoft', 'twitter', 'slack', 'saml', 'oidc')),
    provider_id       text NOT NULL,
    metadata          jsonb,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX user_providers_provider_idx ON user_providers (provider_type, provider_id);
CREATE INDEX user_providers_user_idx ON user_providers (user_uid);

COMMENT ON TABLE user_providers IS 'Links users to external auth providers (OAuth, SAML, OIDC).';
COMMENT ON COLUMN user_providers.provider_type IS 'External provider type: google, github, saml, etc.';
COMMENT ON COLUMN user_providers.provider_id IS 'Unique identifier from the external provider (e.g., OAuth sub claim).';
COMMENT ON COLUMN user_providers.metadata IS 'Provider-specific data (profile info, tokens, etc.).';
```

#### organization_members

Junction table linking users to organizations with role-based access.

```sql
CREATE TABLE organization_members (
    uid               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_uid          uuid NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    organization_uid  uuid NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    role              text NOT NULL CHECK (role IN ('admin', 'user', 'viewer')),
    invited_by_uid    uuid REFERENCES users(uid),
    invited_at        timestamptz,
    joined_at         timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);

CREATE UNIQUE INDEX organization_members_user_org_idx
    ON organization_members (user_uid, organization_uid) WHERE deleted_at IS NULL;
CREATE INDEX organization_members_org_idx ON organization_members (organization_uid) WHERE deleted_at IS NULL;
CREATE INDEX organization_members_user_idx ON organization_members (user_uid) WHERE deleted_at IS NULL;

COMMENT ON TABLE organization_members IS 'Links users to organizations with role-based access control.';
COMMENT ON COLUMN organization_members.role IS 'Role in the organization: admin (full access), user (read/write), viewer (read-only).';
COMMENT ON COLUMN organization_members.invited_by_uid IS 'User that sent the invitation. NULL for founders/migrated users.';
COMMENT ON COLUMN organization_members.invited_at IS 'When invitation was sent. NULL for immediate additions.';
COMMENT ON COLUMN organization_members.joined_at IS 'When user accepted invitation. NULL = pending invitation.';
```

#### user_tokens

Authentication tokens. `organization_uid` is optional (NULL for global refresh tokens).

```sql
CREATE TABLE user_tokens (
    uid               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_uid          uuid NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    organization_uid  uuid REFERENCES organizations(uid) ON DELETE CASCADE,
    token             text NOT NULL,
    type              text NOT NULL CHECK (type IN ('pat', 'refresh')),
    properties        jsonb,
    expires_at        timestamptz,
    last_active_at    timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);

CREATE UNIQUE INDEX user_tokens_token_idx ON user_tokens (token) WHERE deleted_at IS NULL;
CREATE INDEX user_tokens_user_uid_idx ON user_tokens (user_uid) WHERE deleted_at IS NULL;
CREATE INDEX user_tokens_expires_at_idx ON user_tokens (expires_at) WHERE deleted_at IS NULL AND expires_at IS NOT NULL;

COMMENT ON COLUMN user_tokens.user_uid IS 'User that owns this token.';
COMMENT ON COLUMN user_tokens.organization_uid IS 'Organization scope for PAT tokens. NULL for global refresh tokens.';
```

#### state_entries

Key-value state storage with optional user reference for user-scoped entries (email confirmation, password reset, etc.).

```sql
CREATE TABLE state_entries (
    uid               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_uid  uuid REFERENCES organizations(uid) ON DELETE CASCADE,
    user_uid          uuid REFERENCES users(uid) ON DELETE CASCADE,
    key               text NOT NULL CHECK (length(key) <= 255),
    value             jsonb,
    expires_at        timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz,
    UNIQUE (organization_uid, key)
);

CREATE INDEX idx_state_entries_expires ON state_entries (expires_at) WHERE expires_at IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX idx_state_entries_org ON state_entries (organization_uid) WHERE deleted_at IS NULL;
CREATE INDEX idx_state_entries_user ON state_entries (user_uid) WHERE user_uid IS NOT NULL AND deleted_at IS NULL;

COMMENT ON TABLE state_entries IS 'Key-value state storage for notifications, user tokens (email confirm, password reset), and distributed locking.';
COMMENT ON COLUMN state_entries.organization_uid IS 'Optional org reference for org-scoped state.';
COMMENT ON COLUMN state_entries.user_uid IS 'Optional user reference for user-scoped state (email confirmation, password reset).';
COMMENT ON COLUMN state_entries.key IS 'Namespaced key using slash separators (e.g., email_confirm/{token}, password_reset/{token}).';
COMMENT ON COLUMN state_entries.value IS 'JSON value storing context data.';
COMMENT ON COLUMN state_entries.expires_at IS 'Optional TTL for automatic cleanup (NULL = never expires).';
```

### Email Confirmation Flow

Email confirmation tokens are stored in `state_entries` with:
- `key`: `email_confirm/{token}` (random UUID)
- `user_uid`: Reference to the user being verified
- `value`: `{"email": "user@example.com"}` (email at time of request)
- `expires_at`: Token expiry (e.g., 24 hours)

```
POST /api/v1/auth/register
Body: {"email": "user@example.com", "password": "...", "name": "..."}

1. Create user with email_verified_at = NULL
2. Generate confirmation token
3. Store in state_entries: key="email_confirm:{token}", user_uid=user.uid, expires_at=+24h
4. Send confirmation email with link: /confirm-email?token={token}

GET /api/v1/auth/confirm-email?token={token}

1. Lookup state_entries by key="email_confirm:{token}"
2. Verify not expired
3. Update users.email_verified_at = NOW() for user_uid
4. Delete state_entry
5. Return success (or redirect to login)
```

### Password Reset Flow

Password reset tokens also use `state_entries`:
- `key`: `password_reset:{token}`
- `user_uid`: Reference to the user
- `value`: `{"email": "user@example.com"}`
- `expires_at`: Token expiry (e.g., 1 hour)

```
POST /api/v1/auth/forgot-password
Body: {"email": "user@example.com"}

POST /api/v1/auth/reset-password
Body: {"token": "...", "newPassword": "..."}
```

## Role Definitions

| Role | Description | Permissions |
|------|-------------|-------------|
| `super_admin` | Platform-wide access | Access all organizations, manage platform settings |
| `admin` | Full organization access | CRUD all resources, manage members, delete org |
| `user` | Standard operator access | CRUD checks, view results, acknowledge incidents |
| `viewer` | Read-only access | View checks, results, incidents. No modifications |

### Permission Matrix

| Action | Super Admin | Admin | User | Viewer |
|--------|-------------|-------|------|--------|
| Access any organization | Yes | No | No | No |
| View checks | Yes | Yes | Yes | Yes |
| Create/edit checks | Yes | Yes | Yes | No |
| Delete checks | Yes | Yes | Yes | No |
| View results | Yes | Yes | Yes | Yes |
| View incidents | Yes | Yes | Yes | Yes |
| Acknowledge incidents | Yes | Yes | Yes | No |
| Manage members | Yes | Yes | No | No |
| Manage org settings | Yes | Yes | No | No |
| Create PAT tokens | Yes | Yes | Yes | Yes (own only) |
| Delete organization | Yes | Yes | No | No |
| Create organizations | Yes | No | No | No |

## Go Models

### User Model

```go
// internal/db/models/user.go

// User represents a global user account.
type User struct {
    UID             string     `bun:"uid,pk,type:varchar(36)"`
    Email           string     `bun:"email,notnull"`
    Name            string     `bun:"name"`
    AvatarURL       string     `bun:"avatar_url"`
    PasswordHash    *string    `bun:"password_hash"`
    EmailVerifiedAt *time.Time `bun:"email_verified_at"`
    SuperAdmin      bool       `bun:"super_admin"`
    LastActiveAt    *time.Time `bun:"last_active_at"`
    CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
    UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
    DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewUser creates a new user with generated UID.
func NewUser(email string) *User {
    now := time.Now()
    return &User{
        UID:       uuid.New().String(),
        Email:     email,
        CreatedAt: now,
        UpdatedAt: now,
    }
}

// UserUpdate represents fields that can be updated.
type UserUpdate struct {
    Email           *string
    Name            *string
    AvatarURL       *string
    PasswordHash    *string
    EmailVerifiedAt *time.Time
    SuperAdmin      *bool
    LastActiveAt    *time.Time
}
```

### UserProvider Model

```go
// ProviderType represents an external auth provider.
type ProviderType string

const (
    ProviderTypeGoogle    ProviderType = "google"
    ProviderTypeGitHub    ProviderType = "github"
    ProviderTypeGitLab    ProviderType = "gitlab"
    ProviderTypeMicrosoft ProviderType = "microsoft"
    ProviderTypeTwitter   ProviderType = "twitter"
    ProviderTypeSlack     ProviderType = "slack"
    ProviderTypeSAML      ProviderType = "saml"
    ProviderTypeOIDC      ProviderType = "oidc"
)

// UserProvider links a user to an external auth provider.
type UserProvider struct {
    UID          string       `bun:"uid,pk,type:varchar(36)"`
    UserUID      string       `bun:"user_uid,notnull"`
    ProviderType ProviderType `bun:"provider_type,notnull"`
    ProviderID   string       `bun:"provider_id,notnull"`
    Metadata     JSONMap      `bun:"metadata,type:jsonb,nullzero"`
    CreatedAt    time.Time    `bun:"created_at,notnull,default:current_timestamp"`
    UpdatedAt    time.Time    `bun:"updated_at,notnull,default:current_timestamp"`
}
```

### OrganizationMember Model

```go
// MemberRole represents a user's role in an organization.
type MemberRole string

const (
    MemberRoleAdmin  MemberRole = "admin"
    MemberRoleUser   MemberRole = "user"
    MemberRoleViewer MemberRole = "viewer"
)

// OrganizationMember links a user to an organization with a role.
type OrganizationMember struct {
    UID             string     `bun:"uid,pk,type:varchar(36)"`
    UserUID         string     `bun:"user_uid,notnull"`
    OrganizationUID string     `bun:"organization_uid,notnull"`
    Role            MemberRole `bun:"role,notnull"`
    InvitedByUID    *string    `bun:"invited_by_uid"`
    InvitedAt       *time.Time `bun:"invited_at"`
    JoinedAt        *time.Time `bun:"joined_at"`
    CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
    UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
    DeletedAt       *time.Time `bun:"deleted_at"`

    // Relations (for eager loading)
    User         *User         `bun:"rel:belongs-to,join:user_uid=uid"`
    Organization *Organization `bun:"rel:belongs-to,join:organization_uid=uid"`
}

// NewOrganizationMember creates a new membership with generated UID.
func NewOrganizationMember(userUID, orgUID string, role MemberRole) *OrganizationMember {
    now := time.Now()
    return &OrganizationMember{
        UID:             uuid.New().String(),
        UserUID:         userUID,
        OrganizationUID: orgUID,
        Role:            role,
        JoinedAt:        &now,
        CreatedAt:       now,
        UpdatedAt:       now,
    }
}

// OrganizationMemberUpdate represents fields that can be updated.
type OrganizationMemberUpdate struct {
    Role     *MemberRole
    JoinedAt *time.Time
}
```

## Authentication Flow

### Login Flow (Global User)

1. User enters email + password
2. Credentials validated against `users` table (global)
3. If `super_admin`: can access any org, skip membership check
4. Otherwise: user selects organization from their memberships
5. JWT issued with `user_uid`, `org_uid`, `role`, and `super_admin`

### JWT Claims

```json
{
  "sub": "user_uid",
  "org": "org_uid",
  "role": "admin|user|viewer",
  "super_admin": true,
  "email": "user@example.com"
}
```

### Auth Context

```go
type AuthContext struct {
    UserUID         string
    OrganizationUID string
    Role            MemberRole
    SuperAdmin      bool
    Email           string
}

// GetAuthContext extracts auth context from request
func GetAuthContext(ctx context.Context) (*AuthContext, bool)

// RequireRole middleware checks minimum role (super_admin bypasses)
func RequireRole(minRole MemberRole) func(http.Handler) http.Handler

// RequireSuperAdmin middleware requires super_admin flag
func RequireSuperAdmin() func(http.Handler) http.Handler
```

## REST API

### Login Endpoints

```
POST /api/v1/auth/login
Body: {"email": "user@example.com", "password": "..."}

Response (single org or super_admin):
{
  "accessToken": "...",
  "refreshToken": "...",
  "user": {"uid": "...", "email": "...", "superAdmin": false},
  "organization": {"uid": "...", "slug": "default"}
}

Response (multiple orgs):
{
  "userUid": "...",
  "superAdmin": false,
  "organizations": [
    {"uid": "...", "slug": "acme-corp", "role": "admin"},
    {"uid": "...", "slug": "beta-inc", "role": "viewer"}
  ],
  "selectOrgToken": "temporary-token-for-org-selection"
}

POST /api/v1/auth/select-org
Headers: Authorization: Bearer {selectOrgToken}
Body: {"organizationUid": "..."}

Response:
{
  "accessToken": "...",
  "refreshToken": "...",
  "organization": {"uid": "...", "slug": "acme-corp"}
}
```

### Backward Compatibility

Keep existing per-org login endpoint working:

```
POST /api/v1/orgs/{org}/auth/login
Body: {"email": "user@example.com", "password": "..."}
```

Validates credentials against global users table, then verifies membership (or super_admin) for specified org.

### Member Management Endpoints

```
GET    /api/v1/orgs/{org}/members              - List organization members
POST   /api/v1/orgs/{org}/members              - Invite member (by email)
GET    /api/v1/orgs/{org}/members/{uid}        - Get member details
PATCH  /api/v1/orgs/{org}/members/{uid}        - Update member role
DELETE /api/v1/orgs/{org}/members/{uid}        - Remove member

GET    /api/v1/me                              - Get current user
GET    /api/v1/me/organizations                - List my organizations
PATCH  /api/v1/me                              - Update my profile
```

#### List Members Response

```json
{
  "data": [
    {
      "uid": "member-uid",
      "userUid": "user-uid",
      "email": "admin@example.com",
      "name": "Admin User",
      "role": "admin",
      "joinedAt": "2025-01-01T00:00:00Z"
    }
  ]
}
```

#### Invite Member Request

```json
{
  "email": "newuser@example.com",
  "role": "user"
}
```

If user exists: creates membership, sends "you've been added" email.
If user doesn't exist: creates pending invitation, sends "create account" email.

## Edge Cases

### Super Admin Access

Super admins can:
- Access any organization without membership
- Create new organizations
- Manage platform-wide settings
- Still need to select an org context for org-scoped operations

### Pending Invitations

If member is invited but hasn't joined:
- `organization_members.joined_at` is NULL
- `organization_members.invited_at` is set
- User may or may not exist (invited by email)

### Organization Deletion

- Memberships soft-deleted with org
- User remains (user still exists globally)
- Can rejoin if org restored

### Last Admin Leaves

Prevent removing the last admin from an organization:
```go
func (s *MemberService) RemoveMember(ctx context.Context, orgUID, memberUID string) error {
    member, _ := s.GetMember(ctx, orgUID, memberUID)
    if member.Role == MemberRoleAdmin {
        adminCount, _ := s.CountAdmins(ctx, orgUID)
        if adminCount <= 1 {
            return ErrCannotRemoveLastAdmin
        }
    }
    // proceed with removal
}
```

## Implementation Steps

### Step 1: Database Schema

1.1. Update migration file with new schema:
   - `users` table with `super_admin` column
   - `user_providers` table
   - `organization_members` table
   - `user_tokens` table (organization_uid nullable)

1.2. Create Go models:
   - `internal/db/models/user.go` - User, UserUpdate
   - `internal/db/models/user_provider.go` - UserProvider
   - `internal/db/models/organization_member.go` - OrganizationMember

### Step 2: Repository Layer

2.1. Create `UserRepository`:
   - `Create`, `GetByUID`, `GetByEmail`, `Update`, `Delete`

2.2. Create `OrganizationMemberRepository`:
   - `Create`, `GetByUID`, `GetByUserAndOrg`
   - `ListByOrganization`, `ListByUser`
   - `Update`, `Delete`
   - `CountAdminsByOrg`

### Step 3: Auth Service

3.1. Update login flow:
   - Validate against users table (global)
   - Check `super_admin` flag
   - Load memberships for org selection
   - Support multi-org selection flow

3.2. Update JWT claims:
   - Include `user_uid`, `org_uid`, `role`, `super_admin`

3.3. Update token validation:
   - Verify membership or super_admin
   - Check role permissions

### Step 4: Middleware

4.1. Update `RequireAuth` middleware:
   - Extract user from JWT
   - Load membership for current org (skip for super_admin)
   - Populate auth context with role and super_admin

4.2. Create `RequireRole` middleware:
   - Check minimum role for endpoint
   - Super admin bypasses role checks

4.3. Create `RequireSuperAdmin` middleware:
   - Require super_admin flag

### Step 5: Handlers

5.1. Create member management handlers:
   - List, invite, update, remove members
   - Role change validation

5.2. Update existing handlers:
   - Use global user lookups
   - Add role checks where needed
   - Super admin bypass logic

### Step 6: Frontend

6.1. Add org switcher component

6.2. Update login flow for multi-org

6.3. Add member management UI (admin only)

### Step 7: Testing

7.1. Unit tests for new services

7.2. Integration tests for auth flows:
   - Login with single org
   - Login with multiple orgs
   - Super admin access
   - Role-based access

## CLI Client

```bash
# User commands (self-service)
solidping client me                    # Show current user
solidping client me orgs               # List my organizations

# Member commands (admin only)
solidping client members list
solidping client members invite user@example.com --role user
solidping client members update <uid> --role admin
solidping client members remove <uid>

# Org switching
solidping client auth switch-org <org-slug>
```

## Error Codes

| Code | Description |
|------|-------------|
| `USER_NOT_FOUND` | User UID not found |
| `MEMBER_NOT_FOUND` | Member UID not found |
| `NOT_A_MEMBER` | User is not a member of this organization |
| `CANNOT_REMOVE_LAST_ADMIN` | Cannot remove the last admin from organization |
| `INVITATION_EXPIRED` | Invitation has expired |
| `ALREADY_A_MEMBER` | User is already a member of this organization |
| `INVALID_ROLE` | Role must be admin, user, or viewer |

## Security Considerations

1. **Super Admin Protection**: Only existing super admins can grant super_admin flag.

2. **Password Unification**: Same email = same password. This is expected (like GitHub/Slack).

3. **Compromised Account Impact**: Affects all orgs. Mitigate with:
   - Strong password requirements
   - MFA support (future)
   - Audit logging of org access

4. **Role Escalation Prevention**: Only admins can change roles. No self-promotion.

5. **Token Scoping**: PAT tokens remain org-scoped. Refresh tokens are global but require org selection.

---

**Status**: Ready for Implementation | **Created**: 2026-01-01
