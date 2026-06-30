# Password hashing in Server Settings — editable algorithm/cost UI with optional rehash

## Context

The password-hashing policy is already **config-driven and hot-swappable** in the
backend (shipped via `specs/done/.../2026-06-29-03-configurable-password-hashing-and-rehash.md`,
now implemented):

- `passwords.Policy` + `PolicyFromConfig(&cfg.Auth)` resolve algorithm + cost from
  `config.AuthConfig.Password` (`server/internal/utils/passwords/policy.go:26-110`).
- `passwords.SetDefaultPolicy(p)` installs a **process-wide, mutex-guarded** policy
  (`policy.go:55-70`) consulted by `Hash`/`NeedsRehash`; `Verify` dispatches purely
  on the stored `$algo$` marker.
- A best-effort login rehash hook `maybeRehashPassword`
  (`auth/service.go:465-488`) re-mints a user's hash on successful login when it no
  longer matches the active policy (`NeedsRehash`, `service.go:475`).

What's missing is an **operator-facing way to change it without editing YAML/env**.
Everything else server-wide is editable from the **Server Settings** UI
(`/orgs/$org/server/*`, super-admin only — `server.tsx:30`), which writes
**system parameters** (DB-backed key/value, `organization_uid IS NULL`) through a
declarative registry:

- **Registry**: `server/internal/systemconfig/systemconfig.go` — a catalog of
  `ParameterDefinition{Key, EnvVar, Secret, ApplyFunc(cfg, value)}`
  (`systemconfig.go:23-78`). At startup `Service.Initialize(ctx)` overlays DB values
  onto `cfg` with precedence **env > DB > default**, calling each `ApplyFunc`.
- **API**: `GET /api/v1/system/parameters`, `PUT /api/v1/system/parameters/:key`
  (`{value, secret}`), super-admin-gated (`server.go:856-862`,
  `RequireSuperAdmin` at `middleware/auth.go:342-359`). Secrets masked on read
  (`handlers/system/service.go:221-237`).
- **Frontend**: `useSystemParameters()` / `useSetSystemParameter()`
  (`web/dash0/src/api/hooks.ts:2088-2127`); `server.auth.tsx` is the canonical page
  (per-key dotted settings, dirty tracking, save, "restart to apply" copy). Tabs are
  a flat array in `server.tsx:16-24`.

This spec adds a **"Password Hashing"** tab that edits `auth.password.*` system
parameters, plus an **optional rehash** toggle, following the established
restart-to-apply convention used by every other credential/perf setting here
(i18n already says *"Credential changes take effect after a server restart"* —
`en/server.json:68`).

## The key questions

### Q1 — When does a change take effect? **On restart** (decided)

Matches every other server setting (OAuth, performance, Slack all surface a
"takes effect after restart" hint). Mechanically: the UI persists `auth.password.*`
system parameters; at next boot `systemconfig.Initialize` overlays them into
`cfg.Auth.Password.*`; the policy is then resolved + installed.

**But there is an ordering bug to fix** (see Q2). Live hot-swap is an explicit
non-goal for v1 (the infra — `SetDefaultPolicy` — supports it, noted as a
follow-up).

### Q2 — Why the current startup order doesn't apply DB values

`passwords.PolicyFromConfig` + `SetDefaultPolicy` run **inside `NewServer` at
`server.go:165-169`**, before the DB is up. The system-parameter overlay runs
**later**, in `InitializeSystemConfig` (`server.go:1991-1994`,
`sysConfigSvc.Initialize(ctx)`). So a policy resolved at `:165` reflects only
YAML/env — **DB-stored `auth.password.*` would be ignored even across a restart.**

**Decision:** after `sysConfigSvc.Initialize(ctx)` mutates `cfg`, **re-resolve and
re-install** the policy:

```go
// end of InitializeSystemConfig, after the overlay has mutated cfg
if pol, err := passwords.PolicyFromConfig(&cfg.Auth); err != nil {
    slog.WarnContext(ctx, "password policy from system params invalid; keeping prior policy", "error", err)
} else {
    passwords.SetDefaultPolicy(pol)
}
```

This re-install is **best-effort / non-fatal** (keep the `:165` policy on error) —
deliberately *unlike* the fatal `:165` path — so a bad DB value can never brick
startup (see Risk log). Keep the `:165` install as the early default for any
hashing that happens before the overlay.

### Q3 — What does "optional rehashing on parameter change" mean?

