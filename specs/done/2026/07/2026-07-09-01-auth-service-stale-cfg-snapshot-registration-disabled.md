# `auth.Service` holds a boot-time-frozen copy of `AuthConfig`, so system-parameter settings (registration email pattern, session max duration, …) never reach it — registration reports "on" but silently rejects everyone

## Problem

`auth.Service` is constructed with `cfg config.AuthConfig` passed **by value**
in `NewServer()`
([`server/internal/app/server.go:330`](../../server/internal/app/server.go)):

```go
authService := auth.NewService(dbService, cfg.Auth, cfg, jobService, entitlementsService)
```

`NewService`
([`server/internal/handlers/auth/service.go:377`](../../server/internal/handlers/auth/service.go))
stores **both** a frozen value copy and the live pointer:

```go
// Service struct — service.go:121-122
cfg          config.AuthConfig   // frozen snapshot taken at NewService time
fullCfg      *config.Config      // live pointer to the shared config
```

The problem is the boot ordering in
[`server/main.go:164-200`](../../server/main.go):

1. `app.NewServer(ctx, cfg)` (line 164) — constructs `auth.Service`, which
   **copies `cfg.Auth` by value into `s.cfg`**.
2. `server.Initialize(ctx)` (line 174) — DB / migrations.
3. `server.InitializeSystemConfig(ctx, ...)` (line 183) — the system-parameter
   overlay that applies `SP_AUTH_REGISTRATION_EMAIL_PATTERN` / the DB-stored
   `auth.registration_email_pattern` (and every other `auth.*` system
   parameter) **onto the shared `*config.Config`**
   ([`internal/systemconfig/systemconfig.go:336`](../../server/internal/systemconfig/systemconfig.go):
   `cfg.Auth.RegistrationEmailPattern = v`).
4. Routes are constructed **after** the overlay — deliberately, per the comment
   at [`main.go:200`](../../server/main.go) and
   [`server.go:391`](../../server/internal/app/server.go): *"Routes are
   constructed after InitializeSystemConfig so handlers see the post-overlay
   [config]."*

The overlay in step 3 mutates the `*config.Config` that `s.fullCfg` points at,
but `s.cfg` was **copied in step 1, before the overlay ran** — so `s.cfg` is
frozen at its pre-overlay (usually empty/default) value forever. A full server
restart doesn't help: the ordering is baked into `main.go`'s boot sequence
every time.

### The visible symptom: registration reports enabled but 403s

`Service.Register()` reads the **frozen** copy
([`service.go:1987`](../../server/internal/handlers/auth/service.go)):

```go
pattern := s.cfg.RegistrationEmailPattern
if pattern == "" {
    return ErrRegistrationDisabled
}
```

Meanwhile `auth.Handler` reads the **same setting live and correctly**
([`providers_available.go:129`](../../server/internal/handlers/auth/providers_available.go)):

```go
RegistrationEnabled: h.cfg.Auth.RegistrationEmailPattern != "",
```

`h.cfg` is `*config.Config` (a pointer), and the handler was constructed
post-overlay, so it sees the applied value.

**Reproduced live:** with `SP_AUTH_REGISTRATION_EMAIL_PATTERN=.*` set at boot,
`GET /api/v1/auth/providers` returns `"registrationEnabled": true`, but
`POST /api/v1/auth/register` still 403s with `REGISTRATION_DISABLED`.
Self-hosted operators who enable registration via the system-parameter API/UI
(or the env var) see the toggle report "on" while registration silently keeps
rejecting everyone. This is the same gap already noted as follow-up finding #1
in
[`2026-07-08-09-create-org-missing-org-scoped-access-token.md`](../done/2026/07/2026-07-08-09-create-org-missing-org-scoped-access-token.md)
(the committed create-org e2e test's register step gracefully skips because of
exactly this bug).

### This is not the only affected field — audit before fixing

The overlay applies **many** `auth.*` system parameters
([`systemconfig.go`](../../server/internal/systemconfig/systemconfig.go)):
`auth.jwt_secret`, `auth.registration_email_pattern`,
`auth.session_max_duration`, all the OAuth provider client-id/secret/enabled
keys, and the password-hashing policy. Any field that `Service` reads from the
frozen `s.cfg` **and** the overlay can mutate has the same latent bug. A
blanket "switch every `s.cfg.X` to `s.fullCfg.Auth.X`" is wrong — some fields
are legitimately boot-time-only. The reads must be classified:

| `s.cfg.X` read | Overlaid by systemconfig? | Verdict |
|---|---|---|
| `RegistrationEmailPattern` (`service.go:1987`) | **Yes** (`KeyRegistrationEmailPattern`) | **BUG — must track live `s.fullCfg.Auth.RegistrationEmailPattern`** |
| `SessionMaxDuration` (`service.go:1034`, in `resolveSessionMaxDuration`) | **Yes** (`KeySessionMaxDuration`) | **BUG — second live instance** (see below) |
| `JWTSecret` (`service.go:1194,1830,1863,3215,3225`) | **Yes** (`KeyJWTSecret`, and **auto-generated** post-`NewService` by `ensureJWTSecret`, `systemconfig.go:1102-1139`) | **Needs careful judgment** (see below) |
| `AccessTokenExpiry` (many sites) | No (not in the overlay key list) | Legitimately boot-time-only — leave on `s.cfg` |
| `RefreshTokenExpiry` (`service.go:1064`) | No | Legitimately boot-time-only — leave on `s.cfg` |

