# Extend User/Org Info in Auth Responses

## Overview

Extend the `/api/v1/auth/me` endpoint to return user profile information (name, avatar), organization name, and a list of all organizations the user belongs to. Add org-switching UI in the dashboard sidebar for users with multiple memberships.

## Motivation

1. The sidebar footer shows only the user's email and a generic icon. Real names and avatars make the UI more personal.
2. Organizations only have a `slug` — a URL-safe identifier, not a human-readable label. A `name` field is needed for display.
3. Users belonging to multiple organizations have no way to switch between them without logging out. The `switch-org` API exists but has no frontend UI.

## Current State

**`UserInfo` struct** (`back/internal/handlers/auth/service.go:79-83`):
```go
type UserInfo struct {
    UID   string `json:"uid"`
    Email string `json:"email"`
    Role  string `json:"role"`
}
```

The `User` DB model already has `Name` and `AvatarURL` fields, but they are not exposed in auth responses.

**`OrganizationInfo` struct** (`back/internal/handlers/auth/service.go:86-89`):
```go
type OrganizationInfo struct {
    UID  string `json:"uid"`
    Slug string `json:"slug"`
}
```

The `Organization` DB model has no `name` field.

**`MeResponse`** (`back/internal/handlers/auth/service.go:102-105`):
```go
type MeResponse struct {
    User         *UserInfo         `json:"user"`
    Organization *OrganizationInfo `json:"organization"`
}
```

No list of available organizations.

**Frontend** (`apps/dash0/src/contexts/AuthContext.tsx`): Uses `email`, `role` from user and `slug` from org. No org switching. Sidebar shows generic `User2` icon and email only.

## Database Schema

### Update existing initial migration

Add `name text` column to the `organizations` table in the existing initial migration files:

- `back/internal/db/sqlite/migrations/20251207000001_initial.up.sql`
- `back/internal/db/postgres/migrations/20251207000001_initial.up.sql`

The `name` column is nullable. When empty, the frontend falls back to displaying the slug.

### Model update

In `back/internal/db/models/organization.go`:

```go
type Organization struct {
    UID       string     `bun:"uid,pk,type:varchar(36)"`
    Slug      string     `bun:"slug,notnull"`
    Name      string     `bun:"name"`
    CreatedAt time.Time  `bun:"created_at,notnull,default:current_timestamp"`
    UpdatedAt time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
    DeletedAt *time.Time `bun:"deleted_at"`
}

type OrganizationUpdate struct {
    Slug *string
    Name *string
}
```

Update `NewOrganization` to accept `name`:
```go
func NewOrganization(slug, name string) *Organization { ... }
```

Update `UpdateOrganization` in both sqlite and postgres to handle `Name`.

## REST API

### Extended structs

```go
type UserInfo struct {
    UID       string `json:"uid"`
    Email     string `json:"email"`
    Name      string `json:"name,omitempty"`
    AvatarURL string `json:"avatarUrl,omitempty"`
    Role      string `json:"role"`
}

type OrganizationInfo struct {
    UID  string `json:"uid"`
    Slug string `json:"slug"`
    Name string `json:"name,omitempty"`
}

type OrganizationSummary struct {
    Slug string `json:"slug"`
    Name string `json:"name,omitempty"`
    Role string `json:"role"`
}
```

### GET `/api/v1/auth/me` — Updated response

The `MeResponse` gains an `organizations` field listing all orgs the user belongs to:

```go
type MeResponse struct {
    User          *UserInfo              `json:"user"`
    Organization  *OrganizationInfo      `json:"organization"`
    Organizations []OrganizationSummary  `json:"organizations"`
}
```