The only rehash mechanism is the lazy login-time `maybeRehashPassword` (we only
hold the plaintext at login). "Optional" = a stored toggle that gates it:

- New `auth.password.rehash_on_login` (bool, **default true** — preserves current
  behavior and delivers migration).
- Surfaced as a Switch in the UI: *"Re-hash existing passwords on next sign-in."*
- Gates the hook: replace the bare `NeedsRehash` at `service.go:475` with
  `passwords.ShouldRehash(*user.PasswordHash)` = `rehashOnLogin && NeedsRehash(...)`,
  where `rehashOnLogin` lives on the resolved `Policy` (single source of truth,
  hot-swapped with the rest of the policy).

When **off**: existing hashes are left alone after a param change; only *new*
passwords (new users, password changes/resets) use the new profile.

### Q4 — Validation must happen at write time, not just at boot

`config.Config.Validate()` already validates `auth.password.*` at load and (per
spec 03) **fails fast/aborts** on bad values. The system `SetParameter` handler
**does no validation today** (`handlers/system/service.go`, accepts any JSON). So a
bad value saved from the UI would persist and then **abort the next startup**.

**Decision:** validate `auth.password.*` in `SetParameter` (reuse the exact bounds
from `config.Validate`): `algorithm ∈ {argon2id, bcrypt}`; argon2 `memory ≥ 8192`
KiB, `time ≥ 1`, `threads ∈ [1,255]`, `key_length ≥ 16`, `salt_length ≥ 8`; bcrypt
`cost ∈ [10,31]`; `rehash_on_login` boolean. Reject with `422 VALIDATION_ERROR`.
The non-fatal re-resolve (Q2) is the belt-and-suspenders for any value that slips
through (e.g. set via the raw API).

## Goal

A super-admin opens **Server Settings → Password Hashing**, picks the algorithm
(argon2id / bcrypt) and its cost parameters, optionally toggles "re-hash on next
sign-in", and saves. Values are validated immediately, persisted as system
parameters, and take effect **after a server restart** — at which point new hashes
use the new profile and (if the toggle is on) existing users migrate transparently
on their next login. Bad input is rejected at save; a malformed stored value can
never prevent startup.

## Non-goals

- **Live hot-swap without restart.** `SetDefaultPolicy` could support it via a
  write hook, but v1 matches the restart-to-apply convention. Follow-up.
- **Per-org / per-user hashing policy.** One process-wide policy.
- **A bulk/offline re-hash job.** Rehash stays lazy-at-login (only point with the
  plaintext); unchanged from spec 03.
- **New algorithms** (scrypt/pbkdf2). The select offers only the two implemented.
- **Re-hashing `$plaintext$` dev hashes.** Left as-is.
- **Exposing secrets.** None of these keys are secret (`Secret: false`) — no
  masking, simpler than `server.auth.tsx`.

## Design

### Backend

**1. systemconfig registry** (`systemconfig.go`) — add keys + definitions:

```go
KeyPasswordAlgorithm     ParameterKey = "auth.password.algorithm"
KeyPasswordArgon2Memory  ParameterKey = "auth.password.argon2.memory"
KeyPasswordArgon2Time    ParameterKey = "auth.password.argon2.time"
KeyPasswordArgon2Threads ParameterKey = "auth.password.argon2.threads"
KeyPasswordArgon2KeyLen  ParameterKey = "auth.password.argon2.key_length"
KeyPasswordArgon2SaltLen ParameterKey = "auth.password.argon2.salt_length"
KeyPasswordBcryptCost    ParameterKey = "auth.password.bcrypt.cost"
KeyPasswordRehashOnLogin ParameterKey = "auth.password.rehash_on_login"
```

Each gets a `ParameterDefinition` in `getKnownParameters()` with `Secret:false`,
`EnvVar:"SP_AUTH_PASSWORD_*"`, and an `ApplyFunc` mutating `cfg.Auth.Password.*`
(JSON numbers arrive as `float64` → assert + `uint32(v)`; algorithm is `string`;
rehash is `bool`). **Reconcile env handling with spec 03's manual
`applyPasswordHashingEnv`**: let `systemconfig` own the env>DB>default precedence
for these keys exactly like every other `auth.*` setting; drop or guard the manual
reader so `SP_AUTH_PASSWORD_*` isn't double-applied (the existing keys like
`auth.google.client_id` already prove this path).

