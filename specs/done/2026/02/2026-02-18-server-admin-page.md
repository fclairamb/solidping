# Server Admin Page

## Overview

Add a "Server" settings page accessible only to super admin users. This page provides a tabbed interface for managing server-wide configuration parameters: Web, Mail, Authentication, and Performance. Currently, these settings can only be changed via environment variables or config files — this page enables runtime configuration through the existing system parameters API.

## Motivation

1. Super admins need a UI to manage server-wide settings without restarting the server or editing config files.
2. OAuth provider credentials (Google, GitHub, GitLab, Microsoft, Slack) and email settings are currently only configurable at startup.
3. The system parameters API (`/api/v1/system/parameters`) already exists with CRUD operations and secret masking, but has no frontend.

## Current State

- The backend has a `systemconfig` package with known parameter definitions (JWT secret, email settings, workers).
- The system parameters API at `/api/v1/system/parameters` supports GET/PUT/DELETE with `RequireSuperAdmin` middleware.
- Secret parameters are masked (`******`) in API responses.
- OAuth provider configs exist in `config.go` (Google, GitHub, GitLab, Microsoft, Slack) but are not yet registered as system parameters.
- The frontend auth context has `isAdmin` but not `isSuperAdmin`. The backend already returns `role: "superadmin"` for super admin users.

---

## Backend Changes

### 1. Add Protocol field to EmailConfig

**File**: `back/internal/config/config.go`

Add to `EmailConfig`:
```go
Protocol string `koanf:"protocol"` // SMTP encryption: none, starttls, ssl (default: starttls)
```

Update defaults in `Load()`:
```go
Email: EmailConfig{
    Port:     587,
    Protocol: "starttls",
    Enabled:  false,
},
```

### 2. Add OAuth + protocol parameter definitions

**File**: `back/internal/systemconfig/systemconfig.go`

Add new parameter key constants:
```go
const (
    // ... existing keys ...
    KeyEmailProtocol          ParameterKey = "email.protocol"

    KeyGoogleClientID         ParameterKey = "auth.google.client_id"
    KeyGoogleClientSecret     ParameterKey = "auth.google.client_secret"
    KeyGitHubClientID         ParameterKey = "auth.github.client_id"
    KeyGitHubClientSecret     ParameterKey = "auth.github.client_secret"
    KeyGitLabClientID         ParameterKey = "auth.gitlab.client_id"
    KeyGitLabClientSecret     ParameterKey = "auth.gitlab.client_secret"
    KeyMicrosoftClientID      ParameterKey = "auth.microsoft.client_id"
    KeyMicrosoftClientSecret  ParameterKey = "auth.microsoft.client_secret"
    KeySlackAppID             ParameterKey = "auth.slack.app_id"
    KeySlackClientID          ParameterKey = "auth.slack.client_id"
    KeySlackClientSecret      ParameterKey = "auth.slack.client_secret"
    KeySlackSigningSecret     ParameterKey = "auth.slack.signing_secret"
)
```

Add corresponding `ParameterDefinition` entries in `getKnownParameters()` with:
- Env var mappings (e.g., `SP_GOOGLE_CLIENT_ID`, `SP_GITHUB_CLIENT_SECRET`)
- `Secret: true` for all `*_secret` and `*_password` keys
- `ApplyFunc` callbacks that set the corresponding `config.Config` fields

---

## Frontend Changes

### 1. Expose super admin in auth context

**File**: `apps/dash0/src/contexts/AuthContext.tsx`

Add `isSuperAdmin` to the `User` interface:
```typescript
interface User {
  email: string;
  name?: string;
  avatarUrl?: string;
  roles: string[];
  isAdmin: boolean;
  isSuperAdmin: boolean;
}
```

Set it wherever `User` is constructed:
```typescript
isSuperAdmin: data.user.role === "superadmin",
```

### 2. Conditional sidebar item

**File**: `apps/dash0/src/components/layout/AppSidebar.tsx`

Add `Server` to lucide-react imports.

Add a super admin nav section (after test mode section):
```tsx
{user?.isSuperAdmin && (
  <SidebarGroup>
    <SidebarGroupContent>
      <SidebarMenu>
        <SidebarMenuItem>
          <SidebarMenuButton
            asChild
            isActive={location.pathname === `/orgs/${org}/server`}
            tooltip="Server"
          >
            <Link to="/orgs/$org/server" params={{ org }}>
              <Server />
              <span>Server</span>
            </Link>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarGroupContent>
  </SidebarGroup>
)}
```

### 3. System parameters API hooks

**File**: `apps/dash0/src/api/hooks.ts`

