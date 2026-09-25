---
model: sonnet
effort: low
---

# POST /auth/logout only clears the cookie — the session row survives

## Problem

The default logout path (no `deleteAllTokens`, no `signOutOthers`) only
clears the cookie (`handlers/auth/handler.go:212-215`). The refresh-token
row in `user_tokens` is untouched, so anyone who captured the refresh token
— it is also returned in the login/logout JSON body — keeps a live
7-day-sliding session after the user "logged out".

A `Logout(ctx, refreshToken)` service that deletes the session row already
exists (`handlers/auth/service.go:1086-1111`) but is not called on this
path; only `LogoutUser` (delete all) and `LogoutOtherSessions` are.

## Proposal

1. Default logout path: when `claims.RefreshUID` is set, delete that session
   row (reuse the existing service method or add
   `LogoutSession(ctx, userUID, refreshUID)` that deletes the row scoped to
   the user) **before** clearing the cookie. Response shape unchanged.
2. When there is no `RefreshUID` (a PAT hitting /logout): behave as today —
   just clear the cookie; a PAT has no session row to delete.
3. Failure handling: if the row delete errors, still clear the cookie and
   return 200, but log at ERROR with the refresh UID — a failed DB delete
   must not trap the user in a logged-in UI.
4. Spec `2026-09-25-14` (frontend logout error noise) is complementary and
   unaffected: the frontend calls this endpoint then clears local state.
   Spec `2026-09-25-23` (hashed tokens) makes the deleted-row semantics the
   full revocation story.
5. Docs/changelog: no API change (same request/response), behavior note only.

## Tests

- Both DBs: login → capture refresh token → `POST /auth/logout` with the
  cookie → assert the `user_tokens` row for `RefreshUID` is gone →
  `POST /auth/refresh` with the captured refresh token → 401.
- Logout without `RefreshUID` (PAT auth) → 200, no rows deleted, PAT still
  valid.
- `deleteAllTokens` / `signOutOthers` paths unchanged (existing tests keep
  passing).
- DB error injection: delete fails → response still 200 and cookie cleared
  (row survives; assert log warning via a test logger hook).