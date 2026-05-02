# Slack Marketplace direct install

## Context

We're switching the App Directory listing from "Install from your landing page" to "Install from Slack Marketplace." That requires a public, no-auth-required URL Slack can hit to start OAuth. Today we don't have one.

The current Slack OAuth flow assumed two things that no longer hold:

1. **The installer is already logged into a SolidPing organization** — `Service.GetOAuthURL(orgUID, redirectURI string) string` (`server/internal/integrations/slack/service.go:483`) takes an `orgUID` and bakes it into the state as `state := orgUID + "_" + nonce` (`service.go:509`). That's incompatible with a Marketplace cold click that has no session.
2. **Each user belongs to one organization** — that constraint is gone. Users can now belong to multiple orgs via `OrganizationMember`, and a Slack workspace ↔ organization mapping is independent of which user clicks Install.

What's already done in the callback path is most of the work for a Slack-first signup:

- `HandleOAuthCallback` (`service.go:97`) already ignores the state argument (`code, _ string`).
- `findOrCreateOrganizationByTeamID` (`service.go:230-273`) finds-or-creates an org keyed on `OrganizationProvider(provider_type=slack, provider_id=team_id)`.
- `findOrCreateUser` (`service.go:320-364`) finds-or-creates a user by Slack OIDC email and links the Slack identity via `UserProvider`.
- `ensureOrganizationMembership` (`service.go:397-439`) adds the installer as a member; the **first** member becomes admin.
- `createOrUpdateConnection` (`service.go:178-228`) is idempotent on `team_id`.
- `authService.GenerateTokensForOAuth` (`service.go:155`) issues access + refresh tokens.
- The handler at `handler.go:33-84` ties it together and currently redirects with tokens in the query string.

What's missing for Marketplace direct install:

- A public install endpoint that generates a real CSRF state and 302s to Slack.
- Real state — today it's a literal `nonce := "0" // TODO` (`service.go:507`).
- State validation in the callback — the `_` parameter at `service.go:97` ignores it; the handler logs a debug if state is missing but still proceeds (`handler.go:54-56`).
- A safe session handoff that doesn't put `accessToken` and `refreshToken` in a redirect URL (`handler.go:75-80`).
- A friendly error page for the install-failure cases that today return JSON.
- The Slack app config flip from landing-page install to Marketplace install.

The Sign-in-with-Slack flow at `server/internal/handlers/auth/slack_service.go:110-173` already has a clean state primitive — base64url-encoded 32-byte nonce, DB-backed `state_entries` storage, TTL, single-use consume-on-validate. The integration flow should reuse that primitive, not roll its own.

## Honest opinion

A few sharp edges to handle before flipping the App Directory radio:

1. **Tokens in the redirect URL are the worst part of the current flow and we shouldn't paper over them in this spec.** `handler.go:75-80` redirects to `/dashboard/org/{slug}/integrations/slack/{conn}?success=true&access_token=…&refresh_token=…`. With the dashboard-initiated install that was already bad — query strings end up in nginx access logs, browser history, and `Referer` headers on every cross-origin asset the dashboard loads after the redirect. With Marketplace direct install it gets *worse*: third-party redirect-handlers (Slack's own, plus whatever browser extensions sit between) may log the URL. The fix is a one-shot exchange code: callback persists `(code → tokens)` with a 60-second TTL and single-use semantics, redirect URL contains only the code, dashboard exchanges it server-to-server. **Recommendation: ship the fix in this spec — same release, same migration cost.**

2. **Reuse the state primitive from `auth/slack_service.go`, don't fork it.** The existing `OAuthState` (nonce + redirectURI + orgSlug + createdAt, stored in `state_entries` with TTL, deleted on validate) is the right shape. Pull it into a small `server/internal/oauthstate/` package keyed on a `kind` discriminator (`"slack-install"`, `"slack-signin"`, `"slack-exchange"`). Two callers becomes three with no duplication.

