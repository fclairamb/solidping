# User Registration & Invitation System

## Overview

Add self-service user registration, organization invitations, and organization creation to SolidPing. Currently users can only be created via admin seeding, OAuth, or by being added by an org admin (who must reference an existing user). This spec introduces:

1. **Self-registration** gated by a global email regex pattern
2. **Per-org email patterns** for automatic organization membership
3. **Invitation system** using `state_entries` with single-use tokens (email-targeted or open link)
4. **Organization creation** by authenticated users
5. **"No org" experience** for users without memberships
6. **OAuth auto-join** enhancement based on org email patterns

## Motivation

1. Self-hosted deployments need to allow users to register themselves (restricted to company domain via regex)
2. Admins need to invite external collaborators without creating accounts manually
3. New users should have a smooth onboarding path (register → join/create org → dashboard)
4. OAuth users should automatically join all organizations whose email pattern matches

## Design Decisions

- **Registration gating**: A global config parameter `SP_AUTH_REGISTRATION_EMAIL_PATTERN` (regex). When set, registration is enabled. When empty, registration is disabled. Example: `.*` allows all, `^.*@company\.com$` restricts to company.com
- **Invitations respect global pattern**: Invitation-based account creation also validates against the global email pattern, so self-hosted deployments can restrict all account creation to their domain
- **Invitation storage**: Reuse `state_entries` table with key `invite:{token}`, org-scoped, with TTL. All invitations are single-use (deleted on acceptance)
- **Invitation types**: Two modes — (1) email-targeted: only the specified email can accept, (2) open link: any user can accept, shared as a URL. Both are single-use; admins create one invitation per person for open links
- **Org email patterns**: Stored as org-scoped parameters (key: `registration.email_pattern`) in the existing `parameters` table
- **No-org users**: Users who register but match no org get an account with no memberships. They see a landing page to create an org or browse existing orgs
- **No DB migration**: All storage uses existing tables (`parameters`, `state_entries`, `users`, `organization_members`)

---

## 1. Configuration

**File**: `back/internal/config/config.go`

Add to `AuthConfig`:

```go
type AuthConfig struct {
    JWTSecret                  string        `koanf:"jwt_secret"`
    AccessTokenExpiry           time.Duration `koanf:"access_token_expiry"`
    RefreshTokenExpiry          time.Duration `koanf:"refresh_token_expiry"`
    RegistrationEmailPattern   string        `koanf:"registration_email_pattern"` // NEW
}
```

Env var: `SP_AUTH_REGISTRATION_EMAIL_PATTERN` (mapped via koanf as `auth.registration.email.pattern`)

**File**: `back/internal/systemconfig/systemconfig.go`

Add parameter definition:

```go
const KeyRegistrationEmailPattern ParameterKey = "auth.registration_email_pattern"

// In getKnownParameters():
{
    Key:    KeyRegistrationEmailPattern,
    EnvVar: "SP_AUTH_REGISTRATION_EMAIL_PATTERN",
    Secret: false,
    ApplyFunc: func(cfg *config.Config, value any) {
        if v, ok := value.(string); ok {
            cfg.Auth.RegistrationEmailPattern = v
        }
    },
},
```

---

## 2. Providers Endpoint Update

**File**: `back/internal/handlers/auth/providers_available.go`

Add `RegistrationEnabled` to the response so the frontend knows whether to show registration UI:

```go
type ProvidersResponse struct {
    Data                []ProviderInfo `json:"data"`
    RegistrationEnabled bool           `json:"registrationEnabled"`
}
```

Set `RegistrationEnabled: cfg.Auth.RegistrationEmailPattern != ""` in `ListProviders()`.

---

## 3. Registration Endpoint (Two-Step with Email Confirmation)

Registration is a two-step process: the user submits their details, receives a confirmation email, and the account is only created when they click the confirmation link.

**File**: `back/internal/handlers/auth/handler.go` — Add `Register()` and `ConfirmRegistration()` handlers
**File**: `back/internal/handlers/auth/service.go` — Add `Register()` and `ConfirmRegistration()` methods

### Step 1: Initiate Registration

`POST /api/v1/auth/register` (public)

Request:
```json
{
    "email": "user@company.com",
    "password": "securepassword",
    "name": "John Doe"              // optional
}
```

Response:
```json
{
    "message": "Confirmation email sent. Please check your inbox."
}
```

#### Flow

