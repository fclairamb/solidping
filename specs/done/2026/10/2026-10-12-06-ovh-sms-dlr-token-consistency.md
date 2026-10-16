---
model: sonnet
effort: medium
---

# The OVH SMS delivery-receipt URL registered with OVH must carry the token and base URL the handler accepts

## Problem
**Confirmed.** On any install that does not set `SP_SMS_OVH_DLR_TOKEN` (or `auth.jwt_secret` in the YAML file), the callback URL registered with OVH and the token the delivery-receipt handler checks are different. Every OVH delivery receipt is rejected with a 404, on every boot. The registered URL also points at `http://localhost:4000` unless the base URL is in the YAML file. The mismatch is stable across restarts: it is not a token that changes, it is two tokens that never agree.

How the pieces fit:

- **No per-send callback.** OVH's job body has no callback field (`server/internal/integrations/ovhsms/client.go:205-224`). Receipts go to the SMS account's service-level `callBack`, set once by `PUT /sms/{serviceName}` (`SetDeliveryCallback`, `client.go:253-256`), through `smssvc.ConfigureOVHDeliveryCallback` (`server/internal/integrations/sms/sender.go:283-295`). It is a no-op unless the instance sender is OVH and the URL is non-empty.
- **Token derivation.** `ovhsmscb.ResolveToken` (`server/internal/handlers/ovhsmscb/handler.go:55-69`) returns the trimmed `cfg.SMS.OVH.DLRToken` when set, else `hex(HMAC-SHA256(key=cfg.Auth.JWTSecret, "solidping:ovhsms:dlr"))`, else `""`. `CallbackURL` (`handler.go:73-79`) is `<baseURL>/api/v1/integrations/ovhsms/dlr/<token>`. The handler compares in constant time (`tokenMatches`, `handler.go:127-133`, `hmac.Equal`) and answers a bare 404 on mismatch (`handler.go:91-94`, pinned by `TestWrongTokenIs404`).
- **Registration happens too early.** `NewServer` registers the callback at `server/internal/app/server.go:516-526` with `ovhsmscb.CallbackURL(cfg.Server.BaseURL, ovhsmscb.ResolveToken(cfg))`. The handler is built later, in `SetupRoutes`, at `server.go:2290` with `ovhsmscb.ResolveToken(s.config)`. Between the two, `main.go` runs `Initialize` (`main.go:312`) and `InitializeSystemConfig` (`main.go:321`), which rewrites both inputs on the same `cfg` pointer.
- **Where the JWT secret comes from.** The default is the literal `"change-me-in-production"` (`server/internal/config/config.go:2015`). `SP_AUTH_JWT_SECRET` is **not** read by `config.Load`: the koanf env transform turns it into `auth.jwt.secret` (`config.go:2184-2199`), which does not match the `jwt_secret` tag, and there is no manual reader. It is applied only by the system-config overlay (`server/internal/systemconfig/systemconfig.go:315-323`, run from `Service.Initialize`, `:1499-1548`). With no env value, `ensureJWTSecret` (`systemconfig.go:1551-1590`) treats the default as unset, loads `auth.jwt_secret` from the database or generates 32 random bytes and stores it (`:1583`). So at `NewServer` time the secret is always the public default, unless the YAML file sets it.
- **Base URL, same story.** The default is `config.DefaultBaseURL = "http://localhost:4000"` (`server/internal/config/discord_oauth.go:38`, used at `config.go:1931`). The documented `SP_BASE_URL` and the DB parameter `server.base_url` are applied only by the overlay (`systemconfig.go:364-372`). `SP_SERVER_BASE_URL` does not reach `cfg.Server.BaseURL` either (koanf path `server.base.url`).
- **Spec 2026-10-12-03 did not change this.** It moved the VAPID keys into `Initialize` (`server.go:4233-4272`) and left the OVH block in `NewServer`. Its own notes flagged this bug.

Proof, run on 2026-10-12 against `origin/main` with a throwaway test (via `go test -overlay`, nothing committed) that calls `config.Load`, takes `ResolveToken` (what `NewServer` registers), runs `systemconfig.NewService(db, cfg).Initialize` on an in-memory SQLite, then takes `ResolveToken` again (what `SetupRoutes` checks):

| Environment | JWT secret seen by `NewServer` | URL token | Handler token | Match |
|---|---|---|---|---|
| nothing set | `change-me-in-production` | `ff757c366ba6…` | `1e8e05155537…` (random, from DB) | no |
| `SP_AUTH_JWT_SECRET=…` | `change-me-in-production` | `ff757c366ba6…` | `e1b35dfbddba…` | no |
| `SP_SMS_OVH_DLR_TOKEN=…` | `change-me-in-production` | explicit | explicit | yes |