```typescript
// Fetch all system parameters
export function useSystemParameters() {
  return useQuery({
    queryKey: ["system-parameters"],
    queryFn: () => apiFetch<{ data: SystemParameter[] }>("/api/v1/system/parameters"),
  });
}

// Update a system parameter
export function useSetSystemParameter() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ key, value, secret }: { key: string; value: any; secret?: boolean }) =>
      apiFetch(`/api/v1/system/parameters/${key}`, {
        method: "PUT",
        body: JSON.stringify({ value, secret }),
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["system-parameters"] }),
  });
}
```

### 4. Server settings page

**New file**: `apps/dash0/src/routes/orgs/$org/server.tsx`

A page with:
- Title: "Server Settings"
- Tabs component with 4 tabs

#### Web tab
| Parameter | Type | Label |
|-----------|------|-------|
| `base_url` | text | Base URL |
| `jwt_secret` | secret | JWT Secret |

#### Mail tab
| Parameter | Type | Label |
|-----------|------|-------|
| `email.enabled` | toggle | Enabled |
| `email.host` | text | SMTP Host |
| `email.port` | number | SMTP Port |
| `email.username` | text | Username |
| `email.password` | secret | Password |
| `email.auth_type` | select (plain/login/cram-md5) | Auth Type |
| `email.protocol` | select (none/starttls/ssl) | Encryption |
| `email.from` | text | From Address |
| `email.from_name` | text | From Name |

#### Authentication tab

Each provider in its own card:

**Google**:
| Parameter | Type | Label |
|-----------|------|-------|
| `auth.google.client_id` | text | Client ID |
| `auth.google.client_secret` | secret | Client Secret |

**GitHub**:
| Parameter | Type | Label |
|-----------|------|-------|
| `auth.github.client_id` | text | Client ID |
| `auth.github.client_secret` | secret | Client Secret |

**GitLab**:
| Parameter | Type | Label |
|-----------|------|-------|
| `auth.gitlab.client_id` | text | Client ID |
| `auth.gitlab.client_secret` | secret | Client Secret |

**Microsoft**:
| Parameter | Type | Label |
|-----------|------|-------|
| `auth.microsoft.client_id` | text | Client ID |
| `auth.microsoft.client_secret` | secret | Client Secret |

**Slack**:
| Parameter | Type | Label |
|-----------|------|-------|
| `auth.slack.app_id` | text | App ID |
| `auth.slack.client_id` | text | Client ID |
| `auth.slack.client_secret` | secret | Client Secret |
| `auth.slack.signing_secret` | secret | Signing Secret |

#### Performance tab
| Parameter | Type | Label |
|-----------|------|-------|
| `check_workers` | number | Check Runners |
| `job_workers` | number | Job Runners |

#### Secret field behavior
- Secret fields display `******` when loaded
- An "Edit" button reveals a text input to set a new value
- Saving a secret field sends `{ value: "new-value", secret: true }`
- Cancel reverts to the masked display

#### Save behavior
- Each tab has a "Save" button
- Only changed parameters are sent to the API
- Toast notification on success/failure

### 5. Route generation

After creating the route file, run TanStack Router code generator to update `routeTree.gen.ts`.

---

## Key Files

| File | Change |
|------|--------|
| `back/internal/config/config.go` | Add `Protocol` to `EmailConfig` |
| `back/internal/systemconfig/systemconfig.go` | Add OAuth + protocol parameter definitions |
| `apps/dash0/src/contexts/AuthContext.tsx` | Add `isSuperAdmin` to User |
| `apps/dash0/src/components/layout/AppSidebar.tsx` | Add conditional "Server" nav item |
| `apps/dash0/src/api/hooks.ts` | Add system parameter hooks |
| `apps/dash0/src/routes/orgs/$org/server.tsx` | **New** — Server settings page with tabs |

---

## Verification

### Manual
1. Start backend: `SP_RUNMODE=test make dev-backend`
2. Start frontend: `cd apps/dash0 && bun run dev`
3. Log in as super admin (`admin@solidping.com` / `solidpass` in default mode, or `test@test.com` / `test` in test mode)
4. Verify "Server" appears in the sidebar
5. Click "Server" — verify 4 tabs render with parameter forms
6. Update `base_url`, verify save works and value persists on reload
7. Verify secret fields show `******` and can be edited
8. Log in as a non-super-admin user — verify "Server" does NOT appear

### API
```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# List all parameters
curl -s -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/system/parameters' | jq

# Set an OAuth parameter
curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"value":"my-google-client-id"}' \
  'http://localhost:4000/api/v1/system/parameters/auth.google.client_id' | jq
```

### Lint
```bash
make lint
make test
```

---

**Status**: Draft | **Created**: 2026-02-18
