# Auth session continuity — silent access-token refresh, sliding sessions, and a Sessions vs API tokens UI

## Context

Using the dashboard on the k8xp dev deployment (`solidping.k8xp.com`), users
get abruptly kicked to the login page with a 401 mid-session. The suspicion
was "no mechanism to refresh the JWT from the refresh token, or the refresh
token is too short" — and the first part is exactly right: the backend ships
a complete refresh flow that the frontend never uses. With the default 1-hour
access-token TTL, every dashboard session hard-dies 60 minutes after login,
no matter how active the user is.

Two goals:

1. Fix the disconnects: the client must keep its access token fresh using the
   refresh token, and an active user must never be logged out.
2. Give tokens a proper home in the UI: **Sessions** (login/refresh tokens,
   one per device) and **API tokens** (PATs) are different things and must be
   presented as such — today only PATs are visible and sessions cannot be
   seen or revoked at all.

## Current state (verified 2026-07-05; re-verify at build)

- **Backend refresh flow exists and works.** Login
  (`POST /api/v1/auth/login`, `server/internal/handlers/auth/handler.go:81-119`)
  returns `accessToken` (HS256 JWT), `refreshToken` (opaque
  `{32 hex}_{nano-ts}` string), `expiresIn` (seconds), and sets an
  `access_token` cookie with `MaxAge = expiresIn` (handler.go:112-117).
  `POST /api/v1/auth/refresh` (handler.go:154-185, service.go:856-946) takes
  `{"refreshToken": "..."}`, validates it against `user_tokens`, and returns a
  fresh `accessToken` + the **same** `refreshToken` (no rotation), bumping
  `last_active_at` at 1-hour write granularity. It does **not** re-set the
  cookie and does **not** extend the row's `expires_at`.
- **TTL defaults** (`server/internal/config/config.go:668-669`, koanf keys
  `auth.access_token_expiry` / `auth.refresh_token_expiry`, env
  `SP_AUTH_ACCESS_TOKEN_EXPIRY` / `SP_AUTH_REFRESH_TOKEN_EXPIRY`): access
  **1h**, refresh **7 days** — and the 7 days are fixed at login, never
  extended by activity.
- **Storage**: `user_tokens`
  (`server/internal/db/postgres/migrations/001_v0_1_0.up.sql:122-148`) —
  `uid`, `user_uid`, `organization_uid`, unique `token`, `type` in
  `('pat','refresh','oauth_refresh')`, `properties` JSONB (login rows carry
  `created_with: {method: password|oauth|passkey, userAgent, remoteAddr}`),
  `expires_at`, `last_active_at`, soft-delete `deleted_at`.
- **The frontend drops the refresh token on the floor.** All login paths call
  `setToken(data.accessToken)` only
  (`web/dash0/src/contexts/AuthContext.tsx:179,242,283`); only the access
  token is persisted (localStorage key `solidping_session_token`,
  `web/dash0/src/api/client.ts`). `refreshToken` appears in the codebase only
  as unused response-type fields (`api/twofa.ts:20`, `api/passkeys.ts:44`,
  `routes/auth.slack.complete.tsx:21`). Nothing ever calls
  `/api/v1/auth/refresh`. There is no expiry tracking and no proactive timer.
- **401 handling**: any 401 in `apiFetch` clears the token and hard-redirects
  to `/orgs/{org}/login?session_expired=true&returnTo=…`
  (client.ts:102-114). That redirect *is* the reported bug surfacing.
- **Live WebSocket**: the server closes the events socket with code `4401`
  when the token expires (`web/dash0/src/lib/live-socket.ts:151-153`); the
  reconnect loop re-reads the same expired token from localStorage and churns.
- **Access tokens are not linked to their session.** `Claims`
  (service.go:136-147) carries `UserUID`/`OrgSlug`/`Role`/`Scopes` + a random
  JTI — no reference to the `user_tokens` row that issued it (grep confirms
  no `refresh_uid` anywhere), so "current session" cannot be computed.
- **Tokens UI**: a single page `/orgs/$org/account/tokens`
  (`web/dash0/src/routes/orgs/$org/account.tokens.tsx`) lists PATs
  (`GET …/tokens?type=pat`) with create (one-time secret display) and revoke.
  Sessions are invisible; there is no "sign out other sessions" (backend
  logout already supports `deleteAllTokens`). The listing endpoints already
  accept a `?type=` filter (handler.go:231,251). The account section has
  routes `account.{index,profile,security,notifications,tokens}.tsx`.