Example response:
```json
{
  "user": {
    "uid": "550e8400-...",
    "email": "admin@solidping.com",
    "name": "Admin User",
    "avatarUrl": "https://avatars.githubusercontent.com/u/123",
    "role": "admin"
  },
  "organization": {
    "uid": "660f9500-...",
    "slug": "default",
    "name": "Default Organization"
  },
  "organizations": [
    { "slug": "default", "name": "Default Organization", "role": "admin" },
    { "slug": "acme", "name": "Acme Corp", "role": "user" }
  ]
}
```

The `organizations` array includes all orgs (including the current one). When a user belongs to only one org, the array has one entry and the frontend should not show an org switcher.

### POST `/api/v1/auth/login` — No change to response shape

The login response (`LoginResponse`) is **not** extended with organizations. The frontend fetches the organizations list from `/me` after login. This keeps the login response lean.

User `name` and `avatarUrl` are added to the `UserInfo` in the login response. Organization `name` is added to `OrganizationInfo`.

### POST `/api/v1/auth/switch-org` — Response includes updated user/org info

Same `LoginResponse` shape as before (with `name`/`avatarUrl` on user and `name` on org). No `organizations` list — the frontend already has it from `/me`.

## Backend Implementation

### Service changes (`back/internal/handlers/auth/service.go`)

**Update `UserInfo` construction** everywhere (Login, GetUserInfo, SwitchOrg) to include `Name` and `AvatarURL`:

```go
User: &UserInfo{
    UID:       user.UID,
    Email:     user.Email,
    Name:      user.Name,
    AvatarURL: user.AvatarURL,
    Role:      role,
},
```

**Update `OrganizationInfo` construction** everywhere to include `Name`:

```go
Organization: &OrganizationInfo{
    UID:  org.UID,
    Slug: org.Slug,
    Name: org.Name,
},
```

**Add `getOrganizationsForUser` helper**:

```go
func (s *Service) getOrganizationsForUser(ctx context.Context, userUID string) ([]OrganizationSummary, error) {
    members, err := s.db.ListMembersByUser(ctx, userUID)
    if err != nil {
        return nil, err
    }

    orgs := make([]OrganizationSummary, 0, len(members))
    for _, m := range members {
        if m.Organization == nil {
            continue
        }
        orgs = append(orgs, OrganizationSummary{
            Slug: m.Organization.Slug,
            Name: m.Organization.Name,
            Role: string(m.Role),
        })
    }
    return orgs, nil
}
```

This leverages `ListMembersByUser` which already eager-loads `Relation("Organization")` in both sqlite and postgres implementations.

**Update `GetUserInfo`** to call `getOrganizationsForUser` and include it in `MeResponse`.

### DB layer changes

- `back/internal/db/models/organization.go`: Add `Name` field
- `back/internal/db/sqlite/sqlite.go`: Update `UpdateOrganization` to handle `Name`
- `back/internal/db/postgres/postgres.go`: Same
- `back/test/testdata/testdata.go`: Add names to test organizations

## Frontend

### Type updates (`apps/dash0/src/contexts/AuthContext.tsx`)

```typescript
interface User {
  email: string;
  name?: string;
  avatarUrl?: string;
  roles: string[];
  isAdmin: boolean;
}

interface OrganizationSummary {
  slug: string;
  name?: string;
  role: string;
}

interface AuthContextType {
  user: User | null;
  org: string | null;
  organizations: OrganizationSummary[];
  isAuthenticated: boolean;
  isLoading: boolean;
  login: (org: string, email: string, password: string) => Promise<void>;
  loginWithOAuth: (accessToken: string, orgSlug: string) => Promise<void>;
  logout: () => Promise<void>;
  switchOrg: (orgSlug: string) => Promise<void>;
}
```

Update `MeResponse` to include `organizations` and the new user/org fields.

### AuthContext state management

- Add `organizations` state: `useState<OrganizationSummary[]>([])`
- Update `validateSession` and `loginWithOAuth` to populate `organizations` from `/me` response
- Update `login` to populate `name`/`avatarUrl` from login response
- Add `switchOrg` method:

```typescript
const switchOrg = async (orgSlug: string) => {
  const data = await apiFetch<AuthResponse>(`/api/v1/auth/switch-org`, {
    method: "POST",
    body: JSON.stringify({ org: orgSlug }),
  });
  setToken(data.accessToken);
  const resolvedOrg = data.organization?.slug || orgSlug;
  setStoredOrg(resolvedOrg);
  setOrg(resolvedOrg);
  setUser({
    email: data.user.email,
    name: data.user.name,
    avatarUrl: data.user.avatarUrl,
    roles: [data.user.role],
    isAdmin: data.user.role === "admin",
  });
  // Navigate to new org via TanStack Router
  navigate({ to: "/orgs/$org", params: { org: resolvedOrg } });
};
```

### Sidebar changes (`apps/dash0/src/components/layout/AppSidebar.tsx`)

**User display** — Replace generic icon with avatar when available, show name as primary text:

```tsx
{user?.avatarUrl ? (
  <img src={user.avatarUrl} alt="" className="size-8 rounded-lg object-cover" />
) : (
  <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-muted">
    <User2 className="size-4" />
  </div>
)}
<div className="grid flex-1 text-left text-sm leading-tight">
  <span className="truncate font-semibold">
    {user?.name || user?.email || "User"}
  </span>
  <span className="truncate text-xs text-muted-foreground">
    {user?.email}
  </span>
</div>
```

When `name` is set, it becomes the primary text and email moves to the secondary line. When `name` is empty, email remains the primary text and the secondary line shows the role (current behavior).

**Org switcher** — Add to the user dropdown when `organizations.length > 1`:

```tsx
{organizations.length > 1 && (
  <>
    <DropdownMenuSeparator />
    <DropdownMenuLabel className="text-xs text-muted-foreground">
      Switch Organization
    </DropdownMenuLabel>
    {organizations
      .filter((o) => o.slug !== org)
      .map((o) => (
        <DropdownMenuItem key={o.slug} onClick={() => switchOrg(o.slug)}>
          <Building className="mr-2 h-4 w-4" />
          {o.name || o.slug}
        </DropdownMenuItem>
      ))}
  </>
)}
```

**Sidebar header** — Show org name when available:

```tsx
<span className="text-xs text-muted-foreground">
  {organizations.find(o => o.slug === org)?.name || org}
</span>
```

## Testing Strategy

### Backend

- Test `GetUserInfo` returns `name`, `avatarUrl` on user and `name` on org
- Test `GetUserInfo` returns `organizations` list with correct slugs, names, and roles
- Test `Login` and `SwitchOrg` include user `name`/`avatarUrl` and org `name` in response
- Test `getOrganizationsForUser` with single-org and multi-org users

### E2E

- Login as multi-org test user, verify org switcher appears in user dropdown
- Click alternate org, verify navigation to new org dashboard
- Login as single-org user, verify org switcher is hidden
- Verify user name and avatar display when set

## Implementation Steps

1. Update existing initial migration: Add `name` column to `organizations` table in both sqlite and postgres initial migration files
2. Organization model: Add `Name` field, update `NewOrganization`, `OrganizationUpdate`
3. DB layer: Update `UpdateOrganization` in sqlite.go and postgres.go
4. Auth service structs: Extend `UserInfo`, `OrganizationInfo`, add `OrganizationSummary`, extend `MeResponse`
5. Auth service methods: Add `getOrganizationsForUser`, update `Login`/`GetUserInfo`/`SwitchOrg`
6. Test data: Add `Name` to test organizations
7. Backend tests: Update for new response fields
8. Frontend types: Update `AuthContext.tsx` interfaces
9. Frontend AuthContext: Add `organizations` state, `switchOrg` method, update login/me flows
10. Frontend AppSidebar: Avatar/name display, org-switching dropdown
11. E2E tests: Org-switching test

---

**Status**: Draft | **Created**: 2026-02-15
