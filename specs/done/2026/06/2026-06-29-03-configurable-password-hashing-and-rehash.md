# Configurable password hashing — selectable algorithm, cost, and rehash-on-change

## Context

Password hashing today is **hardcoded** to a single argon2id profile in
`server/internal/utils/passwords/passwords.go`: `m=64 MiB, t=3, p=4`
(`passwords.go:14-22`, the `argon2Memory/argon2Time/argon2Threads` consts). `Hash`
emits the self-describing PHC-style string `$argon2id$v=19$m=..,t=..,p=..$salt$hash`
(`passwords.go:38-40`) and `Verify` parses the algorithm + params **back out of the
stored string** (`passwords.go:46-53`) — so verification is already
parameter-independent: an old hash keeps verifying with its own embedded `m/t/p`
even after the consts change. The only thing pinned to the consts is *hashing a new
password*.

Two things make this feature low-risk and mostly mechanical:

1. **Hashes already carry their algorithm.** Stored strings begin with a `$<id>$`
   marker — `$argon2id$…` here, plus the dev escape hatch `$plaintext$<password>`
   handled directly in the login path (`auth/service.go:419-423`). bcrypt's native
   strings are likewise self-identifying (`$2a$ / $2b$ / $2y$`). So `Verify` can
   **dispatch on the prefix** and old + new algorithms coexist with zero migration.
2. **There is exactly one place a password is available in plaintext at verify
   time** — the login path `auth/service.go:414-429` (`passwords.Verify` at `:426`).
   That is the single hook where a rehash-on-change can happen transparently.
   Password change / reset / admin-set already call `passwords.Hash`
   (`service.go:1701, 2126, 2527`) so they pick up the configured algorithm for
   free.

What's missing is the ability for an operator to **choose the algorithm and its
complexity from config**, and to **transparently upgrade existing users' hashes**
when that choice changes. This is the standard `needs_rehash` pattern (PHP's
`password_needs_rehash`, Django's `must_update`): on a successful login, if the
stored hash's algorithm *or* cost parameters differ from the currently configured
target, re-hash the just-verified plaintext and persist it.

This directly enables the memory-vs-CPU tuning discussed for this codebase: an
operator can drop argon2id to a lighter OWASP profile (e.g. `19 MiB / t=2 / p=1`)
or switch to bcrypt entirely, and existing users migrate forward on their next
login — no forced password reset.

## The key questions

### Q1 — Which algorithms ship in v1?

The honest tuning axis is **memory-hard (argon2id)** vs **CPU-hard, constant tiny
memory (bcrypt)** — the two ends of the trade-off. Both live in
`golang.org/x/crypto`, already a dependency (argon2 is imported today), so **no new
module** is required for either; scrypt sits in the same module if added later.

**Decision:** ship `argon2id` (default, unchanged behavior) and `bcrypt`. Build the
dispatch as a small prefix-keyed registry so `scrypt`/`pbkdf2` can be added later
without touching call sites. Unknown algorithm in config → **fail fast at startup**,
never a silent fallback.

### Q2 — Where do algorithm + complexity live in config?

Under the existing `AuthConfig` (`config.go:290`, koanf tag `auth`), a nested
`password` block with per-algorithm params. Defaults **reproduce today's exact
argon2id profile**, so the change is a no-op until an operator reconfigures it —
zero forced churn on upgrade.

```go
type PasswordConfig struct {
    Algorithm string        `koanf:"algorithm"` // "argon2id" (default) | "bcrypt"
    Argon2    Argon2Params  `koanf:"argon2"`
    Bcrypt    BcryptParams  `koanf:"bcrypt"`
}
type Argon2Params struct {
    Memory     uint32 `koanf:"memory"`      // KiB, default 65536 (64 MiB)
    Time       uint32 `koanf:"time"`        // default 3
    Threads    uint8  `koanf:"threads"`     // default 4
    KeyLength  uint32 `koanf:"key_length"`  // default 32
    SaltLength uint32 `koanf:"salt_length"` // default 16
}
type BcryptParams struct {
    Cost int `koanf:"cost"` // default 12 (bcrypt range 4–31; validate ≥10)
}
```

**koanf env-var gotcha (known quirk):** koanf's env loader maps `_`→`.`, so
multi-word keys like `SP_AUTH_PASSWORD_ARGON2_MEMORY` / `..._KEY_LENGTH` /
`..._BCRYPT_COST` mis-map. These need a **manual env reader**, exactly like
`applyRateLimitingEnv` (`config.go:652-681`, using the `intEnv`/`durEnv` helpers).
Add `applyPasswordHashingEnv(cfg)` and call it alongside the other manual readers
(`config.go:575-650`). `SP_AUTH_PASSWORD_ALGORITHM` (single word) maps fine but
fold it into the same reader for consistency.

### Q3 — What does "the algorithm changes" mean for rehash?

The literal ask is "if the algorithm changes, rehash." Broaden it (standard
practice) to **algorithm _or_ cost parameters changed** — otherwise lowering
argon2id memory wouldn't migrate anyone. `NeedsRehash(storedHash)` returns true
when the stored hash's algorithm id, or any cost parameter, differs from the active
policy. argon2 params are parsed from the PHC string (as `Verify` already does);
bcrypt cost via `bcrypt.Cost([]byte(hash))`.

**Decision:** headline = algorithm change; included case = parameter change; both
trigger a transparent rehash at login.

### Q4 — bcrypt's 72-byte limit

`golang.org/x/crypto/bcrypt` **errors** on inputs >72 bytes (newer x/crypto no
longer silently truncates) and truncates at embedded NUL bytes. **Decision:** for
the `bcrypt` algorithm, pre-hash the password as `base64(sha256(password))` (44
ASCII chars, NUL-free) before `GenerateFromPassword`; `Verify` replays the same
pre-hash for `$2b$`-prefixed strings. This is a fixed, documented property of our
bcrypt scheme — our hashes are only ever produced and consumed by us, so
self-consistency is all that's required. (Cost still lives in the bcrypt string, so
`NeedsRehash` reads it directly.)

