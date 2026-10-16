---
model: opus
effort: high
---

# Bearer validation accepts every JWT signed with the auth secret, so the passkey login-begin session and the 2FA temp token work as access tokens

## Problem
Found by the security scan of 2026-10-11 (findings C2 and H1, one root cause). **C2 is critical, H1 is high.**
- **C2, who:** anyone, unauthenticated, who knows the email of a user with at least one passkey. Every deployment mode: WebAuthn is on by default (`server/internal/config/config.go:2066-2068`). A super-admin victim means full instance compromise.
- **H1, who:** anyone with the victim's password. TOTP never gets asked.
- Both were reproduced end to end by the scan.

**Root cause.** `ValidateToken` (`server/internal/handlers/auth/service.go:1483-1507`) parses any HS256 JWT signed with `Auth.JWTSecret` into `Claims`. It checks the signature and the time claims, nothing else: no type, purpose, issuer or audience.
- **Every bearer surface trusts it:**
  - `RequireAuth` (`server/internal/middleware/auth.go:71`) and `RequireMCPAuth` (`:257`);
  - the OAuth consent `session()` (`server/internal/oauth/authorize.go:129`);
  - the realtime WS handshake (`server/internal/handlers/realtimews/handshake.go:107`);
  - feedback attribution (`server/internal/handlers/feedback/handler.go:136`).
- **Two non-bearer tokens are signed with the same key** and decode cleanly into `Claims`, because they use its JSON tags (`userUid`, `orgSlug`, `role`).

**Every JWT the server signs with `Auth.JWTSecret`** (all `jwt.NewWithClaims(jwt.SigningMethodHS256, …)` calls in `server/`):

| Token | Minted at | Payload | TTL | Bearer? |
|---|---|---|---|---|
| Session access token | `generateAccessToken`, `service.go:2227-2247` | `Claims`, `iss=solidping`, `jti` | `AccessTokenExpiry`, default 1 h (`config.go:2061`) | yes |
| Impersonation token | `generateImpersonationToken`, `impersonate.go:250-272` | `Claims` + `impersonatedBy`, `iss` | 30 min (`impersonate.go:24`) | yes, fenced by `impersonationDenial` |
| MCP OAuth access token | `GenerateMCPAccessToken`, `service.go:2268-2297`, called by `oauth/service.go:553` | `Claims` + `aud` = MCP resource + `scopes`, `iss` | 30 min (`oauth/service.go:32`) | at `/mcp` only, see spec 2026-10-16-12 |
| 2FA temp token | `generate2FATempToken`, `service.go:4453-4473` | `TwoFAClaims` (`:414-425`): `userUid`, `orgSlug`, `role`, `purpose=2fa`, `iss` | 5 min (`:4436`) | **no** |
| Passkey register session | `sealPasskeySession`, `passkey_session.go:39-65`, from `passkey_service.go:227` | `purpose=passkey-register`, `userUid`, `sd`; no `iss` | 5 min | **no** |
| Passkey login session | same helper, from `passkey_service.go:364` | `purpose=passkey-login`, `userUid` when the email has a passkey, `sd`; no `iss` | 5 min | **no** |

**Checked and not affected:**
- Refresh tokens, PATs and OAuth refresh grants are opaque random values looked up in `user_tokens`.
- The billing upgrade token (`server/internal/handlers/entitlements/handler.go:687-703`) is signed with the billing secret, not `JWTSecret`. It also carries `purpose=billing`.
- The other users of `JWTSecret` produce formats that can never parse as a three-segment JWT:
  - `incidentlinks.Sign` and unsubscribe tokens are `<payload>.<sig>`;
  - `files/signedurl` signs into query parameters;
  - `ovhsmscb` derives a token.

**C2 in detail.**
- `POST /api/v1/auth/passkeys/login/begin` is public (`server/internal/app/server.go:767`).
- `beginLoginCeremony` (`passkey_service.go:375-402`) puts the user's UID in the session when the submitted email has a passkey.
- The returned `session` then works as a Bearer token for that user:
  - `GET /auth/me` answers;
  - `POST /auth/switch-org` (`server.go:787`, `SwitchOrg` at `service.go:2086`) mints a real access and refresh pair for any org the user belongs to.
- `RequireSuperAdmin` reads `user.SuperAdmin` from the row (`middleware/auth.go:775-797`), so a super-admin victim also opens `/api/v1/system/*`.
- The token lives 5 min and can be re-minted at will.

**The register session has the same flaw.** `POST /passkeys/register/begin` (`server.go:805`) sits behind `RequireAuth` and returns a register session token, which `RequireAuth` then accepts. So one stolen access token can be chained into an endless series of 5-minute tokens. No row backs them, so they survive logout and password change.

**H1 in detail.**
- `Login` returns `tempToken` instead of a session when `user.TOTPEnabled` (`service.go:698-712`).
- The token carries the requested org and the user's role in it. Used as a bearer, it opens every org route and `switch-org`, so `/2fa/verify` is never needed.