## Design decisions

### D1 — Wire the refresh flow into the client (the bug fix)

The client persists the whole session, not just the access token:

- Store `refreshToken` and a computed `expiresAt` (from `expiresIn`)
  alongside the access token in localStorage. Every path that receives an
  `accessToken` must capture them: password login, 2FA verify, recovery
  code, passkey login, OAuth completion, Slack completion, `switch-org`.
  Verify at build how each OAuth-style callback actually delivers tokens —
  `loginWithOAuth(accessToken, orgSlug)` currently threads only the access
  token; if a callback genuinely can't carry the refresh token today, fix
  that path server-side rather than leaving it session-mortal.
- **Proactive refresh**: a ~60s interval checks `expiresAt`; when less than
  ⅓ of the access-token lifetime remains (20 min on 1h tokens), call
  `POST /api/v1/auth/refresh` and store the new `accessToken`/`expiresAt`.
- **Reactive refresh**: on a 401, `apiFetch` first attempts one refresh and
  retries the original request once. The refresh call is **single-flight** —
  a module-level in-flight promise shared by all concurrent 401s, so a burst
  of requests triggers exactly one refresh. Only when refresh itself fails
  (or no refresh token is stored) fall through to today's behavior: clear
  state, redirect to `login?session_expired=true&returnTo=…`. The 401→login
  convention is unchanged; it just becomes the last resort.
- **Multi-tab**: tabs share localStorage and the server does not rotate
  refresh tokens, so concurrent refreshes from several tabs are benign (each
  gets a valid access token, same refresh token). Single-flight is per-tab;
  that is sufficient.
- **Live socket**: on a `4401` close, ask the same single-flight helper for
  a refresh before reconnecting, instead of re-reading a dead token.

### D2 — Sliding session expiry (the "too short" fix)

A refresh token valid 7 fixed days still logs out a daily active user every
week. On each successful `/auth/refresh`:

- Extend the row's `expires_at` to `now + refresh_token_expiry`, at the same
  1-hour write granularity already used for `last_active_at` (no write
  amplification). Active sessions live forever; a session idle longer than
  `refresh_token_expiry` (default stays 7d) dies.
- Re-set the `access_token` cookie exactly like login does, so
  cookie-authenticated surfaces don't silently lapse after the first hour.

### D3 — Bind access tokens to their session

`Claims` gains `refreshUid` (`omitempty`): the `user_tokens.uid` of the
refresh-token row that issued this access token. Set on login and refresh;
empty for PAT-validated claims. This enables:

- `isCurrent` on the sessions listing (row uid == caller's `refreshUid`);
- "sign out other sessions" that spares the caller's own session;
- **`switch-org` correctness** (verify at build): after an org switch, a
  background refresh must not flip the user back to the login-time org.
  Update the refresh-token row's `organization_uid` on switch (or mint a new
  refresh token and return it) so refresh reproduces the selected org, and
  keep `refreshUid` consistent either way.

### D4 — Session management API

Sessions are `user_tokens` rows with `type = 'refresh'` (`oauth_refresh`
rows are provider credentials, not dashboard sessions — never listed):

- `GET /api/v1/auth/tokens?type=refresh` (filter already exists) returns per
  session: `uid`, `orgSlug`, `createdAt`, `lastActiveAt`, `expiresAt`,
  `isCurrent`, and the `created_with` metadata (`method`, `userAgent`,
  `remoteAddr`) surfaced as camelCase fields. Keep/align the list wrapper
  with the `{"data": [...]}` convention.
- `DELETE /api/v1/auth/tokens/{uid}` already revokes; ensure it works for
  session rows and that revocation takes effect immediately (the session's
  next refresh gets 401).
- Logout grows a "sign out other sessions" action alongside the existing
  `deleteAllTokens`: delete all of the user's `refresh` rows **except** the
  caller's `refreshUid`; respond with the deleted count. Mutually exclusive
  with `deleteAllTokens`.
- **Session cap**: at login, cap active `refresh` rows per user (10);
  soft-delete least-recently-active rows beyond the cap. Required hygiene
  once expiry slides — otherwise sessions accumulate unboundedly.