Same test with `SP_BASE_URL=https://status.acme.com`: base URL is `http://localhost:4000` before the overlay and `https://status.acme.com` after.

Two consequences beyond "no receipts":
- The URL token on every such install is `HMAC("change-me-in-production", …)`, a value anyone can compute. Today the handler rejects it, so it is not an open door. But no fix may ever make the handler accept a token derived from the default secret.
- The docs say a derived token is the easy path (`web/docs/docs/configuration/sms.md:108`). In practice only the explicit token works.

How it was observed: code reading during the 2026-10-12-03 VAPID work, then the proof above. No production OVH account was exercised.

## Proposal
1. **Register after the overlay.** Remove the `ConfigureOVHDeliveryCallback` call from `NewServer` (`server.go:516-526`). Keep the instance sender on `Server` (new field, e.g. `instanceSMSSender smssvc.Sender`, set where `server` is built at `server.go:~541`). Add `(s *Server) configureOVHDeliveryCallback(ctx)` and call it at the end of `InitializeSystemConfig`, next to `logMetricsScrapeTokenState` (`server.go:4302`). That is the point where `cfg.Auth.JWTSecret` and `cfg.Server.BaseURL` are final, the same contract as `installEgressGuard`. Keep it best-effort: a failure is one WARN, never a boot failure (the current comment at `server.go:516-520` stays true).
2. **One place for the token.** Add `(s *Server) ovhDLRToken() string` that returns `ovhsmscb.ResolveToken(s.config)`. Use it in both `configureOVHDeliveryCallback` and the handler construction at `server.go:2290`. Do not cache it with `sync.Once`: a test harness that skips `InitializeSystemConfig` would freeze a pre-overlay value. Both call sites run after the overlay in `prepareServer` (`main.go:298-341`), so a plain call is enough.
3. **Never derive from the public default.** Export the default as `config.DefaultJWTSecret = "change-me-in-production"`. Use it at `config.go:2015` and `systemconfig.go:1558`. In `ResolveToken`, return `""` when the trimmed secret equals it. Effect: if anything ever runs before the overlay again, the endpoint is disabled and nothing is registered, instead of registering a guessable URL.
4. **Do not register an unreachable URL.** In `configureOVHDeliveryCallback`, skip the `PUT` when the base URL is still `config.DefaultBaseURL` or its host is loopback. Log one WARN telling the operator to set `SP_BASE_URL`. OVH cannot call `localhost`, and overwriting a correct `callBack` that an operator set by hand in the OVH panel with a localhost URL is worse than doing nothing.
5. **One startup line.** When the instance sender is OVH, log exactly one INFO line after the overlay, never the token: `ovh sms delivery receipts enabled` with `tokenSource` (`explicit` | `derived`) and `callbackRegistered` (bool), or a WARN naming why they are off (no token, default base URL, OVH API error). No line when the provider is not OVH. Model it on `logMetricsScrapeTokenState` (`server/internal/app/metrics_auth.go:89-110`).
6. **Keep the handler contract.** Constant-time compare and bare 404 on a wrong token stay as they are (`handler.go:84-94,127-133`). Do not switch to 401/403: the existing tests and the package doc deliberately refuse to confirm the endpoint exists.
7. **Stable across restarts.** Nothing extra to build: the derived token comes from the DB-persisted secret, so it is the same on every boot once the ordering is fixed. Re-registering on every boot is idempotent and also fixes a rotation: rotating the JWT secret or `SP_SMS_OVH_DLR_TOKEN` rotates the URL, and the next boot pushes the new one.
8. **Docs.**
   - `web/docs/docs/configuration/sms.md:108`: say the derived token works without configuration, that SolidPing registers `{SP_BASE_URL}/api/v1/integrations/ovhsms/dlr/{token}` as the account's `callBack` at each boot, that this needs a public `SP_BASE_URL`, and that an operator who wants to set `callBack` by hand in the OVH panel should set `SP_SMS_OVH_DLR_TOKEN` explicitly so they know the token.
   - `web/docs/docs/configuration/authentication.md:34` says an unset `SP_AUTH_JWT_SECRET` means "a random secret is generated on each restart". That is wrong: it is generated once and stored in the database (`systemconfig.go:1578-1587`). Fix the sentence, since the DLR token now depends on that stability.

