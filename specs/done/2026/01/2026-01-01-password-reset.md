# Password Reset

## Overview

Add a password reset flow to SolidPing. Users who forget their password can request a reset link via email, then set a new password. The flow follows the same two-step pattern as registration confirmation: request → email → confirm.

## Motivation

Users with local (email/password) accounts have no way to recover access if they forget their password. This is a basic auth feature expected in any self-hosted application.

## Design Decisions

- **Storage**: Reuse `state_entries` table with key `password_reset:{email}`, same pattern as registration
- **TTL**: 1 hour (shorter than registration's 3 days — reset tokens should expire quickly)
- **Token size**: 32-byte random hex (same as registration)
- **Anti-enumeration**: Always return the same success message regardless of whether the email exists. This prevents attackers from discovering which emails are registered
- **Re-request**: If a pending reset exists for the same email, delete and recreate (allows re-sending)
- **No DB migration**: Uses existing `state_entries` table
- **Password-only users**: Only applies to users with a `password_hash`. OAuth-only users (no password_hash) should still get the same success response (anti-enumeration) but no email is sent

---

## 1. Backend: Service

**File**: `back/internal/handlers/auth/service.go`

### Constants

```go
const (
    passwordResetKeyPrefix = "password_reset:"
    passwordResetTTL       = 1 * time.Hour
)
```

### Errors

```go
var (
    ErrPasswordResetExpired = errors.New("password reset link has expired")
)
```

### Request/Response Types

```go
type RequestPasswordResetRequest struct {
    Email string `json:"email"`
}

type RequestPasswordResetResponse struct {
    Message string `json:"message"`
}

type ResetPasswordRequest struct {
    Token    string `json:"token"`
    Password string `json:"password"`
}

type ResetPasswordResponse struct {
    Message string `json:"message"`
}
```

### RequestPasswordReset

`func (s *Service) RequestPasswordReset(ctx context.Context, req RequestPasswordResetRequest) (*RequestPasswordResetResponse, error)`

Flow:
1. Look up user by email → if not found or no `password_hash`, return success (anti-enumeration) without sending email
2. Generate 32-byte random hex token
3. Store in `state_entries` with key `password_reset:{email}`, value `{"token": "{token}"}`, TTL = 1 hour, no org scope. If entry already exists it will be overwritten (SetStateEntry upserts)
4. Send reset email with link: `{baseURL}/dash0/reset-password/{token}`
5. Return `{"message": "If an account exists with that email, a reset link has been sent."}`

### ResetPassword

`func (s *Service) ResetPassword(ctx context.Context, req ResetPasswordRequest) (*ResetPasswordResponse, error)`

Flow:
1. Search `state_entries` with prefix `password_reset:` for entry where value contains matching token → else return `ErrPasswordResetExpired`
2. Extract email from key (strip prefix)
3. Look up user by email → if not found, return `ErrPasswordResetExpired` (defensive)
4. Validate new password (min 8 chars) → else return validation error
5. Hash new password with Argon2id (`passwords.Hash()`)
6. Update user's `password_hash` in database
7. Delete the state entry
8. Return `{"message": "Your password has been reset. You can now log in."}`

---

## 2. Backend: Handler

**File**: `back/internal/handlers/auth/handler.go`

### RequestPasswordReset handler

```go
func (h *Handler) RequestPasswordReset(writer http.ResponseWriter, req bunrouter.Request) error
```

- Decode `RequestPasswordResetRequest`
- Validate email is not empty
- Call `h.svc.RequestPasswordReset()`
- Return 200 with response

### ResetPassword handler

```go
func (h *Handler) ResetPassword(writer http.ResponseWriter, req bunrouter.Request) error
```

- Decode `ResetPasswordRequest`
- Validate token and password are not empty
- Call `h.svc.ResetPassword()`
- On error: use `handlePasswordResetError()`
- Return 200 with response

### Error handler

```go
func (h *Handler) handlePasswordResetError(writer http.ResponseWriter, err error) error {
    switch {
    case errors.Is(err, ErrPasswordResetExpired):
        return h.WriteErrorErr(writer, http.StatusGone, base.ErrorCodePasswordResetExpired,
            "Reset link has expired or is invalid", err)
    default:
        return h.WriteInternalError(writer, err)
    }
}
```

---

## 3. Error Codes

**File**: `back/internal/handlers/base/handler.go`

```go
const ErrorCodePasswordResetExpired = "PASSWORD_RESET_EXPIRED"
```

---

## 4. Routes

**File**: `back/internal/app/server.go`

Add to the public auth routes (next to register/confirm-registration):

```go
rootAuth.POST("/request-password-reset", authHandler.RequestPasswordReset)
rootAuth.POST("/reset-password", authHandler.ResetPassword)
```

---

## 5. Email Template

**New file**: `back/internal/email/templates/password-reset.html`

```html
{{template "base.html" .}}
{{define "content"}}
<div class="content">
    <h2>Reset Your Password</h2>
    <p>We received a request to reset your SolidPing password. Click the button below to set a new one.</p>
    <p><a href="{{.ResetURL}}" class="button button-success">Reset Password</a></p>
    <p style="color: #666; font-size: 14px;">This link will expire in 1 hour. If you didn't request this, you can safely ignore this email.</p>
</div>
{{end}}
```

Subject: `[SolidPing] Reset your password`

---

## 6. Frontend: Forgot Password Page

**New file**: `apps/dash0/src/routes/orgs/$org/forgot-password.tsx`

- Form with single email input
- On submit: POST to `/api/v1/auth/request-password-reset` (with `skipAuth: true`)
- On success: show "Check your email" message (same pattern as registration)
- Link back to login page
- Style consistent with login page (Card layout, Activity icon)

---

## 7. Frontend: Reset Password Page

**New file**: `apps/dash0/src/routes/orgs/$org/reset-password.$token.tsx`

- Extract token from URL params
- Form with password + confirm password fields
- Client-side validation: passwords match, min 8 chars
- On submit: POST to `/api/v1/auth/reset-password` with token and password (with `skipAuth: true`)
- On success: show success message with link to login
- On error (expired/invalid): show error message with link to request a new reset

---

## 8. Frontend: Login Page Update

**File**: `apps/dash0/src/routes/orgs/$org/login.tsx`

Add a "Forgot password?" link below the password field or below the sign-in button:

```tsx
<Link to="/orgs/$org/forgot-password" params={{ org }} className="text-sm text-muted-foreground hover:underline">
    {t("forgotPassword")}
</Link>
```

---

## 9. Frontend: API Hooks

**File**: `apps/dash0/src/api/hooks.ts`

```typescript
export function useRequestPasswordReset() {
    return useMutation({
        mutationFn: (data: { email: string }) =>
            apiFetch("/api/v1/auth/request-password-reset", {
                method: "POST",
                body: JSON.stringify(data),
                skipAuth: true,
            }),
    });
}

export function useResetPassword() {
    return useMutation({
        mutationFn: (data: { token: string; password: string }) =>
            apiFetch("/api/v1/auth/reset-password", {
                method: "POST",
                body: JSON.stringify(data),
                skipAuth: true,
            }),
    });
}
```

---

## 10. i18n

**File**: `apps/dash0/src/locales/en/auth.json` — Add:

```json
{
    "forgotPassword": "Forgot password?",
    "resetPassword": "Reset password",
    "resetYourPassword": "Reset your password",
    "sendResetLink": "Send reset link",
    "sendingResetLink": "Sending...",
    "resetLinkSent": "If an account exists with that email, a reset link has been sent.",
    "newPassword": "New password",
    "confirmNewPassword": "Confirm new password",
    "resettingPassword": "Resetting...",
    "passwordResetSuccess": "Your password has been reset.",
    "passwordResetExpired": "This reset link is invalid or has expired.",
    "requestNewReset": "Request a new reset link",
    "passwordsDoNotMatch": "Passwords do not match",
    "canNowLogin": "You can now log in with your new password."
}
```

**File**: `apps/dash0/src/locales/fr/auth.json` — Add French translations.

---

## Test Mode

The existing test API endpoint `GET /api/v1/test/state-entries?prefix=password_reset:` already supports this flow — no changes needed. E2E tests can:
1. Call `POST /api/v1/auth/request-password-reset`
2. Call `GET /api/v1/test/state-entries?prefix=password_reset:` to retrieve the token
3. Call `POST /api/v1/auth/reset-password` with the token

---

## Implementation Order

1. Backend: constants, errors, request/response types, service methods
2. Backend: handler methods, error handler, routes
3. Backend: error code in base handler
4. Backend: email template
5. Frontend: API hooks
6. Frontend: forgot-password page
7. Frontend: reset-password page
8. Frontend: login page "Forgot password?" link
9. Frontend: i18n keys (en + fr)

---

## Key Files to Modify

### Backend (existing)
- `back/internal/handlers/auth/service.go` — Service methods, types, errors, constants
- `back/internal/handlers/auth/handler.go` — HTTP handlers
- `back/internal/handlers/base/handler.go` — Error code constant
- `back/internal/app/server.go` — Route registration

### Backend (new)
- `back/internal/email/templates/password-reset.html` — Email template

### Frontend (existing)
- `apps/dash0/src/routes/orgs/$org/login.tsx` — "Forgot password?" link
- `apps/dash0/src/api/hooks.ts` — API hooks
- `apps/dash0/src/locales/en/auth.json` — English i18n
- `apps/dash0/src/locales/fr/auth.json` — French i18n

### Frontend (new)
- `apps/dash0/src/routes/orgs/$org/forgot-password.tsx` — Request reset page
- `apps/dash0/src/routes/orgs/$org/reset-password.$token.tsx` — Set new password page

---

## Verification

1. **Request reset (existing email)**: `POST /api/v1/auth/request-password-reset` → state entry created + email sent
2. **Request reset (unknown email)**: Same success response, no email sent (anti-enumeration)
3. **Request reset (OAuth-only user)**: Same success response, no email sent
4. **Reset with valid token**: `POST /api/v1/auth/reset-password` → password updated, state entry deleted
5. **Login with new password**: Verify the new password works
6. **Reset with expired token**: Expect 410 `PASSWORD_RESET_EXPIRED`
7. **Reset with invalid token**: Expect 410 `PASSWORD_RESET_EXPIRED`
8. **Password validation**: Reset with password < 8 chars → expect 400 `VALIDATION_ERROR`
9. **Re-request**: Request reset twice for same email → old entry replaced, new token works
10. **Frontend E2E**: Login page → forgot password → enter email → (test mode: get token) → set new password → login with new password
11. **Run backend tests**: `make test`
12. **Run frontend tests**: `make test-dash`
13. **Run linters**: `make lint`

---

## Implementation Plan

### Step 1: Backend - Error code, constants, service methods
- Add `ErrorCodePasswordResetExpired` to `base/base.go`
- Add `ErrPasswordResetExpired`, constants, request/response types to `auth/service.go`
- Implement `RequestPasswordReset()` and `ResetPassword()` service methods

### Step 2: Backend - Handler and routes
- Add `RequestPasswordReset` and `ResetPassword` handlers to `auth/handler.go`
- Add error handler for password reset
- Register routes in `server.go`

### Step 3: Backend - Email template
- Create `password-reset.html` template

### Step 4: Frontend - API hooks, pages, i18n
- Add `useRequestPasswordReset` and `useResetPassword` hooks
- Create forgot-password page
- Create reset-password page
- Add "Forgot password?" link to login page
- Add i18n keys (en + fr)