1. Check `RegistrationEmailPattern` is set → else 403 `REGISTRATION_DISABLED`
2. Validate email matches compiled regex → else 400 `EMAIL_NOT_ALLOWED`
3. Validate password (min 8 chars) → else 400 `VALIDATION_ERROR`
4. If name provided, use it; otherwise default to empty string
5. Check email not already taken → else 409 `CONFLICT` (with message "An account with this email already exists")
6. Check no pending registration for this email (existing `email_registration:{email}` state entry) → if exists, delete and recreate (allows re-sending)
7. Hash password with Argon2id (reuse `passwords.Hash()`)
8. Generate 32-byte random hex confirmation token
9. Store in `state_entries` with key `email_registration:{email}`, value `{"passwordHash": "...", "name": "...", "token": "{token}"}`, `expires_at` = now + 3 days, no org scope
10. Send confirmation email with link: `{baseURL}/dash0/confirm-registration/{token}`
11. Return success message

### Step 2: Confirm Registration

`POST /api/v1/auth/confirm-registration` (public)

Request:
```json
{
    "token": "abc123..."
}
```

Response (same as login): `LoginResponse` with `accessToken`, `refreshToken`, `user`, `organization` (null if no org match)

#### Flow

1. Search `state_entries` for entry where value contains matching token and key starts with `email_registration:` → else 404 `NOT_FOUND`
2. Check not expired → else 410 `REGISTRATION_EXPIRED`
3. Extract email from key, password hash and name from value
4. Check email not already taken (race condition guard) → else 409 `CONFLICT`
5. Create `User` record with stored password hash and name
6. Delete state entry
7. Call `autoJoinMatchingOrgs()` (see section 5)
8. Generate access + refresh tokens
9. Return `LoginResponse`

### Error Codes

**File**: `back/internal/handlers/base/handler.go`

```go
const (
    ErrorCodeRegistrationDisabled = "REGISTRATION_DISABLED"
    ErrorCodeEmailNotAllowed      = "EMAIL_NOT_ALLOWED"
    ErrorCodeRegistrationExpired  = "REGISTRATION_EXPIRED"
)
```

### Routes

**File**: `back/internal/app/server.go`

```go
rootAuth.POST("/register", authHandler.Register)
rootAuth.POST("/confirm-registration", authHandler.ConfirmRegistration)
```

### Email Template

**File**: `back/internal/email/` — Add registration confirmation template

Subject: "Confirm your SolidPing account"
Body: Link to `{baseURL}/dash0/confirm-registration/{token}`

---

## 4. Organization Creation

**New package**: `back/internal/handlers/orgs/`

### service.go

```go
type Service struct {
    db db.Service
}

type CreateOrgRequest struct {
    Name string `json:"name"`
    Slug string `json:"slug"`
}

type OrgResponse struct {
    UID  string `json:"uid"`
    Slug string `json:"slug"`
    Name string `json:"name"`
}

func (s *Service) CreateOrg(ctx context.Context, userUID string, req CreateOrgRequest) (*OrgResponse, error)
```

Flow:
1. Validate slug (3-20 chars, `^[a-z0-9][a-z0-9-]{1,18}[a-z0-9]$`)
2. Check slug not taken → else 409 `CONFLICT`
3. Create `Organization`
4. Create `OrganizationMember` with role `admin` and `JoinedAt` set
5. Return org info

### handler.go

`POST /api/v1/orgs` (authenticated)

### Route

**File**: `back/internal/app/server.go`

```go
orgsService := orgs.NewService(s.dbService)
orgsHandler := orgs.NewHandler(orgsService, s.config)
orgsGroup := api.NewGroup("/orgs").Use(authMiddleware.RequireAuth)
orgsGroup.POST("", orgsHandler.CreateOrg)
```

---

## 5. Per-Org Email Pattern & Auto-Join

### Storage

Org-scoped parameter in existing `parameters` table:
- Key: `registration.email_pattern`
- Organization UID: set to the org
- Value: `{"value": "^.*@company\\.com$"}`

### DB Methods

**File**: `back/internal/db/service.go`

```go
// Add to Service interface:
ListOrgParametersByKey(ctx context.Context, key string) ([]*models.Parameter, error)
GetOrgParameter(ctx context.Context, orgUID string, key string) (*models.Parameter, error)
SetOrgParameter(ctx context.Context, orgUID string, key string, value any, secret bool) error
DeleteOrgParameter(ctx context.Context, orgUID string, key string) error
```