## Goal

An operator sets `auth.password.algorithm` and the matching cost block (YAML or
`SP_AUTH_PASSWORD_*`) to choose how passwords are hashed. New hashes use that
profile; **existing hashes keep verifying** regardless of algorithm; and on the
next successful login a user whose stored hash no longer matches the configured
algorithm/cost is **transparently re-hashed** and updated in the DB — best-effort,
never blocking or failing the login. Defaults reproduce the current argon2id
profile exactly, so upgrading the binary changes nothing until reconfigured.

## Non-goals

- **A background/bulk re-hash job.** Rehash happens lazily at login only (that is
  the only point with the plaintext). Users who never log in keep their old hash —
  which still verifies. No batch migration.
- **Re-hashing the `$plaintext$` dev escape hatch** into a real algorithm. Left
  exactly as-is (`service.go:419-423`); it is a dev-only convenience. (Optional
  toggle noted under Follow-ups.)
- **scrypt / PBKDF2 / yescrypt.** Registry is built to admit them; not implemented
  in v1.
- **Per-org or per-user algorithm policy.** One process-wide policy from config.
- **Changing the stored hash format.** Keep PHC-style `$argon2id$…` and native
  bcrypt `$2b$…`; no DB migration (the `password_hash` column is already free-form
  text).

## Design

### `passwords` package refactor

Split the single file into algorithm-dispatched pieces (same package
`server/internal/utils/passwords/`), keeping the public surface minimal and
**`Verify` pure / policy-free** so it never depends on global state (important for
`t.Parallel()`):

- `policy.go`
  - `type Policy struct { … }` — resolved algorithm + params.
  - `func PolicyFromConfig(c config.AuthConfig) (Policy, error)` — maps config →
    policy; validates (Q2/Validation). *(Keep the import direction clean: if
    `passwords` importing `config` would cycle, pass a small local params struct
    instead and map in the app/config layer.)*
  - `func SetDefaultPolicy(p Policy)` / package default — set once at startup so the
    existing package-level `Hash` keeps working at all current call sites.
- `argon2id.go` — current `Hash`/`Verify` logic, now param-driven from `Policy`;
  PHC encode/decode (decode already exists at `passwords.go:46-53`).
- `bcrypt.go` — `GenerateFromPassword`/`CompareHashAndPassword` with the
  `base64(sha256(pw))` pre-hash (Q4).
- Public API:
  - `Hash(password string) (string, error)` — uses the default policy (unchanged
    signature → call sites `service.go:1701/2126/2527` untouched).
  - `Verify(password, hash string) bool` — **dispatch on `$<id>$` prefix**:
    `$argon2id$`→argon2id (parse `m/t/p` from string as today), `$2a$/$2b$/$2y$`→
    bcrypt; unknown → false. Note `$bcrypt$…` (the fake in the existing negative
    test, `passwords_test.go:41`) is **not** a native bcrypt prefix and correctly
    stays invalid.
  - `NeedsRehash(hash string) bool` — true when `hash`'s algorithm or any cost
    param differs from the default policy; true for an unparseable/unknown hash so
    it gets re-minted on next login.

### Config wiring (`config.go`)

- Add `Password PasswordConfig` to `AuthConfig` (`config.go:290-296`) + the param
  structs (Q2).