3. **Drop `GetOAuthURL` entirely.** It has zero callers in the Go code today. The `nonce := "0"` placeholder (`service.go:507`) is a footgun waiting for someone to wire it up. Replace with a single named entry point — `BuildInstallURL(ctx)` — and delete the dead function. There's no reason to keep an exported API that returns a URL with a stub nonce.

4. **team_id ↔ org is 1:1 and irreversible — call this out in the release notes.** `OrganizationProvider` enforces uniqueness on `(provider_type=slack, provider_id=team_id)`. That's correct behavior. But it means: if a workspace was previously installed against a different org via the legacy state-with-orgUID flow, reinstalling won't migrate it. We don't have such installs in production, but a release note keeps us honest.

5. **`ensureOrganizationMembership` makes the first user admin (`service.go:419-421`). That's right for a brand-new workspace, fragile elsewhere.** A second installer (re-auth, scope change, an admin re-installs after a colleague did first) does *not* become admin because membership is checked first. That's correct. But the rule "first member = admin" is fragile if any future cleanup ever wipes memberships. Add a regression test pinning this behavior; don't change the code.

6. **Email-required is a UX cliff.** `service.go:126-128` returns `ErrEmailRequired` if the Slack OIDC response has no email — Slack guests, single-channel guests, and some Enterprise Grid configurations don't share email. Today the user gets a 400 JSON error. With Marketplace install, that's the difference between "install worked" and "install bricked, what now." Friendly redirect to `/saas/install-error?reason=email_missing` with a "sign up with email instead" path is the minimum acceptable behavior.

7. **Slash-command empty state for cold installers is its own UX concern.** First `/check https://example.com` from a freshly-provisioned installer should land on a wizard, not a "no checks yet" empty list. Out of scope here, flagged so it doesn't get lost.

## Scope

**In scope:**

- New public `GET /api/v1/integrations/slack/install` endpoint (no auth) that 302s to Slack.
- New `Service.BuildInstallURL(ctx) (string, error)` using the shared state primitive. No `orgUID` parameter.
- Delete `Service.GetOAuthURL` and the `orgUID + "_" + nonce` state encoding.
- Hardened `OAuthCallback`: state is validated (consume + TTL + reject-on-miss). Today's `_` parameter and "no state? log debug, continue" behavior are both gone.
- One-shot exchange-code session handoff replacing the tokens-in-URL redirect:
  - Callback stores `(code → {accessToken, refreshToken, orgSlug, userUID})` in `state_entries` with `kind="slack-exchange"`, 60s TTL, single-use.
  - Redirect target is `/dashboard/auth/slack/complete?code={code}` — no tokens in URL.
  - New `POST /api/v1/auth/slack/exchange` endpoint, dashboard calls it server-to-server, gets tokens.
- Shared `server/internal/oauthstate/` package extracted from `auth/slack_service.go` and reused by sign-in, install, and exchange.
- Friendly error redirects to `https://www.solidping.io/saas/install-error?reason=…` with reason codes `state_invalid`, `email_missing`, `oauth_failed`, `unknown`. The page lives in the website repo (separate PR — see "Companion changes").
- Slack app manifest / App Directory config: set Direct Install URL to `https://solidping.k8xp.com/api/v1/integrations/slack/install`, flip the App Directory radio.
- Tests covering: state generation, state expiry, state reuse rejection, exchange-code reuse rejection, email-missing, reinstall-same-team_id idempotency, second-installer-is-not-admin.
- Dashboard (`web/dash0`) route at `/dashboard/auth/slack/complete` that reads the code, calls the exchange endpoint, sets the session, replaces history with `/dashboard/org/{slug}`.
- Optional `?source=marketplace|landing|email|dashboard` query parameter on the install endpoint, persisted onto the connection's first creation event for install-source analytics. Best-effort; no schema change.

**Out of scope (own follow-ups):**

- "Link this Slack workspace to my existing org" — requires breaking the 1:1 team_id ↔ org invariant or adding a transfer-ownership flow. Both are bigger than this spec.
- Multi-installer admin promotion / role management UX.
- Token rotation (manifest currently has `token_rotation_enabled: false`).
- Slack Connect cross-workspace channels.
- Slash-command empty-state UX for cold installers.
- Migration of legacy connections created with the old state format — none in production.