**Files**: `back/internal/db/postgres/postgres.go` + `back/internal/db/sqlite/sqlite.go`

`ListOrgParametersByKey`: `SELECT * FROM parameters WHERE key = ? AND organization_uid IS NOT NULL AND deleted_at IS NULL`

### Auto-Join Helper

**File**: `back/internal/handlers/auth/service.go`

```go
func (s *Service) autoJoinMatchingOrgs(ctx context.Context, userUID, email string) error {
    params, err := s.db.ListOrgParametersByKey(ctx, "registration.email_pattern")
    // For each param, compile regex, test email
    // If match: create OrganizationMember with role "user", JoinedAt = now
    // Skip if already a member
}
```

Called from:
- `Register()` (section 3)
- OAuth `ensureMembership()` methods (section 8)

### Settings API

**New endpoints** (admin only):

- `GET /api/v1/orgs/{org}/settings` → `{registrationEmailPattern: string | null}`
- `PATCH /api/v1/orgs/{org}/settings` → update `{registrationEmailPattern: string | null}`

These can live in a new `back/internal/handlers/orgsettings/` package or be added to the existing members/org handlers.

#### PATCH Validation

The `PATCH` endpoint must validate that `registrationEmailPattern`, when non-null, is a valid Go regex by calling `regexp.Compile(pattern)`. If the pattern is invalid, return 400 `VALIDATION_ERROR` with detail "Invalid regex pattern: {compile error}". This prevents broken patterns from silently breaking the auto-join feature at registration/login time.

**File**: `back/internal/app/server.go`

```go
orgSettings := api.NewGroup("/orgs/:org/settings").Use(authMiddleware.RequireAuth)
orgSettings.GET("", settingsHandler.Get)
orgSettings.PATCH("", settingsHandler.Update)
```

---

## 6. Invitation System

### Storage

Reuse `state_entries` table:
- Key: `invite:{32-byte-random-hex-token}`
- `organization_uid`: target org
- `value`: `{"role": "user", "email": "optional@email.com", "invitedByUid": "...", "invitedByEmail": "..."}`
- `expires_at`: configurable, allowed values: `1h`, `6h`, `12h`, `24h`, `48h`, `1w` (default: `24h`)

### New Package

`back/internal/handlers/invitations/`

### Endpoints

#### Create Invitation

`POST /api/v1/orgs/{org}/invitations` (admin only)

Request:
```json
{
    "email": "user@company.com",     // optional — if set, only this email can accept; if omitted, any user can accept via the link
    "role": "user",                   // default: "user"
    "expiresIn": "24h",              // allowed: "1h", "6h", "12h", "24h", "48h", "1w" (default: "24h")
    "sendEmail": false               // default: false
}
```

Response:
```json
{
    "uid": "state-entry-uid",
    "token": "abc123...",
    "inviteUrl": "https://solidping.example.com/dash0/invite/abc123...",
    "email": "user@company.com",
    "role": "user",
    "expiresAt": "2026-02-23T00:00:00Z"
}
```

Flow:
1. Validate role (admin/user/viewer)
2. If email provided AND global pattern is set → validate email matches pattern
3. Generate 32-byte random hex token
4. Store in `state_entries` with key `invite:{token}`, org-scoped
5. If `sendEmail` true and email config enabled → send invitation email with link
6. Return token + invite URL

#### List Invitations

`GET /api/v1/orgs/{org}/invitations` (admin only)

Response:
```json
{
    "data": [
        {
            "uid": "...",
            "email": "user@company.com",
            "role": "user",
            "expiresAt": "2026-02-23T00:00:00Z",
            "createdAt": "2026-02-16T00:00:00Z",
            "invitedBy": "admin@company.com"
        }
    ]
}
```

Implementation: `ListStateEntries(ctx, orgUID, "invite:")` then parse values.

#### Revoke Invitation

`DELETE /api/v1/orgs/{org}/invitations/{uid}` (admin only)

Deletes the state entry.

#### Get Invitation Info (Public)

`GET /api/v1/auth/invite/{token}` (public, no auth)

Response:
```json
{
    "orgName": "Acme Corp",
    "orgSlug": "acme",
    "role": "user",
    "email": "u***@company.com",   // masked if set, null if open invitation
    "expiresAt": "2026-02-23T00:00:00Z"
}
```

Looks up `state_entries` by key `invite:{token}`. Returns limited info (no sensitive data).

#### Accept Invitation

