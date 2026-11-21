# Error Responses

All errors return JSON with:
```json
{
  "title": "Human-readable description",
  "code": "MACHINE_READABLE_CODE",
  "detail": "Detailed explanation"
}
```

Standard error codes:
- `INTERNAL_ERROR` - Unexpected server error
- `VALIDATION_ERROR` - Input validation failed
- `NOT_FOUND` - Resource not found
- `UNAUTHORIZED` - Authentication required
- `FORBIDDEN` - Permission denied
- `CONFLICT` - Resource conflict (duplicate, etc.)
- `ORGANIZATION_NOT_FOUND` - Organization does not exist
- `USER_NOT_FOUND` - User does not exist
- `CHECK_NOT_FOUND` - Check does not exist
- `LOCAL_LOGIN_DISABLED` (403) - Local password login is switched off
  (`auth.local_login.enabled` = false, env `SP_LOCAL_LOGIN_ENABLED`). Answered
  by `POST /auth/login` (password and LDAP), `/register`,
  `/confirm-registration`, `/request-password-reset`, `/reset-password`,
  `/change-password`, and `/accept-invite` when it would create or use a
  password. Sign in with SSO instead. See [auth.md](auth.md#local-login-switch)

Domain-specific codes are documented alongside the endpoints that emit them —
see [checks.md](checks.md), [discovery.md](discovery.md),
[on-call.md](on-call.md), and [integrations.md](integrations.md). The full code
list lives in `server/internal/handlers/base/`.