**`SessionMaxDuration` is a confirmed second instance.**
`resolveSessionMaxDuration` reads `s.cfg.SessionMaxDuration`
([`service.go:1034`](../../server/internal/handlers/auth/service.go)), and its
own doc-comment (lines 1009-1018) *claims* the value is
*"already overlaid onto s.cfg (config.AuthConfig) at startup …editable at
runtime via PUT /api/v1/system/parameters, effective on restart"* — but that's
exactly the false assumption at the root of this bug. The overlay writes
`cfg.Auth.SessionMaxDuration` on the shared pointer
([`systemconfig.go:349`](../../server/internal/systemconfig/systemconfig.go)),
never the frozen `s.cfg`. So a system-wide `auth.session_max_duration` set via
env or DB param is silently ignored (the per-**org** override path still works,
because it does a fresh `s.db.GetOrgParameter` read — only the system-wide
fallback is stale). The misleading comment must be corrected as part of the
fix.

**`JWTSecret` needs a deliberate decision, not a reflexive switch.** It is
overlaid by `KeyJWTSecret` **and** auto-generated at startup by `ensureJWTSecret`
([`systemconfig.go:1102-1139`](../../server/internal/systemconfig/systemconfig.go)),
which writes the generated/DB secret onto `s.config.Auth.JWTSecret` (the live
pointer) *after* `NewService` already froze `s.cfg.JWTSecret`. The `Service`
both signs and validates with `s.cfg.JWTSecret`, so it is *internally*
self-consistent (tokens it mints validate against itself) — but it may be
signing with a **different** secret than the rest of the system believes is
active (e.g. the auto-generated one now stored in the DB and in `fullCfg`).
Determine whether any other component validates tokens against
`fullCfg.Auth.JWTSecret` (check the oauth service, constructed with the live
`s.config` at [`server.go:582`](../../server/internal/app/server.go), and the
auth middleware). If so, this is a latent token-mismatch bug and `Service`
should read the live secret; if the `Service` is the sole authority for its own
tokens it may be acceptable, but the divergence should at minimum be documented.

## Proposal

1. **Fix `Register()`** ([`service.go:1987`](../../server/internal/handlers/auth/service.go)):
   read `s.fullCfg.Auth.RegistrationEmailPattern` instead of
   `s.cfg.RegistrationEmailPattern`, matching how `Handler` reads it live.
   Audit the whole `Register()` body for any other stale `s.cfg.*` read that
   should be live (e.g. any email-allow-pattern / auto-join logic).

2. **Fix `resolveSessionMaxDuration`** ([`service.go:1034`](../../server/internal/handlers/auth/service.go)):
   read `s.fullCfg.Auth.SessionMaxDuration` for the system-wide fallback, and
   correct the doc-comment (lines 1009-1018) so it no longer claims the frozen
   `s.cfg` reflects runtime system parameters.

3. **Complete the audit** of every remaining `s.cfg.X` read in `service.go`
   against the systemconfig overlay key list. For each: if the overlay can
   mutate the field, switch it to `s.fullCfg.Auth.X`; if it is genuinely
   boot-time-only (`AccessTokenExpiry`, `RefreshTokenExpiry`, and anything not
   in the overlay), leave it on `s.cfg` and — ideally — make that intent
   obvious. **Do not blanket-switch every read.**

4. **Resolve the `JWTSecret` question** per the analysis above: either switch
   `Service` to the live secret (if any other component validates against
   `fullCfg`), or document why the frozen snapshot is intentional and safe.

5. **Consider the structural fix.** The cleanest option is to stop keeping a
   value snapshot of live-reconfigurable fields at all — e.g. have `Service`
   read everything reconfigurable through `s.fullCfg.Auth`, and keep `s.cfg`
   only for the truly-frozen fields (or drop it entirely and add small accessor
   helpers so the boot-time-only vs. live distinction is explicit and
   greppable). Weigh this against the blast radius of touching every read site;
   a targeted per-field fix (steps 1-4) is acceptable if the structural change
   is too broad for this spec.

## Acceptance criteria

- **Regression test that mirrors how this bug slipped through:** boot the
  server with `auth.registration_email_pattern` applied **only via the
  systemconfig overlay path** (i.e. set through `InitializeSystemConfig` / a
  system parameter, **not** baked into the `config.Config` passed to
  `NewServer`), then assert `POST /api/v1/auth/register` **succeeds** (2xx),
  not 403 `REGISTRATION_DISABLED`. A test that passes the pattern into config
  pre-`NewServer` would **not** catch this bug and is insufficient.
- A test asserting a system-wide `auth.session_max_duration` set via the
  overlay is actually honored by `resolveSessionMaxDuration` (with no org
  override present).