## Approach

### 1. Shared state primitive

New package `server/internal/oauthstate/`:

```go
package oauthstate

type Entry struct {
    Nonce     string         `json:"nonce"`
    Kind      string         `json:"kind"`
    CreatedAt int64          `json:"createdAt"`
    Payload   map[string]any `json:"payload,omitempty"`
}

// Generate creates a fresh nonce, persists Entry under "<kind>:<nonce>" with TTL,
// and returns the nonce (suitable for the OAuth state parameter).
func Generate(ctx context.Context, db db.Service, kind string, payload map[string]any, ttl time.Duration) (string, error)

// Validate fetches and deletes the Entry. Returns ErrInvalidState if missing,
// expired, or wrong kind.
func Validate(ctx context.Context, db db.Service, kind, nonce string) (*Entry, error)
```

Reasons:

- 32-byte `crypto/rand` nonce, base64url-encoded — same as `auth/slack_service.go:113-118`.
- `kind` is part of the storage key (`slack-install:abcd…`) so a state minted for sign-in cannot be redeemed by the install callback.
- `Payload` carries flow-specific data (sign-in: `{redirectUri}`; exchange: `{accessToken, refreshToken, orgSlug, userUID}`; install: empty or `{source}`).
- One-time-use: `Validate` deletes the entry as part of validation.

Migrate `auth/slack_service.go` to use it; the on-disk schema (`state_entries` table) is unchanged.

### 2. Public install endpoint

`server/internal/integrations/slack/handler.go` adds:

```go
// Install is the public entry point for Slack Marketplace direct install.
// GET /api/v1/integrations/slack/install[?source=marketplace]
func (h *Handler) Install(writer http.ResponseWriter, req bunrouter.Request) error {
    source := req.URL.Query().Get("source")
    redirectURL, err := h.svc.BuildInstallURL(req.Context(), source)
    if err != nil {
        slog.ErrorContext(req.Context(), "Failed to build Slack install URL", "error", err)
        return h.redirectInstallError(writer, req, "unknown")
    }
    http.Redirect(writer, req.Request, redirectURL, http.StatusFound)
    return nil
}
```

Route registered in `server/internal/app/server.go` under the public-routes group (no auth middleware).

### 3. `BuildInstallURL`

`server/internal/integrations/slack/service.go`:

```go
const installStateTTL = 10 * time.Minute

var (
    slackBotScopes = []string{
        "chat:write", "chat:write.public",
        "channels:read", "groups:read", "groups:write",
        "im:read", "im:write", "im:history",
        "users:read", "users:read.email",
        "team:read", "commands",
        "app_mentions:read", "reactions:write",
        "links:read", "links:write",
    }
    slackUserScopes = []string{"openid", "email", "profile"}
)

func (s *Service) BuildInstallURL(ctx context.Context, source string) (string, error) {
    payload := map[string]any{}
    if source != "" {
        payload["source"] = source
    }

    nonce, err := oauthstate.Generate(ctx, s.db, "slack-install", payload, installStateTTL)
    if err != nil {
        return "", fmt.Errorf("generate install state: %w", err)
    }

    redirectURI := s.cfg.Server.BaseURL + "/api/v1/integrations/slack/oauth"

    params := url.Values{}
    params.Set("client_id", s.cfg.Slack.ClientID)
    params.Set("scope", strings.Join(slackBotScopes, ","))
    params.Set("user_scope", strings.Join(slackUserScopes, ","))
    params.Set("redirect_uri", redirectURI)
    params.Set("state", nonce)

    return "https://slack.com/oauth/v2/authorize?" + params.Encode(), nil
}
```

Delete `GetOAuthURL` and the inline scope lists at `service.go:485-504`.

### 4. Hardened callback

`Service.HandleOAuthCallback` signature changes from `(ctx, code, _ string)` to `(ctx, code, state string)`. State is validated up front:

```go
entry, err := oauthstate.Validate(ctx, s.db, "slack-install", state)
if err != nil {
    return nil, ErrInvalidState
}
// entry.Payload may carry "source" for analytics
```

Return values stay the same. `OAuthResult` gains a `UserUID` field (the post-callback session needs it).

`Handler.OAuthCallback` updates:

- On `ErrInvalidState` → 302 to `/saas/install-error?reason=state_invalid`.
- On `ErrEmailRequired` → 302 to `/saas/install-error?reason=email_missing`.
- On `ErrOAuthFailed` → 302 to `/saas/install-error?reason=oauth_failed`.
- On other errors → 302 to `/saas/install-error?reason=unknown` and log.
- On success → mint exchange code (see step 5).

The `errorParam != ""` early-return at `handler.go:40-47` redirects to the same error page instead of `/integrations?error=…`.

### 5. Exchange-code session handoff

After a successful `HandleOAuthCallback`:

```go
exchangePayload := map[string]any{
    "accessToken":  result.AccessToken,
    "refreshToken": result.RefreshToken,
    "orgSlug":      result.OrgSlug,
    "userUID":      result.UserUID,
}
code, err := oauthstate.Generate(ctx, s.db, "slack-exchange", exchangePayload, 60*time.Second)
if err != nil {
    return h.redirectInstallError(writer, req, "unknown")
}
http.Redirect(writer, req.Request,
    s.cfg.Frontend.BaseURL+"/dashboard/auth/slack/complete?code="+url.QueryEscape(code),
    http.StatusFound,
)
```

New auth handler endpoint:

```go
// POST /api/v1/auth/slack/exchange
// Body: {"code": "..."}
// Response: {"accessToken": "...", "refreshToken": "...", "orgSlug": "...", "userUID": "..."}
//
// Single-use; entry is deleted as part of validation.
func (h *AuthHandler) SlackExchange(writer http.ResponseWriter, req bunrouter.Request) error {
    var body struct{ Code string `json:"code"` }
    if err := json.NewDecoder(req.Body).Decode(&body); err != nil { ... }

    entry, err := oauthstate.Validate(req.Context(), h.db, "slack-exchange", body.Code)
    if err != nil {
        return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeInvalidToken, "Code invalid or already used")
    }

    return h.WriteJSON(writer, http.StatusOK, entry.Payload)
}
```

Lifetime: 60 seconds. The dashboard hits this immediately after landing on `/dashboard/auth/slack/complete`, so the window is intentionally tight.

### 6. Friendly error page (companion change in `solidping-website`)

`src/pages/saas/install-error.md` (separate PR in the website repo):

| `reason` | User-facing message |
| --- | --- |
| `state_invalid` | "Your install link expired or was already used. Try again from the SolidPing for Slack page." |
| `email_missing` | "Slack didn't share an email address for your account. Ask your workspace admin to grant the `users:read.email` scope, or sign up with email instead." |
| `oauth_failed` | "Slack returned an error during install. If this keeps happening, contact support." |
| anything else | Generic fallback with a "Try again" button. |

Each case includes a "Try again" link to `https://solidping.k8xp.com/api/v1/integrations/slack/install` and a "Contact support" link to `/saas/support`.

### 7. Slack app config (no code change, console action)

- App Directory listing → "Installing Your App" → flip from "Install from your landing page" to "Install from Slack Marketplace."
- Direct Install URL: `https://solidping.k8xp.com/api/v1/integrations/slack/install`.
- Redirect URLs: keep both `…/integrations/slack/oauth` (bot install callback) and `…/auth/slack/callback` (Sign in with Slack).
- Production app manifest mirrors the dev manifest, minus the `(dev)` suffix on display name and bot user.

### 8. Tests

`server/internal/oauthstate/oauthstate_test.go`:
- `TestGenerateValidate_RoundTrip`
- `TestValidate_RejectsWrongKind`
- `TestValidate_RejectsExpired`
- `TestValidate_RejectsReuse`
- `TestValidate_RejectsUnknown`