- Defaults in the `AuthConfig{…}` literal (`config.go:472-480`): `Algorithm:
  "argon2id"`, argon2 `{65536,3,4,32,16}`, bcrypt `{Cost:12}` — i.e. current
  behavior.
- `applyPasswordHashingEnv(cfg)` manual reader (Q2 gotcha) next to the existing
  manual env block (`config.go:575-650`); reuse `intEnv` and add a tiny `strEnv`
  if none exists.
- **Validation** (fail fast at load, not at first login): algorithm ∈
  {argon2id, bcrypt}; argon2 `memory ≥ 8192 KiB`, `time ≥ 1`, `threads ≥ 1`,
  `key_length ≥ 16`, `salt_length ≥ 8`; bcrypt `cost ∈ [10,31]` (warn-log if `<12`
  or argon2 memory below the OWASP `19456` floor — allowed but flagged).

### App startup

Call `passwords.SetDefaultPolicy(p)` once where config + services are assembled
(`internal/app/…` server bootstrap), with `p, err := passwords.PolicyFromConfig(cfg.Auth)`;
a policy error aborts startup (consistent with other fatal config validation).

### Rehash-on-login hook (`auth/service.go`)

Immediately after the successful `passwords.Verify` at `service.go:426` (and
**before** the 2FA branch at `:438`, since the password is already proven there),
best-effort:

```go
if passwords.NeedsRehash(*user.PasswordHash) {
    if newHash, err := passwords.Hash(password); err == nil {
        if err := s.db.UpdateUser(ctx, user.UID,
            &models.UserUpdate{PasswordHash: &newHash}); err != nil {
            s.log.Warn("password rehash persist failed", "userUid", user.UID, "err", err)
        }
    } else {
        s.log.Warn("password rehash failed", "userUid", user.UID, "err", err)
    }
}
```

Rules: **never** let a rehash error affect the login result; the user is already
authenticated. The `UpdateUser(..., UserUpdate{PasswordHash})` shape is exactly the
one already used at `service.go:2131`. The `$plaintext$` branch is left out of the
hook (non-goal).

### Recommended profiles (for docs)

| Algorithm | Params | Memory/login | Note |
|---|---|---|---|
| argon2id (default) | `m=65536, t=3, p=4` | 64 MiB | current; RFC 9106 memory-constrained profile |
| argon2id (lighter) | `m=19456, t=2, p=1` | 19 MiB | OWASP; drops 4-thread CPU contention |
| argon2id (min) | `m=9216, t=4, p=1` | 9 MiB | OWASP floor; still GPU-hostile |
| bcrypt | `cost=12` | ~4 KiB | constant; not memory-hard (weaker vs GPU/ASIC) |

## Files to create / modify

