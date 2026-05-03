# Password Reset — Hardening Pass

## Context

The first cut of password reset shipped in `specs/done/2026/01/2026-01-01-password-reset.md`.
Backend, frontend, email template, and i18n all exist. The flow works end-to-end.

The current implementation has security and UX gaps that fall short of what we'd expect from
a self-hosted product handling credentials. This spec is a focused hardening pass — not a
rewrite.

## Gaps in the current implementation

1. **Reset token is stored in plaintext.** Key `password_reset:{email}`, value `{"token": "..."}`.
   Anyone with read access to `state_entries` (DB snapshot, leaked backup, compromised
   read-replica) can use any unexpired token to take over the account. Even our own admins
   shouldn't be able to do that.

2. **Email is leaked in the state key.** A scan of `state_entries` reveals which users are
   currently resetting. The value also redundantly stores the email.

3. **Lookup is O(n).** `ResetPassword` calls `ListStateEntries(prefix)` and linear-scans for
   the matching token. Fine today, ugly at scale, easy to fix.

4. **No session invalidation on successful reset.** Existing refresh tokens for the user
   stay valid. If the reset is happening because the account was compromised, the attacker
   keeps their session. PATs should stay (they're separately managed credentials).

5. **No confirmation email after the password actually changes.** Without it, account
   takeover is invisible to the legitimate user.

6. **No rate limiting on `POST /api/v1/auth/request-password-reset`.** An attacker can:
   - email-bomb a victim's inbox,
   - burn email-sending budget,
   - bloat `state_entries`.

7. **No tests.** The `_test.go` files for `auth` don't cover either reset endpoint. Every
   fix below needs a regression guard.

8. **Frontend swallows real errors as success** (`web/dash0/src/routes/forgot-password.tsx`
   line 26: `catch { setSubmitted(true) }`). Anti-enumeration is the *server's* job; the
   client should surface network failures so users know to retry.

## Out of scope

- Password complexity beyond the existing 8-char minimum (HIBP, zxcvbn, etc.).
- 2FA-aware reset (the current behavior is already correct: reset only changes
  `password_hash`, 2FA still gates login).
- Multi-frontend reset URLs (status0, etc.). dash0 stays the only auth surface.
- Cooldown UI on the frontend ("you just requested a reset, try again in 60s") — server-side
  rate limiting is enough.

---

## 1. Hash the reset token at rest

**File**: `server/internal/handlers/auth/service.go`

Change the storage shape:

- **Key**: `password_reset:{sha256_hex(token)}` (was `password_reset:{email}`)
- **Value**: `{"userUid": "..."}` (was `{"token": "...", "email": "..."}`)
- **TTL**: unchanged (1 hour)

Token generation stays the same (32 random bytes, hex-encoded — 256 bits of entropy).
Because the input is already high-entropy, plain SHA-256 is sufficient — no salt or
key-stretching needed.

### `RequestPasswordReset`

1. Look up user by email. If not found or `PasswordHash == nil || == ""` → return success
   (anti-enum), no DB write, no email.
2. Generate token (32 random bytes → hex).
3. Compute `tokenHash = sha256_hex(token)`.
4. `SetStateEntry(ctx, nil, "password_reset:"+tokenHash, &models.JSONMap{"userUid": user.UID}, &ttl)`.
5. Send the email with the **plaintext** token in the URL. The plaintext is never persisted
   server-side.

### `ResetPassword`

1. `tokenHash := sha256_hex(req.Token)`.
2. `entry, err := s.db.GetStateEntry(ctx, nil, "password_reset:"+tokenHash)`.
3. If not found → `ErrPasswordResetExpired`.
4. Pull `userUid` from value, look up user by UID. If not found → `ErrPasswordResetExpired`.
5. Validate password (existing min-length check).
6. Hash password, `UpdateUser(...PasswordHash...)`.
7. `DeleteStateEntry(ctx, nil, entry.Key)`.
8. **Then** do steps 2-4 below, then return success.

This change makes step 1 of the lookup O(1), removes plaintext tokens and emails from the
state table, and turns leaked DB snapshots into useless data.

---

## 2. Revoke all refresh tokens on successful reset

**File**: `server/internal/handlers/auth/service.go`, in `ResetPassword` after the
password update succeeds.

```go
refreshTokens, err := s.db.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
if err != nil {
    slog.ErrorContext(ctx, "Failed to list refresh tokens for revocation", "error", err, "userUID", user.UID)
} else {
    for _, t := range refreshTokens {
        if delErr := s.db.DeleteUserToken(ctx, t.UID); delErr != nil {
            slog.ErrorContext(ctx, "Failed to delete refresh token", "error", delErr, "tokenUID", t.UID)
        }
    }
}
```

- Refresh tokens (`TokenTypeRefresh`) are wiped; PATs (`TokenTypePAT`) are left alone — PATs
  are explicit credentials the user manages from the tokens UI.
- Failures here are logged but do not fail the reset. The password is already rotated; the
  worst case is a short window where a stale refresh token still works, which is no worse
  than today.
- Stateless access tokens (JWTs) cannot be revoked synchronously. Their short TTL is the
  bound. If we ever need synchronous revocation we'll need a token-version field on `users`
  and a check in the JWT middleware — explicitly out of scope here.

---

## 3. Confirmation email after a successful reset

**New file**: `server/internal/email/templates/password-changed.html`

Same shape as `password-reset.html`. Subject: `Your SolidPing password was changed`.
Body: timestamp, "If this wasn't you, contact your administrator immediately." No
secrets, no reset links.

**Wire-up**: in `ResetPassword`, after the state entry is deleted:

```go
s.enqueueEmail(ctx, "", user.Email, "", "password-changed.html", map[string]any{
    "ChangedAt": time.Now().UTC().Format(time.RFC1123),
})
```

Email send failures are non-fatal (log + continue) — same convention as the request email.

---

## 4. Rate limit `request-password-reset`

Two limits, both server-side:

- **Per email**: at most 3 active reset entries at any time. On the 4th request within the
  TTL window, drop the new request silently (still return the anti-enum success message,
  no email sent).
- **Per IP**: at most 5 requests per minute. Beyond that, return 429
  (`base.ErrorCodeValidationError` is wrong here — add `base.ErrorCodeRateLimited` if it
  doesn't exist) with a generic message. Use the existing rate-limit middleware if one
  exists; otherwise implement a small `state_entries`-backed counter keyed by
  `pwd_reset_rl:{ip}`, TTL 1 minute.

The per-email cap requires being able to count active entries for a user. With the new
hashed-token scheme we no longer have `email` in the key, so add an index/list path:
either store a parallel counter at `password_reset_count:{userUid}` (TTL = TTL of the
reset itself) or look it up from the `userUid` value. Counter is simpler — ship the
counter.

If the rate-limit middleware approach turns out to be heavy for the per-IP case, putting
it behind a reverse-proxy rule is acceptable as an interim — but the per-email cap must be
in-app.

---

## 5. Frontend: stop faking success on errors

**File**: `web/dash0/src/routes/forgot-password.tsx`

Replace:

```tsx
} catch {
  setSubmitted(true);
}
```

with proper error surfacing — show the i18n-keyed error inline, leave the form intact so
the user can retry. Anti-enumeration is the server's responsibility (the server already
returns the same 200 for both valid and invalid emails); the client must not paper over
real failures.

Add a generic "Couldn't send the email, please try again" string to `auth.json` for en/de/es/fr.

---

## 6. Tests

**New files** (or extend existing):
- `server/internal/handlers/auth/service_test.go` — table-driven service tests
- `server/internal/handlers/auth/handler_test.go` — handler-level tests

Cover, at minimum:

| # | Scenario | Expected |
|---|---|---|
| 1 | Request reset, valid email, has password | state entry created (key prefix matches), email enqueued |
| 2 | Request reset, valid email, OAuth-only (no password) | success response, no state entry, no email |
| 3 | Request reset, unknown email | success response, no state entry, no email |
| 4 | Request reset, 4th request in same TTL window | success response, no new entry beyond cap, no extra email |
| 5 | Request reset, 6th request from same IP within 1 min | 429 |
| 6 | Reset with valid token | password updated, state entry deleted, refresh tokens for user deleted, PATs preserved, password-changed email enqueued |
| 7 | Reset with already-used token | 410 `PASSWORD_RESET_EXPIRED` |
| 8 | Reset with malformed token | 410 |
| 9 | Reset with expired token (TTL elapsed) | 410 |
| 10 | Reset with password < 8 chars | 400 `VALIDATION_ERROR`, password unchanged, state entry preserved |
| 11 | Token in DB is hashed, not plaintext (regression guard) | manual assert: state entry value contains no plaintext token |

Use `testify/require`, `t.Parallel()`, table-driven structure — per `server/CLAUDE.md`.

---

## 7. Routes / errors / config — no changes needed

Existing routes, error codes (`PASSWORD_RESET_EXPIRED`), and the `password-reset.html`
template stay as-is. The only new public knob is the rate-limit code (point 4).

---

## Implementation order

1. **Storage rework** (item 1) — biggest blast radius, do first, behind tests from item 6.
2. **Refresh-token revocation** (item 2).
3. **Confirmation email** (item 3) + new template.
4. **Rate limiting** (item 4) — start with per-email cap (in-app), then per-IP.
5. **Frontend fix** (item 5).
6. **Tests** (item 6) grow alongside each change; finalize once everything is in.

## Verification

- `make test` passes the new auth tests.
- `make lint` clean.
- Manual end-to-end via `make dev-test`:
  1. Request reset for `test@test.com`.
  2. Inspect `state_entries` — value must contain `userUid` only, key must be a
     64-char hex hash, no email anywhere.
  3. Click reset link, set new password.
  4. Confirm a "password changed" email arrived in the test inbox.
  5. Try to use the access token from step-1's session against any
     `/api/v1/orgs/test/...` endpoint after the refresh interval — should be rejected
     once the access token expires (refresh path is dead).
  6. Repeat the request endpoint 6× from the same client → 6th call returns 429.
  7. Repeat 4× for the same email within an hour → 4th request silently drops.

---

## Files touched

### Backend (existing)
- `server/internal/handlers/auth/service.go` — `RequestPasswordReset`, `ResetPassword`, constants
- `server/internal/handlers/auth/handler.go` — wire 429 if rate-limit middleware isn't middleware-shaped
- `server/internal/handlers/base/base.go` — possibly `ErrorCodeRateLimited`
- `server/internal/app/server.go` — wire rate-limit middleware on the request endpoint if applicable

### Backend (new)
- `server/internal/email/templates/password-changed.html`
- `server/internal/handlers/auth/service_test.go` (or extend)
- `server/internal/handlers/auth/handler_test.go` (or extend)

### Frontend (existing)
- `web/dash0/src/routes/forgot-password.tsx` — stop swallowing errors
- `web/dash0/src/locales/{en,de,es,fr}/auth.json` — generic send-failure string