`POST /api/v1/auth/accept-invite` (public)

Request for new users:
```json
{
    "token": "abc123...",
    "email": "user@company.com",
    "password": "securepassword",
    "name": "John Doe"              // optional
}
```

Request for authenticated users (token in Authorization header):
```json
{
    "token": "abc123..."
}
```

Flow:
1. Look up state entry by key `invite:{token}` → else 404 `NOT_FOUND`
2. Check not expired → else 410 `INVITATION_EXPIRED`
3. If email-targeted → verify email matches
4. If new user (no auth header):
   a. Validate email against global pattern → else 400 `EMAIL_NOT_ALLOWED`
   b. Validate password
   c. Check email not already taken → else 409 `CONFLICT`
   d. Create user with hashed password
5. If authenticated user:
   a. Use user from JWT claims
6. Check not already a member → if already member, just return success
7. Create `OrganizationMember` with role from invitation
8. Delete state entry (one-time use)
9. Call `autoJoinMatchingOrgs()` for new users
10. Return `LoginResponse`

### Routes

**File**: `back/internal/app/server.go`

```go
// Invitation management (admin only)
orgInvitations := api.NewGroup("/orgs/:org/invitations").Use(authMiddleware.RequireAuth)
orgInvitations.POST("", invitationsHandler.Create)
orgInvitations.GET("", invitationsHandler.List)
orgInvitations.DELETE("/:uid", invitationsHandler.Revoke)

// Public invitation routes
rootAuth.GET("/invite/:token", authHandler.GetInviteInfo)
rootAuth.POST("/accept-invite", authHandler.AcceptInvite)
```

### Email Template

**File**: `back/internal/email/` — Add invitation template

Subject: "You've been invited to join {orgName} on SolidPing"
Body: Link to `{baseURL}/dash0/invite/{token}`

---

## 7. OAuth Auto-Join Enhancement

**Files**:
- `back/internal/handlers/auth/google_service.go`
- `back/internal/handlers/auth/github_service.go`
- `back/internal/handlers/auth/gitlab_service.go`
- `back/internal/handlers/auth/microsoft_service.go`

In each provider's `ensureMembership()` or post-callback logic, after the current org join:

```go
// After ensuring membership in the requested org...
// Also auto-join all orgs with matching email patterns
if err := s.authService.autoJoinMatchingOrgs(ctx, user.UID, user.Email); err != nil {
    slog.ErrorContext(ctx, "Failed to auto-join matching orgs", "error", err)
    // Non-fatal: continue even if auto-join fails
}
```

---

## 8. Frontend: Providers & Registration

### Update providers hook

**File**: `apps/dash0/src/api/hooks.ts`

Update `useProviders()` return type to include `registrationEnabled: boolean`.

### Login page update

**File**: `apps/dash0/src/routes/orgs/$org/login.tsx`

Below the login form, when `registrationEnabled` is true, add:

```tsx
<p className="text-center text-sm text-muted-foreground">
    Don't have an account?{" "}
    <Link to="/orgs/$org/register" params={{ org }}>
        Create one
    </Link>
</p>
```

### Registration page

**New file**: `apps/dash0/src/routes/orgs/$org/register.tsx`

- Form fields: Name (optional), Email, Password, Confirm Password
- Validates password match, min 8 chars
- POST to `/api/v1/auth/register`
- On success: show "Check your email" confirmation message (no auto-login yet)
- Link back to login page
- Style consistent with login page (Card layout, Activity icon)

### Registration confirmation page

**New file**: `apps/dash0/src/routes/confirm-registration/$token.tsx`

- Extracts token from URL
- POST to `/api/v1/auth/confirm-registration` with the token
- On success: auto-login (store tokens), redirect to org dashboard or `/no-org`
- On error (expired/invalid): show error message with link to register again

### API hook

**File**: `apps/dash0/src/api/hooks.ts`

```typescript
export function useRegister() {
    return useMutation({
        mutationFn: (data: { email: string; password: string; name: string }) =>
            apiFetch("/api/v1/auth/register", { method: "POST", body: JSON.stringify(data), skipAuth: true }),
    });
}
```

---

## 9. Frontend: No-Org Landing Page

**New file**: `apps/dash0/src/routes/no-org.tsx`

Shown when authenticated user has 0 organization memberships.

Layout:
- Welcome message: "Welcome to SolidPing, {name}"
- "You're not a member of any organization yet."
- **Create Organization** button → inline form (name, slug) or modal
- If invitation was shared, they would have been redirected through `/invite/{token}` flow instead