**New (backend):**
- `server/internal/utils/passwords/{policy.go,argon2id.go,bcrypt.go}` (split from
  today's single `passwords.go`).

**Modified (backend):**
- `server/internal/utils/passwords/passwords.go` — thin public API
  (`Hash`/`Verify`/`NeedsRehash`/`SetDefaultPolicy`) delegating to the split files.
- `server/internal/utils/passwords/passwords_test.go` — extend (Verification).
- `server/internal/config/config.go` — `PasswordConfig` + param structs, defaults,
  `applyPasswordHashingEnv`, validation.
- `server/internal/config/config_test.go` — defaults, env override, validation.
- `server/internal/app/…` (server bootstrap) — `SetDefaultPolicy` from config.
- `server/internal/handlers/auth/service.go` — rehash-on-login hook after `:426`.
- `server/internal/handlers/auth/service_test.go` — rehash hook coverage.
- `server/go.mod` — **no change expected** (bcrypt is `golang.org/x/crypto/bcrypt`,
  same already-required module as argon2); confirm with `go build`.

**Modified (docs):**
- Config reference + env-var table in `web/docs/` (the `auth.*` config docs): new
  `auth.password.*` keys, `SP_AUTH_PASSWORD_*` vars, the recommended-profiles table,
  and the bcrypt pre-hash note. Update any shipped example config file (grep for one,
  e.g. `config*.yaml`).

## Verification

New tests use `testify/require` + `t.Parallel()` per `server/CLAUDE.md`. (The
existing `passwords_test.go` predates that and uses bare `t.Error`; optionally
modernize while here — low priority.)

- **`passwords` unit:**
  - Round-trip per algorithm: argon2id and bcrypt `Hash`→`Verify` ok; wrong
    password fails; two hashes of the same password differ (salt) — extend the
    existing `TestHashAndVerify`/`TestHashUniqueness`.
  - **Policy-independence of `Verify`:** an argon2id hash verifies while the default
    policy is bcrypt, and vice-versa (proves dispatch reads the stored marker, not
    the global).
  - **`NeedsRehash`:** same algo+params → false; different algorithm → true; same
    algo with changed cost (argon2 memory/time, bcrypt cost) → true; unparseable →
    true. Keep the existing invalid-hash table (`passwords_test.go:32-56`) green.
  - **bcrypt 72-byte:** a >72-byte password (and one containing a NUL byte)
    hashes + verifies (proves the sha256+base64 pre-hash).
- **`config` unit:** defaults map to the exact current argon2id profile; algorithm
  `"bcrypt"`→ cost honored; `applyPasswordHashingEnv` maps
  `SP_AUTH_PASSWORD_ALGORITHM`, `SP_AUTH_PASSWORD_ARGON2_MEMORY`,
  `SP_AUTH_PASSWORD_BCRYPT_COST`, `..._KEY_LENGTH`; validation rejects unknown
  algorithm, bcrypt cost out of `[10,31]`, sub-floor argon2 memory.
- **`auth/service` unit:** login with a hash whose algo/params differ from the
  active policy → exactly one `UpdateUser{PasswordHash}` with a hash that
  subsequently `Verify`s and is `NeedsRehash==false`; login with a matching hash →
  **no** `UpdateUser`; a forced `UpdateUser` error still returns a successful login;
  `$plaintext$` login → no rehash.
- **End-to-end** (`make dev-test`): set `SP_AUTH_PASSWORD_ALGORITHM=bcrypt`, log in
  as `admin@solidping.com` (whose seed hash is argon2id), confirm the stored
  `password_hash` flips to `$2b$…` after login and a second login still works;
  switch back to argon2id with lighter params and confirm the next login re-mints to
  the new `m/t/p`.
- `make build && make lint && make test` (backend).

## Risk log

| Risk | Mitigation |
|---|---|
| Global default-policy state breaks `t.Parallel()` | `Verify` is pure (reads the stored marker); only `Hash`/`NeedsRehash` consult the default; policy-sensitive tests pass explicit `Policy`/hashes, not the global |
| Misconfiguration locks everyone out (unknown algorithm, absurd bcrypt cost, sub-floor argon2) | Validate at config load → fail fast at startup; never a silent fallback; warn-log near-floor values |
| Rehash error turns a valid login into a failure | Hook is best-effort: user is already authenticated; persist errors are logged and swallowed, retried on next login |
| Login-spike write amplification | `NeedsRehash` gates it — a user re-hashes at most once after a policy change; thereafter no-op |
| Lowering params weakens offline-crack resistance | Operator's deliberate choice; validation warns below OWASP floors; recommended-profiles table documents the trade-off (memory-hard vs CPU-hard) |
| bcrypt 72-byte / NUL truncation | Mandatory `base64(sha256(pw))` pre-hash, covered by tests |
| koanf `_`→`.` env mangling silently drops `SP_AUTH_PASSWORD_*` | Manual `applyPasswordHashingEnv` reader (mirrors `applyRateLimitingEnv`), tested |
| Concurrent logins for one user both rehash | Idempotent — both write a valid current-policy hash; last-writer-wins is fine |
| `passwords`→`config` import cycle | If it arises, map config→`Policy` in the config/app layer and keep `passwords` free of a `config` import |

**Follow-ups (out of scope):** optional `auth.password.upgrade_plaintext` toggle to
re-hash `$plaintext$` dev logins into the real algorithm; scrypt/pbkdf2 registry
entries; a one-shot admin "rehash all" maintenance command if a forced migration is
ever needed.

**Status**: Todo | **Created**: 2026-06-29

## Implementation Plan

1. **`passwords` package:** split into `policy.go`/`argon2id.go`/`bcrypt.go`;
   add `Policy`, `PolicyFromConfig`/local-params mapping, `SetDefaultPolicy`,
   prefix-dispatch `Verify`, `NeedsRehash`, bcrypt with sha256+base64 pre-hash.
   Keep argon2id as the default profile. Unit tests (round-trip, policy-independence,
   NeedsRehash, bcrypt 72-byte).
2. **Config:** `PasswordConfig` + param structs under `AuthConfig`; defaults
   reproducing the current argon2id profile; `applyPasswordHashingEnv` manual reader;
   validation. Config tests. Wire `passwords.SetDefaultPolicy(PolicyFromConfig(cfg.Auth))`
   at server bootstrap (fatal on error).
3. **Auth service:** rehash-on-login hook after `passwords.Verify`
   (`service.go:426`), best-effort persist via `UpdateUser{PasswordHash}`. Service
   tests (rehash fires / doesn't / survives persist error / skips `$plaintext$`).
4. **Docs:** `auth.password.*` keys + `SP_AUTH_PASSWORD_*` table + recommended
   profiles + bcrypt note; update any example config file.
5. `make build && make lint && make test`.