- `GET /api/v1/auth/providers` `registrationEnabled` and the actual
  `POST /api/v1/auth/register` behavior agree in all three states: pattern
  unset (both off), pattern set via env at boot, pattern set via DB system
  parameter at runtime+restart.
- The audit table is reflected in the diff: every overlaid `auth.*` field the
  `Service` reads tracks the live config, every boot-time-only field stays
  frozen, and the misleading `resolveSessionMaxDuration` comment is fixed.
- `make build lint test` green.

## Notes / open questions

- Whether to also cover the OAuth provider fields (`auth.google.*`, etc.) that
  the overlay applies — `Service` may or may not read those from `s.cfg`; the
  audit will reveal which are affected. The `Handler` already reads all of them
  live via `h.cfg`, so the provider-availability endpoint is fine; the question
  is only whether any `Service` code path re-reads a provider field from the
  frozen snapshot.
- The `JWTSecret` decision (step 4) is the one place a wrong call could
  *introduce* a token-invalidation bug, so it warrants an explicit note in the
  implementation findings rather than a silent change.

## Implementation Plan

### Audit result (auth `Service`, `s.cfg config.AuthConfig` frozen snapshot)

`AuthConfig` only holds: `JWTSecret`, `AccessTokenExpiry`, `RefreshTokenExpiry`,
`RegistrationEmailPattern`, `SessionMaxDuration`, `WebAuthn`, `Password`. The
OAuth **provider** config (`Google`, `GitHub`, `GitLab`, `Microsoft`, `Slack`,
`Discord`, `OIDC`, `SAML`, `LDAP`) lives at the **top level** of
`config.Config`, not inside `AuthConfig`, and the provider services
(`GitLabOAuthService`, `OIDCOAuthService`, …) each hold their own
`cfg *config.Config` **live pointer** — so no provider field is ever read from
the auth `Service`'s frozen snapshot. The Notes' open question resolves to: no
`Service` provider re-read is affected.

Every `s.cfg.X` read on the auth `Service` (`service.go` + `passkey_session.go`):

| Field (sites) | Overlaid by systemconfig? | Verdict |
|---|---|---|
| `RegistrationEmailPattern` (service.go:2048) | Yes (`KeyRegistrationEmailPattern`) | **switch → `s.fullCfg.Auth`** |
| `SessionMaxDuration` (service.go:1034) | Yes (`KeySessionMaxDuration`) | **switch → `s.fullCfg.Auth`** |
| `JWTSecret` (service.go:1194,1891,1924,3276,3286; passkey_session.go:59,79) | Yes (`KeyJWTSecret` + auto-gen `ensureJWTSecret`) | **switch → `s.fullCfg.Auth`** (see decision) |
| `AccessTokenExpiry` (577,618,1164,1800,1881,2005,2238,2677,3042,3533,3576) | No | leave frozen |
| `RefreshTokenExpiry` (1064) | No | leave frozen |

### JWTSecret decision — switch to live

- The auth `Service` both **signs and validates** its own session/2FA/MCP/passkey
  tokens with the same secret, so it is internally consistent either way.
- The auth **middleware** (`middleware/auth.go`) and the **OAuth service**
  (`oauth/*`) do **not** read a secret directly — they delegate to
  `authService.ValidateToken` / `authService.GenerateMCPAccessToken`, so they
  already follow whatever the Service uses (no direct mismatch there).
- BUT the JWT secret is a **system-wide** shared secret: `unsubscribe`
  (server.go:791), `feedback`/`files` signed URLs, `incidents` magic links, and
  `email` links all read the **live** `cfg.Auth.JWTSecret`. And `ensureJWTSecret`
  auto-generates a secret onto the **live** `fullCfg` *after* `s.cfg` is frozen —
  so in the common self-hosted case the Service signs session JWTs with the
  **empty/default** frozen secret while the rest of the system uses the
  auto-generated live one. server.go:2128 even logs "auth service will use new
  secret on restart" — but the boot order makes that never true.
- Decision: **switch Service JWTSecret reads (sign AND validate together) to the
  live `s.fullCfg.Auth.JWTSecret`.** This eliminates the empty-secret signing
  weakness and aligns the Service with every other subsystem. Sign+validate move
  together so no token is invalidated mid-run.

### Steps
1. `Register()` → live `RegistrationEmailPattern`.
2. `resolveSessionMaxDuration` → live `SessionMaxDuration`; fix the misleading
   doc-comment.
3. Switch all 7 `JWTSecret` sign/validate sites (service.go + passkey_session.go)
   to live; leave `AccessTokenExpiry`/`RefreshTokenExpiry` frozen with a
   clarifying comment on the struct field.
4. Test seam: `setupAuthTestService` and the two `realtimews` sites pass `nil`
   fullCfg — give them a real `*config.Config` so live reads don't panic. Update
   `session_max_duration_test.go` to mutate `fullCfg.Auth` for the system-wide
   value.
5. New regression tests: registration enabled **only via the overlay path**
   (mutate `fullCfg` after construction) → `Register` succeeds; session cap set
   via overlay is honored; providers-vs-register agreement across states.