**2. config** (`config.go`) — add `RehashOnLogin bool koanf:"rehash_on_login"` to
the `PasswordConfig` block (default `true`); the rest of `PasswordConfig`/
`Argon2Params`/`BcryptParams` already exist (spec 03).

**3. Policy** (`policy.go`) — add `RehashOnLogin bool` to `Policy`; set it in
`PolicyFromConfig` (default true when the block is zero-value). Add
`func ShouldRehash(hash string) bool { return getDefaultPolicy().RehashOnLogin && NeedsRehash(hash) }`.

**4. Startup re-resolve** (`server.go`) — at the end of `InitializeSystemConfig`
(after `sysConfigSvc.Initialize`), best-effort `SetDefaultPolicy(PolicyFromConfig(...))`
(Q2). Keep `:165-169` as the early default.

**5. Rehash gate** (`auth/service.go:475`) — `NeedsRehash` → `passwords.ShouldRehash`.

**6. Write validation** (`handlers/system/service.go` `SetParameter`) — validate the
`auth.password.*` keys (Q4) → `422` on failure. Factor the bounds so they match
`config.Validate` exactly (consider a shared `passwords`/`config` validation helper
to avoid drift).

### Frontend (`web/dash0/`)

New route **`web/dash0/src/routes/orgs/$org/server.hashing.tsx`**, structured like
`server.auth.tsx` (reuse `useSystemParameters`/`useSetSystemParameter`, dirty
tracking, saved/error `Alert`s) — built from **design-reference primitives**
(`Card`, `Label`, `Input type="number"`, `Select`, `Switch`, `Button`); no raw
Radix (`web/dash0/CLAUDE.md`):

- **Algorithm** `Select` (argon2id / bcrypt) — mirror the mail page's auth-type/
  encryption selects.
- **Conditional cost inputs**: argon2 → memory (KiB), time, threads, key_length,
  salt_length; bcrypt → cost. Numeric inputs with inline help (ranges + the OWASP
  floor warning). Empty field = inherit default (backend zero-fills via
  `resolveArgon2Params`).
- **Optional: recommended-profile preset buttons** (64 MiB/t3/p4 · 19 MiB/t2/p1 ·
  9 MiB/t4/p1) that fill the argon2 inputs — steers good choices.
- **"Re-hash existing passwords on next sign-in"** `Switch` →
  `auth.password.rehash_on_login`.
- **Restart notice** (reuse the existing "takes effect after restart" copy pattern).
- **Save** writes algorithm + the active algorithm's params + rehash toggle in one
  `Promise.all` (like `server.auth.tsx:164-185`), all `secret:false`.
- Effective values that were set via YAML/env (not as system parameters) won't
  appear — show defaults as placeholders, same limitation/behavior as the other
  pages (documented; optional effective-policy GET endpoint is a follow-up).

Wiring:
- Add tab to `server.tsx:16-24`: `{ label: t("tabs.hashing"), path: "/orgs/$org/server/hashing" }`
  (after Authentication).
- i18n: `tabs.hashing` + a `hashing` section in `web/dash0/src/locales/en/server.json`
  and the parallel `fr/de/es` files (labels, field labels, help text, range hints,
  restart notice, rehash toggle).

## Files to create / modify

**New:**
- `web/dash0/src/routes/orgs/$org/server.hashing.tsx`.

**Modified (backend):**
- `server/internal/systemconfig/systemconfig.go` — 8 `ParameterKey` consts + defs.
- `server/internal/config/config.go` — `RehashOnLogin` field + default; reconcile
  `applyPasswordHashingEnv`.
- `server/internal/utils/passwords/policy.go` — `Policy.RehashOnLogin`, set in
  `PolicyFromConfig`; add `ShouldRehash`.
- `server/internal/app/server.go` — best-effort policy re-resolve at end of
  `InitializeSystemConfig`.
- `server/internal/handlers/auth/service.go` — `service.go:475` use `ShouldRehash`.
- `server/internal/handlers/system/service.go` — validate `auth.password.*` on write.
- Tests: `systemconfig/*_test.go`, `config/config_test.go`, `passwords/*_test.go`,
  `handlers/system/*_test.go`, `handlers/auth/service_test.go`.

**Modified (frontend):**
- `web/dash0/src/routes/orgs/$org/server.tsx` — tab entry.
- `web/dash0/src/locales/{en,fr,de,es}/server.json` — `tabs.hashing` + `hashing`.

**Modified (docs):** server-settings / config reference — new tab, `auth.password.*`
keys, `SP_AUTH_PASSWORD_*`, restart-to-apply, recommended profiles (overlaps spec 03
docs).

