# Personal Access Tokens Management Page

## Overview

Add a frontend page to list, create, and revoke Personal Access Tokens (PATs) for the current organization. The backend API already supports all required operations — this is a frontend-only change.

## Motivation

1. Users have no way to create PATs from the dashboard — they must use the CLI or API directly.
2. Users cannot see or revoke existing tokens without API calls.
3. PATs are essential for CI/CD integrations and programmatic access. A management UI makes them accessible to all users.

## Current State

**Backend** — fully implemented:
- `GET /api/v1/orgs/$org/tokens` — list tokens for current org (`back/internal/handlers/auth/handler.go:206-223`)
- `POST /api/v1/orgs/$org/tokens` — create a PAT (`back/internal/handlers/auth/handler.go:226-254`)
- `DELETE /api/v1/auth/tokens/$tokenUid` — revoke a token (`back/internal/handlers/auth/handler.go:257-278`)

**Response types** (`back/internal/handlers/auth/service.go:124-154`):
```go
type TokenInfo struct {
    UID        string     `json:"uid"`
    Name       string     `json:"name,omitempty"`
    Type       string     `json:"type"`
    OrgSlug    string     `json:"orgSlug,omitempty"`
    CreatedAt  time.Time  `json:"createdAt"`
    LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
    ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

type CreateTokenRequest struct {
    Name      string     `json:"name"`
    ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

type CreateTokenResponse struct {
    UID       string     `json:"uid"`
    Token     string     `json:"token"`
    Name      string     `json:"name"`
    ExpiresAt *time.Time `json:"expiresAt,omitempty"`
    CreatedAt time.Time  `json:"createdAt"`
}
```

**Frontend** — no token management UI exists.

## Frontend

### API hooks (`apps/dash0/src/api/hooks.ts`)

Add types and hooks following existing patterns (e.g., `useChecks`, `useDeleteCheck`):

```typescript
export interface TokenInfo {
  uid: string;
  name?: string;
  type: string;
  orgSlug?: string;
  createdAt: string;
  lastUsedAt?: string;
  expiresAt?: string;
}

export interface CreateTokenRequest {
  name: string;
  expiresAt?: string;
}

export interface CreateTokenResponse {
  uid: string;
  token: string;
  name: string;
  expiresAt?: string;
  createdAt: string;
}
```

- `useTokens(org)` — `GET /api/v1/orgs/${org}/tokens?type=pat`, queryKey `["tokens", org]`
- `useCreateToken(org)` — `POST /api/v1/orgs/${org}/tokens`, invalidates `["tokens", org]`
- `useRevokeToken()` — `DELETE /api/v1/auth/tokens/${uid}`, invalidates all `["tokens"]`

### Tokens page (`apps/dash0/src/routes/orgs/$org/tokens.index.tsx`)

Follows the `checks.index.tsx` layout pattern.

**Header**: Title "Tokens" + subtitle "Manage personal access tokens" + "New Token" button

**Table columns**:
| Name | Created | Last Used | Expires | Actions |
|------|---------|-----------|---------|---------|

- **Name**: Token name
- **Created**: Relative time (e.g., "2 days ago")
- **Last Used**: Relative time or "Never"
- **Expires**: Date or "Never"
- **Actions**: Dropdown with "Revoke" option

**States**:
- Loading: Skeleton rows
- Error: `QueryErrorView`
- Empty: "No tokens yet" + create button
- Search filter on token name

**Create Token Dialog** (`Dialog` component):
- Name input (required)
- Expiry select: 7 days, 30 days, 90 days, 1 year, No expiration
- On success: show the token string **once** in a copy-to-clipboard box with a warning that it won't be shown again

**Revoke Confirmation**: `AlertDialog` (same pattern as check deletion)

### Navigation (`apps/dash0/src/components/layout/AppSidebar.tsx`)

Add a "Tokens" link in the user dropdown menu (between ThemeToggle and the org switcher). Uses `KeyRound` icon from lucide-react. Links to `/orgs/$org/tokens`.

## Testing Strategy

### Manual
1. Navigate to `/orgs/default/tokens` — verify empty state
2. Create a token — verify token string displayed with copy button
3. Verify token appears in list after dialog closes
4. Revoke a token — verify confirmation dialog and removal
5. Verify "Tokens" link in user dropdown navigates correctly

## Implementation Steps

1. Add token types and hooks to `apps/dash0/src/api/hooks.ts`
2. Create `apps/dash0/src/routes/orgs/$org/tokens.index.tsx` with table, create dialog, revoke dialog
3. Add "Tokens" link to user dropdown in `apps/dash0/src/components/layout/AppSidebar.tsx`
4. Verify build and lint pass

---

**Status**: Draft | **Created**: 2026-02-15