### Auth context update

**File**: `apps/dash0/src/contexts/AuthContext.tsx`

After login/register, if `organizations` array is empty, redirect to `/no-org` instead of org dashboard.

**File**: `apps/dash0/src/routes/orgs/$org.tsx` (org layout)

If user is authenticated but has no orgs, redirect to `/no-org`.

---

## 10. Frontend: Invitation Management

**New file**: `apps/dash0/src/routes/orgs/$org/invitations.tsx`

Admin-only page (guard with `isAdmin` check).

Features:
- List pending invitations: email, role, expiry, created date, invited by
- "Create Invitation" button → dialog/form:
  - Email (optional — "Leave empty for an open link invitation; each invitation is single-use")
  - Role selector (admin/user/viewer)
  - Expiry selector (1h, 6h, 12h, 24h, 48h, 1w)
  - Send email checkbox (only shown if email provided)
- After creation: show invite URL with copy-to-clipboard button
- Revoke button on each invitation

### Sidebar

**File**: `apps/dash0/src/components/layout/AppSidebar.tsx`

Add "Invitations" nav item (admin only), grouped near existing navigation.

### API hooks

**File**: `apps/dash0/src/api/hooks.ts`

```typescript
export function useInvitations(org: string) { ... }
export function useCreateInvitation(org: string) { ... }
export function useRevokeInvitation(org: string) { ... }
```

---

## 11. Frontend: Invitation Acceptance

**New file**: `apps/dash0/src/routes/invite/$token.tsx`

- Fetches `GET /api/v1/auth/invite/{token}` to show org info
- If user is authenticated: "Accept invitation to join {orgName} as {role}" button → POST `/api/v1/auth/accept-invite` with just the token
- If user is NOT authenticated:
  - If registration enabled: show registration form (name, email, password) that also accepts the invite
  - "Already have an account?" link → login page with `returnTo=/invite/{token}`
- On success: redirect to `/orgs/{orgSlug}`
- Handle expired/invalid tokens with appropriate error messages

---

## 12. Frontend: Organization Settings

**New file**: `apps/dash0/src/routes/orgs/$org/settings.tsx`

Admin-only page.

- "Registration Email Pattern" input field with current value
- Help text: "Users whose email matches this regex pattern will automatically join this organization when they register. Leave empty to disable auto-join."
- Examples: `^.*@company\.com$`, `^.*@(company\.com|partner\.com)$`
- Save button → PATCH `/api/v1/orgs/{org}/settings`

### Sidebar

Add "Settings" nav item (admin only) to sidebar.

---

## Test Mode API

When `SP_RUNMODE=test`, expose additional endpoints under `/api/v1/test/` to allow E2E tests to complete flows that normally require email delivery (e.g., registration confirmation, invitation acceptance).

**File**: `back/internal/handlers/testapi/handler.go`

### List Pending Tokens

`GET /api/v1/test/state-entries` (test mode only, no auth)

Query parameters:
- `prefix` — filter by key prefix (e.g., `email_registration:`, `invite:`)

Response:
```json
{
    "data": [
        {
            "uid": "...",
            "key": "email_registration:user@company.com",
            "value": {"passwordHash": "...", "name": "John Doe", "token": "abc123..."},
            "organizationUid": null,
            "expiresAt": "2026-02-19T00:00:00Z",
            "createdAt": "2026-02-16T00:00:00Z"
        }
    ]
}
```

This allows E2E tests to:
1. Call `POST /api/v1/auth/register` to initiate registration
2. Call `GET /api/v1/test/state-entries?prefix=email_registration:` to retrieve the confirmation token
3. Call `POST /api/v1/auth/confirm-registration` with the token to complete registration
4. Similarly for invitations: retrieve tokens via `?prefix=invite:` to test acceptance flows

### Route

**File**: `back/internal/app/server.go`

Register only when `cfg.RunMode == "test"`:

```go
if s.config.RunMode == "test" {
    api.GET("/test/state-entries", testHandler.ListStateEntries)
}
```

The handler needs access to `db.Service` to query state entries. Either pass it to the existing `testapi.Handler` or create a separate handler.

---

## Implementation Order