## Verification

New tests use `testify/require` + `t.Parallel()` (`server/CLAUDE.md`).

- **systemconfig:** `auth.password.*` applied with precedence env > DB > default;
  numeric coercion (`float64`→`uint32`); algorithm string; rehash bool.
- **Startup re-resolve:** after an overlay setting `argon2.memory`, the active
  `getDefaultPolicy()` reflects the DB value; an *invalid* overlaid value →
  warn-log, **prior policy retained, no abort** (the key safety property).
- **ShouldRehash:** rehash on + differing hash → true; rehash off → false even when
  `NeedsRehash` is true; matching hash → false.
- **Write validation:** `SetParameter` rejects `algorithm:"sha1"`, bcrypt
  `cost:99`, argon2 `memory:1024` with `422 VALIDATION_ERROR`; accepts a valid
  19 MiB/t2/p1 set; rehash non-bool rejected.
- **Integration** (`make dev-test`): PUT `auth.password.argon2.memory=19456` +
  `time=2` + `threads=1` → restart → a freshly created user's hash encodes
  `m=19456,t=2,p=1`; an existing `m=65536` user logging in with rehash **on** is
  re-minted to the new profile; with rehash **off**, unchanged.
- **E2E** (Playwright): super-admin opens the Hashing tab, switches to bcrypt
  cost 12, saves, sees the restart notice and a saved confirmation; a non-super-admin
  hitting `/orgs/$org/server/hashing` is redirected (`server.tsx:30` gate);
  out-of-range input shows the API validation error.
- `make build && make lint && make test`; dash0 `lint` + Playwright.

## Risk log

| Risk | Mitigation |
|---|---|
| A bad stored `auth.password.*` value aborts the **next startup** (`config.Validate` is fatal) | Write-time validation in `SetParameter` (Q4); **non-fatal** post-overlay re-resolve keeps the prior policy and warns (Q2) — startup never bricks |
| Policy installed at `server.go:165` before the DB overlay → UI changes silently ignored | Re-resolve + `SetDefaultPolicy` at the end of `InitializeSystemConfig` (Q2); covered by the integration test |
| Env double-handling between systemconfig `EnvVar` and spec 03's `applyPasswordHashingEnv` | Let systemconfig own env>DB>default for these keys (as for all `auth.*`); drop/guard the manual reader |
| Startup admin seed (`jobs/jobtypes/job_startup.go:110`) may hash before the re-resolve | Cosmetic — seeded admin re-hashes on next login when rehash is on; order the re-resolve before startup jobs where practical |
| Lowering cost weakens offline-crack resistance | Validation warns below OWASP floors; recommended-profile presets steer good choices; it's a deliberate super-admin action |
| Values set via YAML/env aren't visible in the UI (only system-param overrides show) | Placeholders show effective defaults; documented; optional effective-policy GET endpoint as follow-up |
| Partial multi-key save leaves inconsistent params | Write all active-algorithm keys in one `Promise.all`; backend zero-fills + validates so any subset still resolves to a valid policy |
| Mismatch between write-time bounds and `config.Validate` bounds | Share one validation helper between `config` and the system handler to prevent drift |

**Status**: Todo | **Created**: 2026-06-30 | **Depends on**: 2026-06-29-03 (implemented)

## Implementation Plan

1. **config + policy:** add `RehashOnLogin` to `PasswordConfig` (default true) and
   `Policy`; set it in `PolicyFromConfig`; add `ShouldRehash`. Unit tests.
2. **systemconfig:** 8 `auth.password.*` keys + `ParameterDefinition`s mutating
   `cfg.Auth.Password.*`; reconcile env handling. Tests for the overlay + precedence.
3. **startup re-resolve:** best-effort `SetDefaultPolicy(PolicyFromConfig)` at the
   end of `InitializeSystemConfig`; switch `service.go:475` to `ShouldRehash`. Tests
   for "DB value wins after restart" and "invalid value keeps prior policy".
4. **write validation:** validate `auth.password.*` in `SetParameter` (shared bounds
   helper) → 422. Handler tests.
5. **frontend:** `server.hashing.tsx` (algorithm select, conditional cost inputs,
   rehash switch, presets, restart notice, save); tab in `server.tsx`; i18n in
   en/fr/de/es. Playwright.
6. **docs:** server-settings + config reference. `make build && make lint && make test`;
   dash0 lint + Playwright.