### D5 — UI: Sessions and API tokens as distinct surfaces

Two account pages instead of one token dump:

- **New route** `/orgs/$org/account/sessions` (`account.sessions.tsx`), new
  "Sessions" entry in the account nav. `/orgs/$org/account/tokens` stays
  PAT-only, labeled "API tokens".
- **Sessions page** — list sorted current-first, then `lastActiveAt` desc.
  Each session renders as a card/row with:
  - device icon (mobile / tablet / desktop) + browser name/version + OS,
    parsed from the stored user agent (a small parsing utility or a tiny
    dependency — no heavyweight UA parser); the raw UA as muted mono text;
  - a highlighted **"Current session"** badge + accent border on the
    caller's own session ("this device");
  - a login-method badge (`password` / `oauth` / `passkey`);
  - "Connected {createdAt}", "Last active {lastActiveAt}", expiry, and the
    IP (`remoteAddr`) shown raw;
  - a per-row revoke button — `Trash2`, destructive red, per convention;
    revoking the current session behaves as logout;
  - a **"Sign out other sessions"** button (destructive) with a confirmation
    dialog naming the count; disabled when there are none. On success the
    list refreshes in place — no redirect.
- **API tokens page**: existing PAT flow (create with one-time secret
  display, expiry presets, revoke) unchanged; just verify no `refresh` rows
  can leak into it.
- Both pages fully mobile-usable; build from the design-reference primitives
  and add any missing primitive (e.g. a session card/badge pattern) to the
  design-reference page as part of the change.

## Non-goals

- **Refresh-token rotation.** Deliberately out: rotation with single-use
  enforcement breaks concurrent refreshes from multiple tabs/devices unless
  a reuse grace-window is added — reintroducing the very class of surprise
  logouts this spec removes. Revisit separately if theft-hardening becomes a
  requirement.
- Device/IP binding of tokens (`created_with` stays display-only) and
  IP→geolocation resolution (raw IP is enough for now).
- Admin visibility into *other* users' sessions.
- "Remember me" / per-login configurable session length.
- Any change to PAT mechanics (prefix, scopes, validation cache).

## Acceptance criteria

1. **The disconnect is gone**: with a short `SP_AUTH_ACCESS_TOKEN_EXPIRY`
   on the test server, a logged-in dashboard left past the access-token
   expiry keeps working — the next user action succeeds with no redirect to
   login (Playwright E2E), both via the proactive timer and via the
   reactive 401 path.
2. **Single-flight**: N concurrent requests hitting 401 produce exactly one
   `/auth/refresh` call, and the original requests retry and succeed
   (frontend unit test with a mocked fetch).
3. **Sliding window**: a successful refresh extends the session row's
   `expires_at` to `now + refresh_token_expiry` (with granularity) and
   re-sets the `access_token` cookie. An expired or revoked refresh token
   makes refresh fail and the client lands on
   `login?session_expired=true&returnTo=…` — the existing convention.
4. **Claims linkage**: access tokens minted by login and refresh carry
   `refreshUid`; PAT-validated claims don't. The sessions listing flags
   exactly the caller's row `isCurrent`.
5. **Sessions surface**: the sessions page shows browser/OS/device, login
   method, connected / last-active / IP, current session first and
   highlighted. Revoking another session invalidates it immediately (its
   next refresh 401s). "Sign out other sessions" deletes all but the
   current one and reports the count. The API tokens page lists only PATs.
6. **Cap**: an 11th concurrent login prunes the least-recently-active
   session; the 10 newest survive.
7. **Live socket**: after access-token expiry the events socket recovers
   without user action (refresh, then reconnect with the fresh token).
8. **Org stability**: `switch-org` followed by a refresh keeps the switched
   org — no silent flip back to the login-time org.
9. **Tests**: backend table-driven coverage for sliding expiry, `refreshUid`
   claims, logout-others, the session cap, and the `type` filter; frontend
   unit tests for single-flight and expiry scheduling; Playwright E2E for
   the past-expiry survival scenario and the sessions page (list, revoke,
   sign-out-others). `make lint` and `make test` pass; no new dash0 eslint
   errors.

## Open questions

- Should `refresh_token_expiry` default rise (e.g. 30d) now that it slides
  with activity? Leaning **no** — 7 days as an *idle* timeout is a sane
  security default once active users are never logged out.