1. Config + registration endpoint (backend, testable via curl)
2. Organization creation endpoint (backend)
3. Per-org email pattern + auto-join logic (backend)
4. Invitation system: create, list, revoke, accept (backend)
5. Frontend: providers update + registration page + login link
6. Frontend: no-org landing + org creation
7. Frontend: invitation management + acceptance page
8. Frontend: org settings page
9. OAuth auto-join enhancement

---

## Key Files to Modify

### Backend (existing)
- `back/internal/config/config.go` — Add RegistrationEmailPattern to AuthConfig
- `back/internal/systemconfig/systemconfig.go` — Add registration parameter key
- `back/internal/handlers/auth/handler.go` — Add Register, ConfirmRegistration, AcceptInvite, GetInviteInfo handlers
- `back/internal/handlers/auth/service.go` — Add Register(), ConfirmRegistration(), AcceptInvite(), autoJoinMatchingOrgs()
- `back/internal/handlers/auth/providers_available.go` — Add registrationEnabled
- `back/internal/handlers/base/handler.go` — Add error codes
- `back/internal/db/service.go` — Add ListOrgParametersByKey, GetOrgParameter, SetOrgParameter
- `back/internal/db/postgres/postgres.go` — Implement new DB methods
- `back/internal/db/sqlite/sqlite.go` — Implement new DB methods
- `back/internal/handlers/testapi/handler.go` — Add ListStateEntries for test mode
- `back/internal/app/server.go` — Register new routes (including test-mode routes)
- `back/internal/handlers/auth/google_service.go` — Auto-join after OAuth
- `back/internal/handlers/auth/github_service.go` — Auto-join after OAuth
- `back/internal/handlers/auth/gitlab_service.go` — Auto-join after OAuth
- `back/internal/handlers/auth/microsoft_service.go` — Auto-join after OAuth

### Backend (new)
- `back/internal/handlers/invitations/handler.go` — Invitation HTTP handlers
- `back/internal/handlers/invitations/service.go` — Invitation business logic
- `back/internal/handlers/orgs/handler.go` — Org creation handler
- `back/internal/handlers/orgs/service.go` — Org creation logic
- `back/internal/handlers/orgsettings/handler.go` — Org settings handler
- `back/internal/handlers/orgsettings/service.go` — Org settings logic

### Frontend (existing)
- `apps/dash0/src/routes/orgs/$org/login.tsx` — Add register link
- `apps/dash0/src/api/hooks.ts` — Add new hooks
- `apps/dash0/src/contexts/AuthContext.tsx` — Handle no-org redirect
- `apps/dash0/src/components/layout/AppSidebar.tsx` — Add invitations + settings links

### Frontend (new)
- `apps/dash0/src/routes/orgs/$org/register.tsx` — Registration page
- `apps/dash0/src/routes/confirm-registration/$token.tsx` — Registration confirmation page
- `apps/dash0/src/routes/no-org.tsx` — No-org landing
- `apps/dash0/src/routes/invite/$token.tsx` — Invitation acceptance
- `apps/dash0/src/routes/orgs/$org/invitations.tsx` — Invitation management
- `apps/dash0/src/routes/orgs/$org/settings.tsx` — Org settings

---

## Verification

1. **Registration initiate**: `SP_AUTH_REGISTRATION_EMAIL_PATTERN=.*` → `POST /api/v1/auth/register` → verify state entry created + confirmation email sent
2. **Registration confirm**: `POST /api/v1/auth/confirm-registration` with token → verify user created + logged in
3. **Registration expired**: Wait for token to expire → confirm → expect 410
4. **Registration blocked**: Pattern `@company\.com$` → try registering `user@gmail.com` → expect 400
3. **Org creation**: Authenticated user → `POST /api/v1/orgs` → verify org created + user is admin
4. **Auto-join**: Set org email pattern → register matching user → verify auto-joined
5. **Invitation create**: Admin → `POST /api/v1/orgs/{org}/invitations` → verify state entry created
6. **Invitation accept (new user)**: `POST /api/v1/auth/accept-invite` with credentials → verify user + membership created
7. **Invitation accept (existing user)**: Authenticated user → `POST /api/v1/auth/accept-invite` → verify membership created
8. **Invitation email-targeted**: Create invitation with email → try accepting with different email → expect 403
9. **OAuth auto-join**: Set org pattern → OAuth login with matching email → verify joined multiple orgs
10. **Frontend E2E**: Register → see no-org page → create org → invite user → accept from second browser
11. **Run backend tests**: `make test`
12. **Run frontend tests**: `make test-dash`
13. **Run linters**: `make lint`