`server/internal/integrations/slack/service_test.go` (new tests, table-driven):
- `TestBuildInstallURL_GeneratesValidStateAndScopes`
- `TestHandleOAuthCallback_RejectsMissingState`
- `TestHandleOAuthCallback_RejectsInvalidState`
- `TestHandleOAuthCallback_RejectsExpiredState`
- `TestHandleOAuthCallback_RejectsReusedState`
- `TestHandleOAuthCallback_RejectsSignInState` (sign-in nonce minted with `kind="slack-signin"` cannot be redeemed by the install callback)
- `TestHandleOAuthCallback_ReinstallSameTeamIsIdempotent` (two valid OAuth round-trips for the same `team_id` produce one connection, updated tokens)
- `TestHandleOAuthCallback_SecondInstallerIsRegularMember` (asserts `MemberRoleUser`, not `MemberRoleAdmin`)
- `TestHandleOAuthCallback_EmailMissingReturnsErr`

`server/internal/handlers/auth/slack_exchange_test.go`:
- `TestSlackExchange_HappyPath`
- `TestSlackExchange_ReusedCodeRejected`
- `TestSlackExchange_ExpiredCodeRejected`
- `TestSlackExchange_UnknownCodeRejected`
- `TestSlackExchange_WrongKindRejected` (a `slack-install` nonce cannot be exchanged via the exchange endpoint)

Use testcontainers for the DB (matches existing convention per `server/CLAUDE.md`). Mock Slack's token-exchange and OIDC-userinfo endpoints with `httptest.Server` injected via the existing `Slack.BaseURL`-style override (add one if not present).

E2E in `web/dash0` (Playwright):
- Visit `/api/v1/integrations/slack/install` with a freshly-cleared browser → mocked Slack OAuth round-trip → user lands on `/dashboard/org/{slug}` signed in.
- Visit the same endpoint with an existing session for a different org → user is added as a member of the workspace's org and ends up on its dashboard (existing other-org session is not destroyed; standard `switch-org` flow handles the rest).

### 9. Dashboard change (`web/dash0`)

- New route `/dashboard/auth/slack/complete`:
  1. Read `code` from query string.
  2. `POST /api/v1/auth/slack/exchange` with `{"code": code}`.
  3. Set the session via the existing post-login helper (cookies / localStorage, whatever today's flow uses).
  4. `replace()` history with `/dashboard/org/{orgSlug}`.
  5. On any error → redirect to `https://www.solidping.io/saas/install-error?reason=oauth_failed`.
- If a dashboard "Add Slack" button exists today and uses the legacy URL, replace it with a link to `/api/v1/integrations/slack/install?source=dashboard`. The auto-create-vs-link behavior is identical from the user's perspective.

### 10. Migration

No DB migration. `state_entries` already exists. Legacy `orgUID + "_" + nonce` state has zero callers in Go (the dashboard never wired it up), so there are no in-flight installs to preserve.

If any in-progress development install is mid-OAuth at deploy time, it fails with `state_invalid` and the user clicks Try Again. Acceptable.

## Risks

- **Slack changes the OAuth response shape.** Mitigated by the existing `OAuthResponse` struct being narrow and well-tested.
- **`state_entries` cleanup.** TTL handling is on the read path (`Validate` rejects expired); we should confirm the table has a janitor or grows unboundedly. If unbounded, add a periodic cleanup job — separate spec, but worth checking before this rolls out at Marketplace volume.
- **App Directory review delay.** Slack typically reviews submissions in 1-3 weeks. If review fails for non-code reasons (icon, screenshots, description), nothing in this spec needs to change — fix the listing and resubmit.
- **Email scope dependency.** `users:read.email` plus `openid email profile` is required for the auto-provision flow to identify the installer. A workspace admin who somehow strips email scopes during install will hit `email_missing`; we redirect to the friendly error page rather than silently failing.

## Companion changes (not in this repo)

- `solidping-website`: add `src/pages/saas/install-error.md` with the four reason codes from §6.
- Slack App Directory listing: flip the install radio, set the Direct Install URL, finalize description and screenshots for production app (separate from the `(dev)` app).