- Exact delivery of the refresh token on OAuth/Slack/passkey callbacks
  (D1): confirm each callback's response/URL shape at build and close any
  path that can only produce a refresh-less session.

## Implementation Plan

### Open questions — resolved

**`refresh_token_expiry` default**: stays **7 days** (`server/internal/config/config.go:668-669`).
No code change to the default.

**Refresh-token delivery per callback** (verified by reading each path):

| Path | Delivers `refreshToken` today? | Fix needed |
|---|---|---|
| Password login (`applyLoginResponse`) | Yes — `LoginResponse.RefreshToken` (service.go:571-573) | Frontend must capture it (currently dropped) |
| 2FA verify / recovery (`applyLoginResponse`) | Yes — same `LoginResponse` shape via `completeLoginAfter2FA` | Frontend must capture it |
| Passkey login (`applyLoginResponse`, `PasskeyLoginResponse`) | Yes — `refreshToken?` already in the wire type (`api/passkeys.ts:44`) | Frontend must capture it |
| `switch-org` (`AuthContext.switchOrg`) | Yes — `SwitchOrg` mints and returns a refresh token (service.go:1426-1445) | Frontend must capture it |
| Generic OAuth (Google/GitHub/GitLab/Discord/Microsoft) callback → `orgs/$org.tsx` reads `access_token` from URL, calls `loginWithOAuth(accessToken, orgSlug)` | **Yes, server-side** — every provider's `buildSuccessRedirect` already sets `query.Set("refresh_token", result.RefreshToken)` (confirmed identical in `google.go:118-119`, `github.go`, `gitlab.go`, `discord.go`, `microsoft.go`) alongside `access_token` and `expires_in` (needs adding, see below) | **Frontend-only fix**: `orgs/$org.tsx` must also read `refresh_token` from the URL and `loginWithOAuth` must accept/thread it through to storage. No server change needed for the token itself. |
| Slack install (`auth.slack.complete.tsx` → `/api/v1/auth/slack/exchange`) | Yes — `ExchangeResponse.refreshToken` already in the JSON body (route.tsx:21) | **Frontend-only fix**: same `loginWithOAuth` signature change picks it up. |

Conclusion: **no server-side path is refresh-less.** All backend flows already mint
and deliver a refresh token; the bug is 100% client-side (the frontend reads
`accessToken` and discards everything else). One gap found while verifying: the
OAuth redirect query string does **not** currently include `expires_in`
(`buildSuccessRedirect` in `google.go`/`github.go`/`gitlab.go`/`discord.go`/`microsoft.go`
only sets `access_token`, `refresh_token`, `org`) — add `expires_in` there (from
`OAuthLoginResponse`/equivalent result struct's already-computed access-token TTL)
so the client can compute `expiresAt` for OAuth logins exactly like every other
path, instead of assuming the configured default.

### D1 — client-side wiring

**Backend (small, additive-only):**
- `server/internal/handlers/auth/google.go`, `github.go`, `gitlab.go`, `discord.go`,
  `microsoft.go` (`buildSuccessRedirect`): add `query.Set("expires_in", strconv.Itoa(result.ExpiresIn))`
  once each provider's `*OAuthResult` struct carries `ExpiresIn` (mirror `GoogleOAuthResult`
  et al. — check whether `ExpiresIn` is already on the result struct; if not, thread
  `int(s.cfg.AccessTokenExpiry.Seconds())` through like `GenerateTokensForOAuth` does).

**Frontend:**
- `web/dash0/src/api/client.ts`: add `setSession(accessToken, refreshToken?, expiresIn?)`
  storing `solidping_session_token` (unchanged), new `solidping_refresh_token`, and new
  `solidping_expires_at` (epoch ms = `Date.now() + expiresIn*1000`). Add
  `getRefreshToken()`, `getExpiresAt()`, `clearToken()` also clears the two new keys.