## Proposal
1. **Separate keys for the non-bearer JWTs.**
   - Add `func (s *Service) purposeKey(purpose string) ([]byte, error)` in `server/internal/handlers/auth/`. It derives a 32-byte key with `hkdf.Key(sha256.New, []byte(s.fullCfg.Auth.JWTSecret), nil, "solidping-jwt:"+purpose, 32)`, the same stdlib pattern as `server/internal/authhandoff/authhandoff.go:199-216`.
   - Read `fullCfg` live: the overlay mutates it after `NewService` (`service.go:160-170`).
   - Sign and verify with it in two places:
     - `generate2FATempToken` and `validate2FATempToken` (`service.go:4453-4495`), with purpose `twoFAPurpose`;
     - `sealPasskeySession` and `unsealPasskeySession` (`passkey_session.go:39-104`), with the session's own purpose (`passkey-register` or `passkey-login`).
   - Access, impersonation and MCP tokens keep the raw secret. `ValidateToken` then cannot even verify the signature of a non-bearer token.
   - Add `jwt.WithValidMethods([]string{"HS256"})` to `unsealPasskeySession`, which today has no algorithm check (`:77-80`).
2. **Mandatory purpose check in `ValidateToken`** (`service.go:1483-1507`).
   - Parse into a local struct that embeds `Claims` and adds `Purpose string \`json:"purpose"\``. Return `ErrInvalidToken` when `Purpose != ""`.
   - This is what holds during a rolling deploy, while old replicas still sign 2FA and passkey tokens with the raw key.
   - Add the parser options `jwt.WithValidMethods([]string{"HS256"})`, `jwt.WithIssuer(jwtIssuer)` and `jwt.WithExpirationRequired()`.
     - Every bearer token sets `iss` and `exp`: `service.go:2236-2240` and `:2284-2288`, `impersonate.go:260-264`.
     - The passkey tokens set no `iss`, so the issuer option refuses them too.
3. **One signing helper.**
   - Route every `SignedString` in the package through one helper, e.g. `s.signJWT(purpose, claims)`, which picks the raw key only for `purpose == ""`. A future token type then cannot land on the bearer key by accident.
   - Document on `Claims` (`service.go:198`) that a token carrying `purpose` is never a bearer.
4. **The MCP token is left alone here.** It is a legitimate bearer at `/mcp`. Keeping it off REST is spec 2026-10-16-12, which keys on its `aud` claim, so the two specs can land in either order.

**Deploy impact.**
- No session breaks. Access, impersonation and MCP tokens keep the raw key, so every token already issued stays valid until its normal expiry.
- Only a 2FA or passkey ceremony in flight during the rollout can fail. That happens when begin and finish hit replicas on different versions, and the tokens last 5 min. The user sees "session expired" or "invalid code" once and retries.
- No frontend change: dash0 treats `tempToken` and `session` as opaque strings.

## Tests
- **`server/internal/handlers/auth/token_purpose_test.go`** (new, package `auth`, SQLite in-memory through `setupAuthTestService`, `service_test.go:23`). Table-driven over every non-bearer mint path. Each case asserts that `ValidateToken` returns `ErrInvalidToken`, and fails before the fix:
  - the 2FA temp token from `generate2FATempToken`, minted as `mintTwoFATempToken` does (`two_fa_attempt_throttle_test.go:153`);
  - the passkey login `session` from `BeginLogin`, for a user with a seeded passkey row (seed it as `passkey_membership_cover_test.go:126` does);
  - the passkey register `session` from `BeginRegistration`;
  - the legacy shapes an old replica mints during a rollout, signed with the raw secret: `TwoFAClaims` with `purpose=2fa`, and `passkeySessionClaims` with `purpose=passkey-login` and a `userUid`;
  - a `Claims` token signed with the raw secret but without `iss`, and one signed with HS512.
- **Positive controls in the same table:**
  - these validate: an access token from `generateAccessToken`, an impersonation token from `generateImpersonationToken`, and an MCP token from `GenerateMCPAccessToken`;
  - `validate2FATempToken` and `unsealPasskeySession` still accept their own tokens;
  - each refuses the other's token, and both refuse an access token.
- **`server/internal/middleware/token_purpose_test.go`** (new, with a real router as in `impersonation_test.go:74-160`). End to end through `RequireAuth`:
  - the passkey login session as Bearer on `GET /api/v1/auth/me` and on `POST /api/v1/auth/switch-org`: 401 `INVALID_TOKEN`;
  - a 2FA temp token on an org route: 401;
  - positive control: the session from a normal password login answers 200.
- **Must stay green unchanged:** `two_fa_attempt_throttle_test.go`, `passkey_service_test.go`, `impersonate_test.go`, `middleware/mcpauth_test.go`, and `app/demo_guard_route_table_test.go`. The latter's `mintTestToken` (`:103-125`) sets `iss`, so it keeps validating.

## To verify
- Nothing else parses a 2FA or passkey token. The known callers are `validate2FATempToken` at `service.go:4611` and `:4656`, and `unsealPasskeySession` at `passkey_service.go:245` and `:417`.
- Grep `server/` once more for `jwt.Parse` and `SignedString` after the change. Only the signing helper and `ValidateToken` should name the raw `JWTSecret` bytes for JWTs.

## Related
- **L3 (2026-10-12), not covered:** passkey login-begin still reveals which emails have a passkey (`allowCredentials`, `passkey_service.go:375-401`), and its signed (not encrypted) session exposes the user UID.
- **H3 (2026-10-11):** the MCP-token-on-REST half of the same "one key, no type" design, covered by spec 2026-10-16-12.
- **Spec 2026-10-16-14 (E1)** hands 2FA temp tokens to the browser after SSO, so it depends on this spec.