Backward compatibility: installs with `SP_SMS_OVH_DLR_TOKEN` keep the same URL and token. Installs on the derived token have a broken `callBack` today; the first boot after this change overwrites it with the correct URL. No receipt that works today stops working. Nothing changes for Twilio or for orgs with their own SMS account.

## Tests
- `server/internal/handlers/ovhsmscb/handler_test.go`, new table-driven `TestHandleDLRTokenSources`. Each case builds a `config.Config`, gets the token with `ResolveToken`, builds the handler with `setupDLR`, then calls the route with a candidate token:
  - explicit `SMS.OVH.DLRToken`, request with it: 204, delivery recorded.
  - explicit token set and a JWT secret set, request with the JWT-derived token: 404, explicit wins.
  - derived from a random JWT secret, request with the derived token: 204.
  - derived, request with a token derived from another secret: 404, empty body, nothing written.
  - JWT secret equal to `config.DefaultJWTSecret`, no explicit token: `ResolveToken` is `""`, request with `HMAC(default)` (`ff757c36…`): 404.
  - explicit token with surrounding spaces: trimmed, request with the trimmed value: 204.
- `TestResolveToken` (`handler_test.go:232`): add the default-secret case returning `""`, and a padded default (`"  change-me-in-production "`) also returning `""`.
- `server/internal/app/ovh_dlr_callback_test.go` (new), app-level, modelled on `egress_guard_test.go`. A fake OVH API on `httptest.NewServer` answers `GET /auth/time` and records every `PUT /sms/sms-acme-1` body. Config: provider `ovh`, `SMS.OVH.BaseURL` pointing at the fake, no JWT secret in YAML terms (the default), no DLR token. Table cases:
  - **fresh DB, derived token.** Store `server.base_url = https://status.acme.com` as a system parameter (or `t.Setenv("SP_BASE_URL", …)`, no `t.Parallel()` then). Run `NewServer`: zero PUTs. Run `Initialize`, `InitializeSystemConfig`, `SetupRoutes`: exactly one PUT. Its `callBack` starts with `https://status.acme.com/api/v1/integrations/ovhsms/dlr/`. Take the token from it and `GET` that path with `?id=424242&ptt=4` on `server.Handler()`: 204. The token is not `HMAC(config.DefaultJWTSecret)`.
  - **restart.** Same SQLite file (in `t.TempDir()`), a second full startup: the second PUT carries the same `callBack` as the first.
  - **explicit token.** `SMS.OVH.DLRToken` set: the `callBack` ends with it, and the handler accepts it.
  - **default base URL.** Nothing sets the base URL: zero PUTs, and the handler still answers its own token with 204 (receipts can arrive if the operator registered the URL by hand).
  - **not OVH.** Provider `twilio` or none: zero calls to the fake.
- Optional: capture the logs with a test `slog` handler and assert one line per boot, and that the token never appears in any log record.

## To verify
- That nothing else between `NewServer` and `InitializeSystemConfig` reads `ResolveToken` or the OVH callback. `grep -rn "ResolveToken\|ConfigureOVHDeliveryCallback"` showed only `server.go:522` and `server.go:2290` on 2026-10-12.
- Which processes run `InitializeSystemConfig`. `prepareServer` (`main.go:297-345`) is shared by `serve` and `mcp --stdio`, so `mcp --stdio` would also `PUT` the callback. Decide whether that is fine (same idempotent value) or whether to register only when `s.servesAPI()` is true. A worker-only node registering the same URL is harmless.
- That a file-backed SQLite in `t.TempDir()` is supported by `sqlite.Config` for the restart case. If not, use the embedded Postgres harness or drop the restart case and rely on `ensureJWTSecret` reading the stored secret.
- Whether `ConfigureOVHDeliveryCallback` should first `GET /sms/{serviceName}` and skip the `PUT` when `callBack` already matches. Not required for correctness. Only worth it if OVH rate-limits or audits that call.
- Outside this spec, worth a line in the PR: `job_startup.go:425` reads `SP_SERVER_BASE_URL` directly, and `envcheck.go:179` lists it as known, but it never sets `cfg.Server.BaseURL`. The documented variable is `SP_BASE_URL`.

## Acceptance Criteria
- [ ] On a fresh database with only `SP_BASE_URL` and the OVH credentials set, the `callBack` URL registered with OVH is accepted by the handler (app-level test).
- [ ] No OVH `PUT` happens before `InitializeSystemConfig`.
- [ ] `ResolveToken` never derives from `change-me-in-production`.
- [ ] No registration while the base URL is the localhost default.
- [ ] One startup log line about OVH delivery receipts, no token in it.
- [ ] `sms.md` and `authentication.md` corrected.