- `web/dash0/src/contexts/AuthContext.tsx`:
  - `AuthResponse` interface gains `refreshToken?: string; expiresIn?: number`.
  - `applyLoginResponse` (currently `setToken(data.accessToken)` at line 179) →
    `setSession(data.accessToken, data.refreshToken, data.expiresIn)`. Covers password,
    2FA verify/recovery, and passkey login (all three route through this function).
  - `acceptInviteSession` (line 242) → same change; `AcceptInvite`/`ConfirmRegistration`
    responses already carry `RefreshToken`/`ExpiresIn` on `LoginResponse` — verify at
    the call site the JSON includes them (it does; same `LoginResponse` struct).
  - `loginWithOAuth(accessToken, orgSlug)` (line 259) → change signature to
    `loginWithOAuth(accessToken, orgSlug, refreshToken?, expiresIn?)`, call
    `setSession(...)` instead of `setToken(...)`. Update both call sites:
    `orgs/$org.tsx` (~line 811-818, add `params.get("refresh_token")` and
    `params.get("expires_in")`) and `auth.slack.complete.tsx` (~line 56, pass
    `data.refreshToken` — `ExchangeResponse` already declares it).
  - `switchOrg` (line 283) → `setSession(data.accessToken, data.refreshToken, data.expiresIn)`.
- Proactive refresh: new `web/dash0/src/lib/token-refresh.ts` (or inline in
  `AuthContext`) exporting `refreshAccessToken()` — the single-flight helper (module-level
  `let inFlight: Promise<string | null> | null`), used by both the interval and
  `apiFetch`'s reactive path and `live-socket.ts`. `refreshAccessToken()`:
  reads refresh token from storage; if absent, resolves `null` immediately (no network
  call); else POSTs `/api/v1/auth/refresh`, on success calls `setSession(new access,
  same refresh, expiresIn)` and resolves the new access token; on failure clears session
  and resolves `null`. Wrapped so concurrent callers share the same in-flight promise
  and it resets to `null` after settling (success or failure) so the *next* 401 can
  trigger a fresh refresh.
  - `AuthProvider` mounts a `setInterval(60_000)` effect: if `getExpiresAt()` exists and
    `expiresAt - Date.now() < accessTokenLifetime/3` (derive lifetime from
    `expiresAt - issuedAt`... simpler: store `expiresIn` too and use
    `expiresIn*1000/3` as the threshold directly), call `refreshAccessToken()`.
