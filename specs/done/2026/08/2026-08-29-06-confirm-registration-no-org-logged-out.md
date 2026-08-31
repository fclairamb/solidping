---
model: sonnet
effort: high
---

# Confirming a new email+password registration instantly logs the user out

## Problem

Register a new account with email + password, click the confirmation link in
the email → the "Email confirmed" screen flashes, then the app immediately
bounces to the login page with `session_expired=true`. The account *was*
created, but nothing tells the user that — the perceived state is "it failed,
I have to register again" (and re-registering then fails with email-taken).

Root cause — the zero-org confirmation response carries no session:

1. `ConfirmRegistration` creates the user, then resolves org membership via
   `autoJoinMatchingOrgs`. For the common case — a brand-new email that
   matches no org's `registration.email_pattern` — `len(members) == 0`, and
   the branch at
   [service.go:2432](server/internal/handlers/auth/service.go:2432) returns a
   `LoginResponse` containing **only** `TokenType` and `User`: no
   `AccessToken`, no `RefreshToken`, no `ExpiresIn`.
2. The handler skips `setAccessTokenCookie` (empty token) and returns 200
   ([handler.go:593](server/internal/handlers/auth/handler.go:593)).
3. The frontend handoff
   ([confirm-registration-handoff.ts](web/dash0/src/lib/confirm-registration-handoff.ts))
   calls `setSession(data.accessToken, …)` with `accessToken === undefined`;
   `setSession` ([client.ts:67](web/dash0/src/api/client.ts:67)) does
   `localStorage.setItem(TOKEN_KEY, undefined)`, persisting the literal string
   `"undefined"`, then the route navigates to `/no-org`.
4. `/no-org`'s first authenticated call sends `Authorization: Bearer
   undefined` → 401 → the silent refresh has no refresh token to work with →
   `handleResponse` runs `clearToken()` + `redirectToExpiredLogin()`
   ([client.ts:314](web/dash0/src/api/client.ts:314)) → login page. That is
   the "logged out instantly."

The zero-org state is otherwise fully supported: password/OAuth login mints an
org-less access token via `completeLogin`'s `resolvedOrg == nil` branch
([service.go:698](server/internal/handlers/auth/service.go:698)), and
`GetUserInfo` explicitly handles `claims.OrgSlug == ""`
([service.go:1520](server/internal/handlers/auth/service.go:1520)) so `/no-org`
works with such a token. Only the confirm-registration path forgot to mint one.

## Proposal

Backend (the real fix):

- In `ConfirmRegistration`'s `len(members) == 0` branch, mint a session
  instead of a bare user payload — mirror (or better, reuse)
  `completeLogin`'s `resolvedOrg == nil` branch: `generateAccessToken(user.UID,
  "", role="", "")`, `ExpiresIn`, `TokenType`, `User`, and
  `LoginAction: LoginActionNoOrg`. Consistency question to settle while in
  there: the login no-org branch issues no refresh token (session lasts one
  access-token lifetime, long enough to create an org) — keep confirm
  consistent with that rather than inventing a third behavior.
- The org path (members > 0) already mints a full session and is fine.

Frontend (defense in depth):

- `applyConfirmRegistrationHandoff` must not persist a session from a
  response without an `accessToken` — treat it as an error path (show the
  failure state with a "log in" link) instead of calling
  `setSession(undefined)`.
- Consider guarding `setSession` itself against a falsy access token (throw
  or no-op + console.error) so no other login-shaped path can ever store the
  string `"undefined"`.

Tests (must prove the negative):

- Backend: confirm-registration test where the new user auto-joins no org —
  assert the response carries a non-empty `accessToken` and that `/auth/me`
  succeeds with it. A positive control with an auto-join match already exists
  (`created_with_test.go`).
- Frontend unit: `applyConfirmRegistrationHandoff` with a token-less payload
  does not write `"undefined"` into localStorage.
- E2E (Playwright, `web/dash0/e2e/`): register → fetch the confirmation link
  (test mode) → confirm → land on `/no-org` still authenticated, no bounce to
  login.

## Open questions

- `confirm-registration.$token.tsx` fires the mutation from a `useEffect`
  guarded only by captured state
  ([confirm-registration.$token.tsx:23](web/dash0/src/routes/confirm-registration.$token.tsx:23)),
  so a StrictMode/dev double-mount can consume the one-shot token twice and
  show a spurious "confirmation failed". Secondary to the bug above; fix or
  file separately if it inflates the change.

## Implementation Plan

1. **Backend fix** — `ConfirmRegistration`'s `len(members) == 0` branch
   (`server/internal/handlers/auth/service.go`) now calls `completeLogin(ctx,
   user, nil, "", LoginActionNoOrg, nil, AuthMethodRegistration, authContext)`
   instead of returning a bare `LoginResponse{TokenType, User}`. This reuses
   `completeLogin`'s existing `resolvedOrg == nil` branch (mints an access
   token only, no refresh token — same lifetime rule as `Login`'s no-org
   case) rather than duplicating it. The `members > 0` branch is untouched.
   `handler.go`'s `ConfirmRegistration` already guards
   `setAccessTokenCookie` on `resp.AccessToken != ""`, so no handler change
   is needed.
2. **Backend test** — new `confirm_registration_no_org_test.go`:
   register + confirm with no org auto-join match, assert a non-empty
   `AccessToken`, no `RefreshToken`, `LoginActionNoOrg`, and that
   `ValidateToken` + `GetUserInfo` succeed with the minted token (proves the
   `/no-org` first-call path actually works). Existing positive control in
   `created_with_test.go` (org auto-join match) must stay green.
3. **Frontend defense in depth**:
   - `applyConfirmRegistrationHandoff`
     (`web/dash0/src/lib/confirm-registration-handoff.ts`): treat a response
     with no `accessToken` as a failure path (no `setSession` call) instead
     of forwarding `undefined` into `setSession`.
   - `setSession` (`web/dash0/src/api/client.ts`): guard against a falsy
     access token — no-op + `console.error` rather than persisting the
     literal string `"undefined"`.
4. **Frontend unit test**: `applyConfirmRegistrationHandoff` with a
   token-less payload does not write `"undefined"` (or anything) into
   `localStorage`, and surfaces the failure state.
5. **E2E test** (`web/dash0/e2e/`): register with email+password → fetch the
   confirmation token in test mode → confirm → land on `/no-org` still
   authenticated (no bounce to `/login`).
6. **Open question (StrictMode double-mount)**: evaluate
   `confirm-registration.$token.tsx`'s `useEffect` guard; fix inline if
   small, otherwise leave as-is and flag for a separate spec.
7. QA gate: backend build/lint/test, dash0 build/lint/tsc/unit tests, full
   Playwright suite (shared auth/session code).
