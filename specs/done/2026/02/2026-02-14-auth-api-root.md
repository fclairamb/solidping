# Move Auth API to Root Path

## Overview

Move the authentication API from `/api/v1/orgs/{org}/auth` to `/api/v1/auth`. Authentication is fundamentally a user-level concern, not an organization-level one. This change aligns the org-scoped auth endpoints with the OAuth endpoints that are already at `/api/v1/auth/{provider}`.

## Motivation

1. **Consistency**: OAuth endpoints (`/api/v1/auth/google/login`, etc.) are already at the root level. Having password login under `/orgs/{org}` while OAuth is at root is inconsistent.
2. **Semantic correctness**: A user authenticates themselves, then operates within an organization. Authentication should not be nested under a resource the user hasn't proven access to yet.
3. **Simplification**: The org slug in the JWT claims already determines what organization the session targets. Having it also in the URL is redundant for most auth operations.

## Current State

| Method | Current Path | Auth Required |
|--------|-------------|---------------|
| POST | `/api/v1/orgs/{org}/auth/login` | No |
| POST | `/api/v1/orgs/{org}/auth/refresh` | No |
| POST | `/api/v1/orgs/{org}/auth/logout` | Yes |
| GET | `/api/v1/orgs/{org}/auth/me` | Yes |
| GET | `/api/v1/orgs/{org}/auth/tokens` | Yes |
| POST | `/api/v1/orgs/{org}/auth/tokens` | Yes |
| DELETE | `/api/v1/orgs/{org}/auth/tokens/{tokenUid}` | Yes |

## New State

### Root-level auth endpoints (user-level operations)

| Method | Path | Auth Required | Notes |
|--------|------|---------------|-------|
| POST | `/api/v1/auth/login` | No | `org` optional in request body |
| POST | `/api/v1/auth/refresh` | No | Token already contains org claim |
| POST | `/api/v1/auth/logout` | Yes | Operates on current session |
| POST | `/api/v1/auth/switch-org` | Yes | Switch to a different org |
| GET | `/api/v1/auth/me` | Yes | Returns user + current org context |
| GET | `/api/v1/auth/tokens` | Yes | List all user's tokens across all orgs |
| DELETE | `/api/v1/auth/tokens/{tokenUid}` | Yes | User can only revoke own tokens |

### Org-scoped token endpoints

| Method | Path | Auth Required | Notes |
|--------|------|---------------|-------|
| GET | `/api/v1/orgs/{org}/tokens` | Yes | List PATs for this org |
| POST | `/api/v1/orgs/{org}/tokens` | Yes | Create PAT scoped to this org |

PATs are authorization grants for a specific organization, so they belong under the org resource — not under `/auth`. This gives a clean separation:

- `/api/v1/auth/*` = authentication (login, logout, session, user-level token management)
- `/api/v1/orgs/{org}/tokens` = org-scoped API credentials

This also opens the door for future org-level token management (e.g., service accounts, bot tokens) that wouldn't logically live under "auth".

## Detailed Changes

### POST `/api/v1/auth/login`

The `org` parameter moves from the URL path to the request body and becomes **optional**.

**Request:**
```json
{
  "org": "default",
  "email": "admin@solidping.com",
  "password": "solidpass"
}
```

**Org resolution:**
- If `org` is provided: authenticate and mint a JWT scoped to that org (after membership check)
- If `org` is omitted: authenticate and mint a JWT scoped to the user's **default org**

**Default org** is determined by: the organization of the user's most recent `refresh` token (i.e., the last org they logged into). If no refresh token exists (first login ever), fall back to the first org the user is a member of.

This ensures login **always returns a JWT**, which is critical for OAuth flows where not returning a token would force the user to redo the entire OAuth dance. The frontend can then show an org switcher if the user has multiple memberships.

```sql
-- Default org: org of the most recent refresh token
SELECT o.slug FROM user_tokens t
JOIN organizations o ON o.uid = t.organization_uid
WHERE t.user_uid = ? AND t.type = 'refresh'
ORDER BY t.created_at DESC LIMIT 1
```

**Anti-enumeration:** The login endpoint must return a generic `401 INVALID_CREDENTIALS` for all failure cases — whether the org doesn't exist, the user doesn't exist, or the password is wrong. This prevents attackers from probing for valid organization slugs. The current behavior of returning `ORGANIZATION_NOT_FOUND` separately is removed.

```go
// All of these return the same error:
// - org not found
// - user not found in org
// - wrong password
return Unauthorized("INVALID_CREDENTIALS", "Invalid credentials")
```

### POST `/api/v1/auth/refresh`

No change to request/response. The refresh token already encodes the org context.

### POST `/api/v1/auth/logout`

The JWT identifies the user and session.

**Request (optional):**
```json
{
  "deleteAllTokens": true
}
```

**Scope of `deleteAllTokens`:** When `true`, revokes all refresh tokens for the user **across all organizations**. This is a user-level security action (e.g., "sign out everywhere"). PATs are not affected — they must be revoked individually.

When `deleteAllTokens` is `false` or omitted, only the current session's refresh token is revoked.

### POST `/api/v1/auth/switch-org`