- Reactive refresh in `apiFetch` (`client.ts:102-114`): on 401 (and `!skipAuth`),
  before clearing/redirecting: call `refreshAccessToken()`; if it resolves a token,
  retry the original request once with the new `Authorization` header and return that
  result; if it resolves `null`, fall through to today's clear+redirect. Must not
  recurse into infinite 401 loops — the retry itself does not re-trigger refresh on a
  second 401 (pass a private `_isRetry` flag or just don't re-wrap).
- `live-socket.ts`: `connectOnce`'s `sock.onclose` currently has no special case for
  4401 (spec's claimed line numbers were the JSDoc comment, not code) — add
  `CLOSE_TOKEN_EXPIRED = 4401` and in `onclose`, when `ev.code === CLOSE_TOKEN_EXPIRED`,
  call `refreshAccessToken()` (awaited) before `finish("disconnected")` so the reconnect
  loop's next `getToken()` read picks up the fresh token instead of looping on a dead one.

### D2 — sliding session expiry + cookie re-set

`server/internal/handlers/auth/service.go` `Refresh` (~858-946):
- After computing `now`, only write `expires_at`/`last_active_at` when
  `token.LastActiveAt == nil || now.Sub(*token.LastActiveAt) > time.Hour`
  (mirror the existing granularity guard at line 1044, `ValidatePATToken`) — the
  current unconditional write at line 924 has no such guard; add one so this doesn't
  regress into a write on every refresh. On a granularity-triggered write, set BOTH
  `LastActiveAt: &now` and `ExpiresAt: &newExpiresAt` (`now.Add(s.cfg.RefreshTokenExpiry)`)
  in the same `UpdateUserToken` call (single write, no amplification).
- `server/internal/handlers/auth/handler.go` `Refresh` (154-185): re-set the
  `access_token` cookie exactly like `Login` (112-117) — it currently only builds the
  JSON body; add the same `http.SetCookie` call before `WriteJSON`.

### D3 — `refreshUid` claims + switch-org fix

- `Claims` (service.go:136-147): add `RefreshUID string \`json:"refreshUid,omitempty"\`` .
- `generateAccessToken` (service.go:1530-1548): add a `refreshUID string` parameter;
  set `claims.RefreshUID = refreshUID` when non-empty. Update all call sites:
  - `completeLogin` (543): pass the refresh token row's `UID` (available after
    `s.db.CreateUserToken` — reorder so the row is created before the access token is
    minted, or mint access token after; currently access token is generated at line 543
    *before* the refresh row is created at line 567 — must swap this order to have the
    UID in hand).
  - `Refresh` (916): pass `token.UID` (the refresh-token row driving this refresh).
  - `SwitchOrg` (1421): pass the *new* refresh token row's UID (see below).
  - `ValidatePATToken`/2FA temp-token paths: pass `""` (PAT-validated claims and 2FA
    temp tokens carry no `refreshUid` — 2FA temp tokens use a separate `TwoFAClaims`
    type already, unaffected).
  - `GenerateMCPAccessToken`: unaffected (separate claims path, no refresh binding).
- **Switch-org correctness**: `SwitchOrg` (1380-1465) already mints a **new** refresh
  token row scoped to the target org (1429-1445) — this already avoids the "stale org"
  bug structurally, since a subsequent `/auth/refresh` reads `token.OrganizationUID`
  from whichever refresh token the client is holding. The fix is client-side: after
  `switchOrg()`, `AuthContext` must overwrite the stored `refreshToken` with the
  **new** one from the response (already covered by the D1 `setSession(...)` change
  in `switchOrg`) — today the switch-org path doesn't matter yet since refresh tokens
  aren't stored at all, but once D1 lands, using the *old* refresh token after a switch
  would silently refresh back into the pre-switch org. Verify the response actually
  contains the new `refreshToken` (it does — `SwitchOrgResponse` reuses `LoginResponse`,
  field populated at line 1449) and that `setSession` overwrites (not merges) the
  refresh token on every call. No backend change needed beyond the `refreshUid` claim
  wiring above — the existing "mint a new refresh token on switch" behavior already
  satisfies the design decision's stated fix.
- Add a helper `isCurrentSession(claims *Claims, tokenUID string) bool` (or inline
  `claims.RefreshUID == tok.UID`) used by the session listing (D4).

### D4 — session management API

- `TokenInfo` (service.go:299-308): add `OrgSlug` (already present), `IsCurrent bool`,
  and a `CreatedWith` sub-struct `{ Method, UserAgent, RemoteAddr string }` (camelCase
  via JSON tags) populated from `tok.Properties[keyCreatedWith]` when present. Rename
  `LastUsedAt` usage for sessions to also expose as `lastActiveAt` — check existing
  JSON tag `lastUsedAt`; the spec asks for `lastActiveAt` on sessions specifically, so
  either add a second field or alias — simplest: add `LastActiveAt *time.Time
  \`json:"lastActiveAt,omitempty"\`` alongside existing `LastUsedAt` (kept for PAT
  back-compat) and populate both from `tok.LastActiveAt` when building session rows
  (PAT rows can leave `LastActiveAt` empty and keep using `lastUsedAt`, or populate
  both — decide for minimal risk: populate both fields with the same value for all
  token types, additive, no breaking change to existing PAT consumers).
- `GetUserTokens`/`GetAllUserTokens` (service.go:1207-1277, 1467-1528): thread the
  caller's `refreshUid` (from `claims.RefreshUID`, passed down from the handler) into
  both functions so `IsCurrent = (tok.Type == TokenTypeRefresh && tok.UID ==
  callerRefreshUID)`. Handler methods `GetOrgTokens`/`GetAllUserTokens` (handler.go:225-260)
  already have `claims` in scope — pass `claims.RefreshUID` through.
- `DELETE /api/v1/auth/tokens/{uid}` (`RevokeToken`, service.go:1347-1376): already
  works for any token type owned by the user, including `refresh` — no change needed.
  Immediate revocation is already structural: `Refresh` looks up the row fresh via
  `GetUserTokenByToken` on every call, so a soft-deleted row 401s on the very next
  attempt (verify `GetUserTokenByToken`'s query excludes soft-deleted rows — check
  postgres.go/sqlite.go `WHERE deleted_at IS NULL` clause).
- **Logout — sign out other sessions**: `LogoutRequest` (handler.go:67-69) gains
  `SignOutOthers bool \`json:"signOutOthers"\`` alongside `DeleteAllTokens`. Reject
  (400 validation error) if both are true. New service method
  `LogoutOtherSessions(ctx, userUID, currentRefreshUID string) (*LogoutResponse, error)`:
  `ListUserTokensByType(ctx, userUID, TokenTypeRefresh)`, delete every row except
  `currentRefreshUID`, return `{Success: true, TokensDeleted: n}` (reuse
  `LogoutResponse`). Handler needs `claims.RefreshUID` to know which row to spare —
  if `claims.RefreshUID == ""` (e.g. a PAT hitting `/logout`, unusual but possible),
  reject with 400 (can't "sign out others" without a current session to keep).
- **Session cap**: in `completeLogin` (post-refresh-token-creation, service.go
  ~567-569) and `SwitchOrg` (post-creation, ~1443-1445) — after `CreateUserToken`
  succeeds, call a new helper `s.enforceSessionCap(ctx, userUID, maxSessions=10)`:
  `ListUserTokensByType(ctx, userUID, TokenTypeRefresh)`, sort by `LastActiveAt` (nil
  treated as `CreatedAt`) ascending, soft-delete every row beyond the newest 10 via
  `DeleteUserToken`. Best-effort (log on error, don't fail the login). Constant
  `const maxActiveSessions = 10` near the other auth constants.
  - Not applied in `GenerateTokensForOAuth` — check whether OAuth login also needs the
    cap (yes, it creates `refresh` rows too — hook the same helper there, and in
    `ConfirmRegistration`/`AcceptInvite` if they also mint refresh tokens at
    registration-completion; verify each call site: `service.go:1880` (AcceptInvite),
    `2631` (ConfirmRegistration or similar), `3123` (2FA completion) — cap must apply
    at every refresh-token-minting site, not just password login, or the cap is
    trivially bypassed via OAuth/invite/2FA logins).

### D5 — UI

- `web/dash0/src/api/hooks.ts`: add `SessionInfo` interface (`uid, orgSlug, createdAt,
  lastActiveAt, expiresAt, isCurrent, createdWith: {method, userAgent, remoteAddr}`),
  `useSessions(org)` hook hitting `GET /api/v1/orgs/{org}/tokens?type=refresh` (mirrors
  `useTokens`), `useRevokeSession()` (same DELETE endpoint, different query-key
  invalidation), `useSignOutOtherSessions()` mutation POSTing
  `/api/v1/auth/logout` with `{signOutOthers: true}`.
- New `web/dash0/src/lib/user-agent.ts`: tiny hand-rolled parser (regex-based, no
  dependency) exporting `parseUserAgent(ua: string): {browser?: string, browserVersion?:
  string, os?: string, device: "mobile"|"tablet"|"desktop"}`. Cover Chrome/Firefox/
  Safari/Edge on Windows/macOS/Linux/iOS/Android — good-enough coverage, not
  exhaustive (matches spec's "small utility, not a full parser").
- New route `web/dash0/src/routes/orgs/$org/account.sessions.tsx`: list via
  `useSessions`, sort `isCurrent` first then `lastActiveAt` desc (client-side sort,
  small N). Session card (reuse the `Card`/bordered-row pattern from
  `account.security.tsx`'s passkey list): device icon (`Smartphone`/`Tablet`/`Monitor`
  from lucide-react per `parseUserAgent().device`) + browser/OS text, raw UA in
  `text-xs text-muted-foreground font-mono`, `Badge variant="default"` + `border-primary`
  accent for `isCurrent`, a method `Badge variant="secondary"` (password/oauth/passkey),
  connected/last-active/expiry relative-time text (reuse `formatRelativeTime`/
  `formatExpiry`-style helpers, extract to a shared `lib/format-time.ts` if duplicating
  from `account.tokens.tsx` becomes ugly — otherwise just duplicate the two small
  functions, consistent with existing per-file duplication in this codebase), per-row
  `Trash2` destructive ghost icon button + `AlertDialog` confirm, "Sign out other
  sessions" `Button variant="destructive"` + `AlertDialog` naming the count (disabled
  when `sessions.length <= 1`). Revoking the current session's own row: detect
  `session.isCurrent` in the revoke handler and call `logout()` from `AuthContext`
  instead of just invalidating the query (matches "behaves as logout").
- `account.tsx` layout: add `{ label: t("nav:sessions"), path:
  "/orgs/$org/account/sessions" }` tab; relabel the tokens tab string if needed
  ("API tokens" per spec — check `nav:tokens` translation value and update en/fr).
- `web/dash0/src/locales/en/account.json` + `fr/account.json`: add a `sessions.*`
  namespace mirroring `tokens.*` (title, subtitle, empty state, revoke confirm,
  sign-out-others confirm with `{{count}}`, current-session label, connected/
  last-active/expiry labels, method labels). Add `nav:sessions` = "Sessions" (en) /
  "Sessions" (fr) to `nav.json`.
- `design-reference.tsx`: add a "Session card" example under `ButtonsBadgesSection`
  or a new small section near it — a static mock session row demonstrating the
  current-session accent border + badges + revoke button, with its import line.
- Verify `useTokens` (`hooks.ts:1192-1203`) keeps `?type=pat` — already correct, no
  change; add a one-line comment noting sessions must never be fetched through this
  hook.

### Testing plan

**Backend (table-driven, `sqlite.New(InMemory: true)` per existing helpers):**
- `service_test.go`: `TestRefreshSlidingExpiry` (refresh extends `expires_at`;
  granularity — two refreshes within the same hour only write once, assert via a
  spy/second read of `UpdatedAt` or by checking `expires_at` unchanged on the second
  call when forced within-hour), `TestRefreshUID` (login and refresh both produce
  claims with non-empty `RefreshUID`; PAT validation produces empty), `TestLogoutOthers`
  (creates N sessions, calls logout-others, asserts caller's row survives and count
  matches), `TestSessionCap` (11 logins, assert 10 rows remain, oldest-by-`LastActiveAt`
  pruned), `TestGetUserTokensTypeFilter` (mixed pat/refresh/oauth_refresh rows,
  `?type=refresh` returns only refresh rows with `isCurrent` correctly flagged).
- `handler_test.go`: cookie re-set assertion on `/auth/refresh` response.
- Add `strconv` import where needed for the OAuth `expires_in` param.

**Frontend:**
- `client.test.ts` (or new `token-refresh.test.ts`): mocked-fetch test firing N
  concurrent 401s, asserting exactly one `/auth/refresh` call and that all N original
  requests resolve successfully via retry.
- `AuthContext` or a small scheduler test: fake timers, assert the proactive check
  calls refresh only once the 1/3-lifetime threshold is crossed and not before.
- `live-socket.test.ts` (existing file — check for one): add a case for the 4401
  close code invoking the refresh helper before reconnecting.

**E2E (Playwright, author regardless of whether it can run against the shared
:4000 devloop — that server doesn't currently accept a custom
`SP_AUTH_ACCESS_TOKEN_EXPIRY`; if it can't be exercised locally, report
authored-but-not-run per the task instructions):**
- `e2e/session-continuity.spec.ts`: short-expiry scenario — proactive-timer path and
  reactive-401 path, asserting no redirect to login and the next action succeeds.
- `e2e/sessions.spec.ts`: list sessions, revoke another session, sign-out-others,
  assert current-session badge and count.

### Sequencing (commit-sized steps)

1. Backend: `refreshUid` claims + `generateAccessToken` signature change + all call
   sites (D3).
2. Backend: sliding expiry + granularity guard + cookie re-set on refresh (D2).
3. Backend: session cap helper + wire into every refresh-token mint site (D4 cap).
4. Backend: `TokenInfo` additions (`isCurrent`, `createdWith`, `lastActiveAt`) +
   thread `refreshUid` through `GetUserTokens`/`GetAllUserTokens` (D4 listing).
5. Backend: logout `signOutOthers` (D4).
6. Backend: OAuth redirect `expires_in` param, all 5 providers (D1 prerequisite).
7. Backend tests for 1-5.
8. Frontend: `client.ts` session storage (`setSession`/`getRefreshToken`/`getExpiresAt`)
   + single-flight `refreshAccessToken()` helper.
9. Frontend: wire `AuthContext` call sites to `setSession` (D1 bug fix) + proactive
   timer.
10. Frontend: `apiFetch` reactive 401 refresh-and-retry.
11. Frontend: `live-socket.ts` 4401 handling.
12. Frontend: `user-agent.ts` parser + `SessionInfo`/hooks in `hooks.ts`.
13. Frontend: `account.sessions.tsx` page + nav entry + translations +
    design-reference addition.
14. Frontend tests (single-flight, scheduling).
15. E2E specs (author; run if possible).
16. Final `make fmt` + full check pass + squash-fix commit.
