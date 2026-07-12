# SSO callbacks never set the `access_token` cookie — MCP OAuth consent needs a login-page bounce

## Problem

The SSO provider callbacks all redirect back to the SPA with tokens in the URL
query but never call `http.SetCookie` for the `access_token` cookie:

- `server/internal/handlers/auth/google.go:93` (`buildSuccessRedirect` →
  `http.Redirect(..., http.StatusFound)`)
- `server/internal/handlers/auth/github.go:93`
- `server/internal/handlers/auth/gitlab.go:93`
- `server/internal/handlers/auth/microsoft.go:93`
- `server/internal/handlers/auth/slack.go:84`
- `server/internal/handlers/auth/discord.go:89`
- `server/internal/handlers/auth/oidc.go:101`
- `server/internal/handlers/auth/saml.go:102`

This is inconsistent with the password flows in
`server/internal/handlers/auth/handler.go`, where Login (`handler.go:116`),
Refresh, SwitchOrg, and Verify2FA all set the cookie:

```go
http.SetCookie(writer, &http.Cookie{
    Name:   CookieAuthToken, // "access_token" (handler.go:16)
    Value:  resp.AccessToken,
    Path:   "/",
    MaxAge: resp.ExpiresIn,
})
```

### Why it matters

The cookie is what the embedded MCP OAuth authorization server authenticates
with — `extractSessionToken` in `server/internal/oauth/authorize.go` reads it,
both for rendering the consent screen and for the consent screen's native form
POST (a cross-page form submit that carries no `Authorization` header).

Since commit `e4c68f15`, the dashboard login page works around the missing
cookie: before navigating to an OAuth-authorize `returnTo`, it forces a
`POST /auth/refresh` (which re-sets the cookie). The MCP connect flow therefore
*works* for SSO users, but they take a visible bounce through `/dash0/login`
instead of going straight to the consent screen. Password users go straight
through because Login already set the cookie.

## Proposal

1. **Shared helper** in `server/internal/handlers/auth/handler.go`, e.g.:

   ```go
   // setAccessTokenCookie sets the SPA session cookie so cookie-authenticated
   // surfaces (the embedded MCP OAuth authorize/consent flow) work without a
   // refresh bounce.
   func setAccessTokenCookie(w http.ResponseWriter, accessToken string, expiresIn int) {
       http.SetCookie(w, &http.Cookie{
           Name:   CookieAuthToken,
           Value:  accessToken,
           Path:   "/",
           MaxAge: expiresIn,
       })
   }
   ```

   Optionally refactor the existing `http.SetCookie` call sites in
   `handler.go` (Login `:116`, plus the Refresh/SwitchOrg/Verify2FA blocks) to
   use it so the cookie shape stays defined in one place.

2. **Call it in every SSO callback** right before the success redirect (the
   provider result structs differ — `GoogleOAuthResult`, `SAMLResult`, … — but
   all expose `AccessToken` and `ExpiresIn`, mirroring what
   `buildSuccessRedirect` already reads):

   ```go
   redirectURL := h.buildSuccessRedirect(oauthState.RedirectURI, result)
   setAccessTokenCookie(writer, result.AccessToken, result.ExpiresIn)
   http.Redirect(writer, req.Request, redirectURL, http.StatusFound)
   ```

   The callback response is a 302; `Set-Cookie` on a redirect response works
   fine. Success path only — error redirects (`redirectWithError`,
   `handleOAuthError`) must not set a cookie.

   Files: `google.go`, `github.go`, `gitlab.go`, `microsoft.go`, `slack.go`,
   `discord.go`, `oidc.go`, `saml.go`.

3. **Test**: extend one provider's handler callback test (whichever has the
   most complete success-path coverage) to assert the response carries a
   `Set-Cookie` header for `access_token` with `Path=/` and a positive
   `Max-Age`, alongside the existing 302/Location assertions.

### Non-goals / follow-up

- The dashboard's forced-refresh workaround from `e4c68f15` stays: it is still
  the safety net for pre-existing sessions whose cookie has expired while the
  refresh token is still valid. Simplifying it is a separate frontend concern.

## QA

- `make build-backend lint-back test`

## Implementation Plan

1. **Shared helper** — add `setAccessTokenCookie(w http.ResponseWriter, accessToken string, expiresIn int)`
   to `server/internal/handlers/auth/handler.go`, and refactor every existing
   access-token `http.SetCookie` call site to use it so the cookie shape lives
   in one place:
   - `handler.go`: Login, Refresh, SwitchOrg, ConfirmRegistration, CreateOrg,
     AcceptInvite, Verify2FA, Recovery2FA
   - `passkey_handler.go`: FinishLogin

   (`clearAuthCookie` stays as-is — it clears, not sets.)

2. **Slack result gap** — `SlackOAuthResult` (`slack_service.go`) is the one
   provider result struct without `ExpiresIn`; add the field and populate it
   from `tokens.ExpiresIn` in `HandleCallback` so the callback can set a
   correctly-scoped cookie. (The redirect URL query stays unchanged —
   non-goal.)

3. **Set the cookie in every SSO callback** — in `google.go`, `github.go`,
   `gitlab.go`, `microsoft.go`, `slack.go`, `discord.go`, `oidc.go`, `saml.go`,
   call `setAccessTokenCookie(writer, result.AccessToken, result.ExpiresIn)`
   right before the success `http.Redirect`. Success path only; error
   redirects (`redirectWithError`, `handleOAuthError`, `handleSAMLError`) are
   untouched.

4. **Test** — the OIDC provider is the only one whose *real*
   `HandleCallback` runs end-to-end in tests (fake in-repo IdP in
   `oidc_service_test.go`), so add a handler-level test there:
   drive `OIDCOAuthHandler.Callback` through `httptest` with a valid
   state + fake IdP code and assert the 302 response carries a `Set-Cookie`
   for `access_token` with `Path=/`, a positive `Max-Age`, and a value
   matching the `access_token` query param in `Location`. Also assert the
   invalid-state error redirect sets no cookie.

5. **QA** — `make build-backend lint-back test`.
