---
model: sonnet
effort: medium
---

# Register with a too-short password returns 500 INTERNAL_ERROR instead of 400 VALIDATION_ERROR

## Problem

`POST /api/v1/auth/register` with a password shorter than 8 characters answers:

```json
{"title":"Internal server error","code":"INTERNAL_ERROR","detail":"invalid credentials: password must be at least 8 characters"}
```

HTTP 500 for a user typo — and because the fallthrough is `WriteInternalError`,
every such typo is also reported to Sentry as if it were a server fault.

Cause: `Service.Register` returns the password-length failure wrapped in
`ErrInvalidCredentials` (`server/internal/handlers/auth/service.go:2303`), but
`handleRegistrationError` (`server/internal/handlers/auth/handler.go:1104`) has no
`errors.Is(err, ErrInvalidCredentials)` case — it only maps
`ErrRegistrationDisabled`, `ErrEmailNotAllowed`, `ErrEmailAlreadyTaken` and
`ErrRegistrationExpired`, so anything else falls into the
`WriteInternalError` default (handler.go:1118-1119).

The repo already has the correct pattern for exactly this error in two sibling
helpers — both map `ErrInvalidCredentials` → `http.StatusBadRequest` /
`base.ErrorCodeValidationError` with `err.Error()` as the message so the
"password must be at least 8 characters" detail reaches the client:

- `handleChangePasswordError` (handler.go:688-690)
- `handlePasswordResetError` (handler.go:701-703)

**The same gap exists in `handleInvitationError`** (handler.go:1124), which also
lacks an `ErrInvalidCredentials` case and funnels into `WriteInternalError`
(handler.go:1144-1145). Two service paths reach it with that error today:

- `AcceptInvite` wraps the same password-length failure in
  `ErrInvalidCredentials` (service.go:3456) — so accepting an invite with a
  7-character password also 500s and pages Sentry.
- `CreateInvitation` wraps `"invalid role"` in `ErrInvalidCredentials`
  (service.go:3243) — an invalid role in the request body 500s too.

## Proposal

1. In `handleRegistrationError` (handler.go:1104), add a case mirroring
   handler.go:688-690:

   ```go
   case errors.Is(err, ErrInvalidCredentials):
       return h.WriteErrorErr(writer, request, http.StatusBadRequest, base.ErrorCodeValidationError,
           err.Error(), err)
   ```

   `err.Error()` surfaces the password-length detail
   ("invalid credentials: password must be at least 8 characters"), matching
   what the sibling helpers already expose. `WriteErrorErr` with a 4xx never
   reports to Sentry, which fixes the noise as a side effect.

2. Add the identical case to `handleInvitationError` (handler.go:1124), covering
   both `AcceptInvite` (short password) and `CreateInvitation` (invalid role).

3. Tests (in `internal/handlers/auth/handler_test.go`, following the existing
   handler-test conventions — `testify/require` via `r := require.New(t)`,
   `t.Parallel()`):
   - `POST /auth/register` with a too-short password → 400,
     `code == "VALIDATION_ERROR"`, detail/title mentions the 8-character
     minimum. This pins the regression.
   - `AcceptInvite` with a too-short password → 400 `VALIDATION_ERROR`
     (not 500).
   - If a cheap direct unit test of the two `handle*Error` helpers with a
     wrapped `ErrInvalidCredentials` is more natural than full route tests,
     that is acceptable, but at least the `/auth/register` case should go
     through the route so the end-to-end 400 is pinned.

4. Sweep the remaining `handle*Error` helpers in the auth handler
   (`handle2FAError`, etc.) for other service error sentinels that can reach
   them but fall through to `WriteInternalError`; fix any found the same way.
   `ConfirmRegistration` (handler.go:580) shares `handleRegistrationError`, so
   it is covered by step 1 — no separate change expected, just verify no other
   sentinel it can return is unmapped.

Scope note: do not restructure error handling — this is strictly adding the
missing `errors.Is` cases and pinning them with tests.
