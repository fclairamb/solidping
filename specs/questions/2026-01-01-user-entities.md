# User Entity Architecture Decision

## Question

Should we create a user entity per organization or a global user entity with memberships in each organization?

## Recommendation: Global User with Per-Organization Memberships

After deep analysis, **Option 2 (Global User + Memberships)** is the better choice.

## Current State Analysis

The current model uses **one User per Organization**:
- `User.OrganizationUID` directly links user to one org
- `User.UserID` (email) can exist in multiple orgs
- Simple but limiting for multi-org scenarios

## Options Compared

### Option 1: User Per Organization (Current)

```
users
├── uid
├── organization_uid  ← direct FK, user belongs to ONE org
├── user_id (email)
├── password_hash
├── role
```

**Pros:**
- Simpler data model
- Clear ownership
- No complex permission system
- Easier to reason about

**Cons:**
- Users need multiple accounts for multiple orgs
- No unified identity
- Password confusion ("which password for which org?")
- SSO integration is awkward

### Option 2: Global User + Memberships (Recommended)

```
identities (global)
├── uid
├── email (UNIQUE globally)
├── password_hash
├── name

organization_members
├── uid
├── identity_uid FK→identities
├── organization_uid FK→organizations
├── role
├── UNIQUE(identity_uid, organization_uid)
```

**Pros:**
- Single sign-on experience
- User can belong to multiple orgs with one account
- Industry standard pattern (GitHub, Slack, Datadog, PagerDuty)
- Better for B2B SaaS (consultants, MSPs, multi-org enterprises)
- Cleaner SSO integration
- Separates identity (who you are) from authorization (what you can do)

**Cons:**
- More complex data model
- Need "current org" context in UI/API
- Permission system is more complex
- Requires migration

## Use Cases That Favor Global User

| Use Case | Per-Org User | Global User |
|----------|--------------|-------------|
| Small team / single org | Works | Works |
| MSP managing multiple clients | Multiple accounts needed | One account, switch orgs |
| Consultant invited to client monitoring | Creates new account each time | Existing account, just add membership |
| Enterprise with prod/staging/regional orgs | Multiple logins | One login, multiple memberships |
| SSO integration | Complex linking | Authenticate once, authorize per org |

## Security Analysis

**Global User Model:**
- Compromised account affects all orgs user belongs to
- BUT: This is expected behavior (same as GitHub/Slack)
- Single point for password reset, MFA, account recovery
- Audit trail spans all orgs for one identity

**Per-Org Model:**
- Compromised password affects only one org
- BUT: Users often reuse passwords anyway
- Multiple passwords to manage = weaker security in practice

## Proposed Data Model

### identities

Global user accounts (authentication).

| Column | Type | Notes |
|--------|------|-------|
| uid | uuid | PK |
| email | text | UNIQUE, globally unique |
| password_hash | text | nullable (SSO users may not have) |
| name | text | Display name |
| avatar_url | text | Profile picture |
| email_verified | bool | Email verification status |
| created_at | timestamp | |
| updated_at | timestamp | |
| deleted_at | timestamp | Soft delete |

### identity_providers

Links identities to external auth providers (SSO).

| Column | Type | Notes |
|--------|------|-------|
| uid | uuid | PK |
| identity_uid | uuid | FK→identities |
| provider_type | text | google, github, gitlab, microsoft, saml |
| provider_id | text | External provider's user ID |
| metadata | jsonb | Provider-specific data |
| created_at | timestamp | |

### organization_members

Memberships linking identities to organizations (authorization).

| Column | Type | Notes |
|--------|------|-------|
| uid | uuid | PK |
| identity_uid | uuid | FK→identities |
| organization_uid | uuid | FK→organizations |
| role | text | admin, member, viewer |
| invited_by_uid | uuid | FK→identities (who invited) |
| joined_at | timestamp | When accepted invitation |
| created_at | timestamp | |
| updated_at | timestamp | |
| deleted_at | timestamp | Soft delete |
| | | UNIQUE(identity_uid, organization_uid) |

### user_tokens (modified)

| Column | Type | Notes |
|--------|------|-------|
| uid | uuid | PK |
| identity_uid | uuid | FK→identities |
| organization_uid | uuid | FK→organizations (token scope, nullable for global tokens) |
| token | text | |
| type | text | pat, refresh |
| ... | | |

## Auth Flow Changes

### Current Flow
1. User selects org → enters credentials → logged into that org

### New Flow
1. User enters email → enters password → authenticated
2. If multiple orgs: select org to work in
3. Org context stored in session/token
4. User can switch orgs without re-authenticating

### API Changes

JWT tokens will include:
- `sub`: identity_uid (who you are)
- `org`: organization_uid (current context)
- `role`: role in that org

APIs remain org-scoped (`/api/v1/orgs/{org}/...`) but middleware validates membership instead of user ownership.

## Migration Path

### Phase 1: Schema Migration
1. Create `identities` table
2. Create `organization_members` table
3. Migrate existing users:
   - For each User, create Identity with same email
   - Create OrganizationMember linking Identity to User's org
4. Keep `users` table temporarily for rollback

### Phase 2: Code Migration
1. Update auth handlers to use new model
2. Update middleware to check membership
3. Update all services that query users

### Phase 3: Cleanup
1. Drop old `users` table
2. Rename `identities` → `users` if preferred

## Edge Cases

### Same Email, Different Orgs (Current State)
If `admin@example.com` exists in org-a and org-b with different passwords:
- Migration creates ONE identity
- Creates TWO memberships
- User must use one password (prompt to choose or reset)

### Invitation Flow
1. Admin invites `new@example.com` to org
2. System checks if identity exists
   - If yes: create membership, send "you've been added" email
   - If no: create pending invitation, send "create account" email
3. User accepts, membership activated

### Org Deletion
- Memberships soft-deleted with org
- Identity remains (user still exists)
- Can rejoin if org restored

## Implementation Effort

**Low effort:**
- Schema changes
- Basic auth flow changes

**Medium effort:**
- Org switcher UI
- Migration script for existing data
- Update all org-scoped queries

**Higher effort:**
- Invitation system
- SSO integration refactor
- Token scoping changes

## Conclusion

The global user model is:
1. **Industry standard** - What users expect from modern SaaS
2. **Better UX** - Single account for all orgs
3. **Future-proof** - Enables SSO, SCIM, proper identity management
4. **Worth the complexity** - Migration effort pays off long-term

Recommended to implement this before the user base grows, as migration complexity increases with data volume.