Switches the user's active organization. Returns a new token pair for the target org.

**Request:**
```json
{
  "org": "acme"
}
```

**Response:**
```json
{
  "accessToken": "eyJ...",
  "refreshToken": "...",
  "expiresIn": 3600,
  "tokenType": "Bearer",
  "user": {
    "uid": "...",
    "email": "user@example.com",
    "role": "member"
  }
}
```

**Behavior:**
- Verifies the user is a member of the target org
- Mints a new access token + refresh token scoped to the target org
- Returns `403 FORBIDDEN` if the user is not a member of the target org
- The previous org's tokens remain valid until they expire naturally

### GET `/api/v1/auth/me`

No change to response. The JWT already contains the org claim, so no URL parameter is needed.

### GET `/api/v1/auth/tokens`

Lists all tokens for the authenticated user across all organizations. Useful for a "security settings" page where users can see and manage all their active sessions and PATs.

**Query parameters:**
- `type` (optional): Filter by token type (`personal_access_token`, `refresh`)

**Response:**
```json
{
  "data": [
    {
      "uid": "550e8400-...",
      "name": "CI/CD Pipeline",
      "type": "personal_access_token",
      "orgSlug": "default",
      "createdAt": "2026-02-14T10:30:00Z",
      "lastUsedAt": "2026-02-15T14:22:00Z",
      "expiresAt": "2026-12-31T23:59:59Z"
    },
    {
      "uid": "660f9500-...",
      "name": "",
      "type": "refresh",
      "orgSlug": "default",
      "createdAt": "2026-02-14T08:00:00Z",
      "lastUsedAt": "2026-02-14T09:00:00Z",
      "expiresAt": "2026-02-21T08:00:00Z"
    }
  ]
}
```

### DELETE `/api/v1/auth/tokens/{tokenUid}`

Revokes any token (PAT or refresh) owned by the authenticated user.

**Authorization rules:**
- The server must verify the token belongs to the authenticated user — not just that it exists
- No org check needed — a user can revoke their own token regardless of org context
- Returns 404 if the token doesn't exist **or** doesn't belong to the user (don't leak existence)

```go
token, err := db.GetToken(ctx, tokenUID)
if err != nil || token.UserUID != authenticatedUser.UID {
    return NotFound("TOKEN_NOT_FOUND", "Token not found")
}
```

### GET `/api/v1/orgs/{org}/tokens`

List PATs for the given organization. Supports `?type=` filter.

### POST `/api/v1/orgs/{org}/tokens`

Create a new PAT scoped to the given organization. Unchanged request/response format.

## OAuth Redirect Clarification

OAuth callbacks redirect with tokens as query parameters:
```
{redirect_uri}?access_token=...&refresh_token=...&org=...
```

The `org` parameter in the redirect is for the frontend's convenience only — the JWT is the source of truth for organization context.

## CLI Client Update

The CLI client (`solidping client auth login`) currently constructs URLs with the org path. It must be updated to:
- Pass `org` in the request body instead of the URL path
- Update any token management commands to use the new endpoint paths

## Migration Strategy

Old endpoints are removed immediately. All frontend, CLI, and documentation are updated in the same change. There is no deprecation period — this is an internal API with no external consumers.

## Files to Modify

### Backend
- `back/internal/app/server.go` — Route registration: move auth routes to root, add `/orgs/{org}/tokens`, add `switch-org`
- `back/internal/handlers/auth/handler.go` — Login reads org from body (optional); add switch-org handler; unify error responses for anti-enumeration
- `back/internal/handlers/auth/handler_tokens.go` — Add cross-org listing at root; revoke verifies user ownership; create/list stay under org
- `back/internal/middleware/auth.go` — No changes (middleware reads from JWT, not URL)

### Frontend
- `apps/dash0/src/contexts/AuthContext.tsx` — Update API paths, add org switching support, show org switcher for multi-org users
- `apps/dash0/src/api/client.ts` — Update auth endpoint paths
- `apps/dash0/src/routes/orgs/$org/login.tsx` — Update login API call
- `apps/dash0/src/api/hooks.ts` — Update token API hooks to new paths

### CLI
- `back/cmd/client/` — Update auth and token commands to use new paths

### Documentation
- `CLAUDE.md` — Update API endpoint list

## Security Considerations

1. **Token ownership enforcement**: The revoke endpoint must verify `token.UserUID == authenticatedUser.UID`. Without this, a user could revoke another user's tokens by guessing UIDs.
2. **Anti-enumeration on login**: All login failures (bad org, bad user, bad password) return the same `INVALID_CREDENTIALS` error to prevent probing for valid org slugs.
3. **No change to JWT validation**: The middleware already validates tokens independently of URL org.
4. **PAT org scope preserved**: PATs remain scoped to a single org, ensuring least-privilege access.
5. **Cross-org token listing**: The `GET /api/v1/auth/tokens` endpoint only returns tokens owned by the authenticated user — never other users' tokens.
6. **Default org selection**: When `org` is omitted, the server selects the default org automatically. The list of orgs the user belongs to is never exposed in the login response — only via `/auth/me` after authentication.
