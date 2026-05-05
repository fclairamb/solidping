# Credentials encryption at rest

## Context

Today every credential a user gives Solidping is stored in **plaintext** in the database:

- `checks.config` (JSONB/TEXT) holds the per-check config map. Of the 32 registered check types, **at least 20 carry credentials** in this map: `password` for HTTP/SSH/SFTP/SMTP/IMAP/POP3/FTP/MySQL/Postgres/Oracle/MSSQL/Mongo/RabbitMQ/MQTT/Redis, `private_key` for SSH/SFTP, `sasl_password` for Kafka, `auth_password` + `priv_password` for SNMP, plus arbitrary `token` fields for Email/Heartbeat checks.
- `integration_connections.settings` (JSONB) holds OAuth tokens and webhook URLs — `SlackSettings.AccessToken`, `DiscordSettings.WebhookURL`, etc. (`server/internal/db/models/integration.go`).
- `auth_providers.config` (JSONB) holds OAuth client secrets per org.
- `check_jobs.config` is a denormalized snapshot dispatched to workers — same plaintext.

The dashboard also receives the full config back from `GET /api/v1/orgs/:org/checks/:slug` (`server/internal/handlers/checks/service.go:1170` — `Config: check.Config` is passed unchanged), so any user with read access to a check can read its credentials in their browser, regardless of whether they originally entered them.

The DB schema already anticipated this work: `CheckJob.Encrypted` exists (`server/internal/db/models/check_job.go:19`), unused.

Goal: if the database is stolen, no credential can be recovered without also possessing an out-of-band master key. As a side effect, the dashboard stops echoing secrets back to the client.

## Honest opinion

The proposed design (envelope encryption with a master key and per-org DEKs, two-column split, server-side enforcement) is **correct in spirit**. Six concrete adjustments before we build it.

1. **Master key must be out-of-band, not in `system_parameters`.** The JWT secret currently auto-generates and persists into the DB if the env var is unset (`systemconfig.ensureJWTSecret`). For a credential-encryption KEK that pattern is wrong — if the master key sits in the same DB as the ciphertexts, "the database is stolen" leaks both. The master key must come from `SP_ENCRYPTION_MASTER_KEY` (or a file path via `SP_ENCRYPTION_MASTER_KEY_FILE`, the standard k8s/secret-mount pattern) and **never** be persisted server-side. If it's missing, encryption is off — but we log a `WARN` at startup, not silent.

2. **"Each check defines how" should be a one-line method on the existing `Config` interface, not bespoke per-check splitter functions.** The `checkerdef.Config` interface already has `FromMap`/`GetConfig`. Add `SecretFields() []string` returning the JSON keys whose values are secrets. The split is then **generic**: walk the config map, move any key in `SecretFields()` into the private side. 32 check types × one method ≈ 32 small commits, no clever abstraction. This is the cheap right answer.

3. **HTTP `headers` and `body` are a known leak in V1.** A user can put `Authorization: Bearer xyz` in the `headers` map and we will store it in plaintext, because the secret-key list operates at top-level keys, not values inside maps. Marking the whole `headers` map as secret was considered, but it hides non-sensitive headers from the dashboard for the 95 % of HTTP checks that don't use auth headers — net UX regression. **Recommendation: ship V1 with explicit fields only (`password`, `private_key`, `token`, `sasl_password`, etc.) and document the limitation.** A V2 can add a per-check `secretHeaders: ["Authorization"]` array driven from the check config itself. Same applies to `body`.

4. **Two-column split (`config` + `config_private`) vs inline tagging (`{"password": "enc:v1:..."}`).** The split is the better choice: (a) the public column is queryable/indexable, the encrypted column is opaque; (b) at read-from-DB time the public column is human-readable in `psql` for debugging without any decrypt path; (c) the dashboard payload-shaping is trivial — return `config`, omit `config_private`, attach a `configPrivateKeys: [...]` hint so the UI can render placeholder dots. The inline approach is simpler to migrate but mixes concerns and forces every reader to know about the `enc:` prefix.

5. **PATCH update semantics are the ugliest part of this whole design and must be specified explicitly, not left as "TBD".** Today PATCH replaces `config` wholesale (`service.go:750`). After encryption, the dashboard will not have the secret values when the user edits a check, so a naïve full-replace would silently wipe every secret. The contract has to be: **on PATCH, secret keys absent from the incoming `config` are preserved from the existing `config_private`; secret keys present (including `null` to clear, or empty-string) replace.** This is the only sane behavior; getting it wrong is a footgun that will be discovered six months in by a customer who edited their check and silently broke it.

6. **Don't do per-org without admitting per-org is overkill in single-tenant deploys.** Per-org keys are the right shape for SaaS where blast radius matters. For the typical self-hosted single-org install, having a per-org DEK adds one DB lookup + one AES-GCM decrypt per check operation with zero meaningful security gain over a single global DEK. **Recommendation: do per-org anyway** — the cost is negligible (DEK is cached in memory after first decrypt, ~32 bytes per org, never re-derived), and it's the only design that works for multi-org deployments without re-architecting later. Just don't pretend the security gain is meaningful in the 1-org case.

Three things explicitly **punted** out of this spec, because each is its own conversation:

- **Master-key rotation.** Easy to add later thanks to the versioned envelope (`{"v": 1, ...}`). Out of scope for V1.
- **Per-org DEK rotation.** Same — versioned envelope means we can add a `rotate-org-key` command later without schema changes.
- **HSM / cloud-KMS integration** (AWS KMS, GCP KMS, Vault). The KEK provider is wrapped behind a small interface; AWS-KMS-backed implementation is a future-spec drop-in. Mentioned in "Future work".

The most honest piece: **encryption-at-rest only protects against database theft. It does not protect against a compromised server process, a malicious admin, a worker leaking credentials in its logs, an over-permissive RBAC config, or a curl with `?includeSecrets=true`.** This is real and useful — it kills the "snapshot of the prod DB on a developer's laptop" risk and it stops the dashboard echo — but it is not a security panacea. We should be precise about that in the changelog.

## Scope

**In scope:**

- New env-var-driven master key (`SP_ENCRYPTION_MASTER_KEY` or `SP_ENCRYPTION_MASTER_KEY_FILE`).
- Per-org DEK (Data Encryption Key), wrapped by master key, persisted in the existing `parameters` table with `secret = true`, cached in memory.
- New `config_private` column on `checks` (and `settings_private` on `integration_connections`, `config_private` on `auth_providers`, `config_private` on `check_jobs`).
- A `SecretFields() []string` method added to every checker config struct enumerating the secret JSON keys, used by a generic splitter.
- Same coverage applied to `IntegrationConnection.Settings` (Slack tokens, webhook URLs, Opsgenie API keys, etc.) and `AuthProvider.Config` (OAuth client secrets).
- Server-side split on write, server-side merge on read-for-worker.
- Dashboard `GET` returns `config` (public only) plus a `configPrivateKeys: [...]` hint; never returns secret values.
- PATCH preserves absent secret keys from existing `config_private`; explicitly-provided keys (including empty string / null) overwrite.
- Worker dispatch decrypts and merges before sending the plaintext config over TLS.
- One-shot migration command (`solidping encrypt-credentials`) to encrypt existing plaintext data when the env var is set for the first time on an existing install. Idempotent.
- Startup `WARN` when master key is unset and any plaintext secret exists, or when encrypted rows exist but no master key is configured.

**Out of scope (own follow-ups):**

- Master-key rotation and per-org DEK rotation commands.
- AWS-KMS / GCP-KMS / Vault KEK providers.
- Encrypting `headers` / `body` values for HTTP checks (V2).
- Audit log of secret access events.
- Field-level masking in API responses for non-credential PII.
- Encrypting `system_parameters` rows (e.g., the JWT secret) — that's a different threat model.

## Approach

### 1. Master key loading

`server/internal/config/config.go` gets a new struct:

```go
type EncryptionConfig struct {
    MasterKey     string `koanf:"master_key"`      // SP_ENCRYPTION_MASTER_KEY (base64, 32 bytes after decode)
    MasterKeyFile string `koanf:"master_key_file"` // SP_ENCRYPTION_MASTER_KEY_FILE (path containing base64)
}
```

Loaded in `Load()`. If both are set, the file wins. If neither is set, encryption is disabled and `Service.Enabled() == false`. Validated in `Validate()`: when set, the decoded key must be exactly 32 bytes.

A new package `server/internal/crypto/credentials/` exposes:

```go
type Service interface {
    Enabled() bool
    EncryptForOrg(ctx context.Context, orgUID string, plaintext map[string]any) (string, error) // returns envelope JSON
    DecryptForOrg(ctx context.Context, orgUID string, envelope string) (map[string]any, error)
    EnsureOrgKey(ctx context.Context, orgUID string) error // generates DEK on first use
}
```

Internally:

- AES-256-GCM on every encrypt/decrypt call (KEK and DEK both).
- Envelope format: `{"v":1,"alg":"AES-256-GCM","nonce":"<b64>","ct":"<b64>"}` — JSON inside the column for forward-compatibility.
- DEK is generated with `crypto/rand` (32 bytes), wrapped with the KEK, stored in `parameters` under key `encryption.dek` (`secret=true`, `value` = envelope JSON).
- DEK cache: `sync.Map[orgUID]= [32]byte`. Populated on first decrypt; never persisted.
- Master-key wipe protection: don't store the raw KEK on the `Service` struct beyond what AES-GCM needs; copy at construction, zero on shutdown best-effort.

The service is wired via `services.Registry` and injected into `checks`, `connections`, `auth_providers`, and the worker job dispatcher.

### 2. `SecretFields()` on the checker `Config` interface

In `server/internal/checkers/checkerdef/interface.go`:

```go
type Config interface {
    FromMap(map[string]any) error
    GetConfig() map[string]any
    SecretFields() []string // JSON keys whose values are secrets
}
```

Each of the 32 checker config structs implements it. Examples:

```go
// checkhttp/config.go
func (c *HTTPConfig) SecretFields() []string { return []string{"password"} }

// checkssh/config.go
func (c *SSHConfig) SecretFields() []string { return []string{"password", "private_key"} }

// checksnmp/config.go
func (c *SNMPConfig) SecretFields() []string { return []string{"auth_password", "priv_password"} }

// checktcp/config.go (no secrets)
func (c *TCPConfig) SecretFields() []string { return nil }
```

A central helper in `checkerdef`:

```go
// SplitConfig partitions a config map into (public, private) using the
// checker's declared secret fields. Keys not declared as secret stay public.
func SplitConfig(cfg Config, full map[string]any) (public, private map[string]any) {
    secrets := map[string]struct{}{}
    for _, k := range cfg.SecretFields() {
        secrets[k] = struct{}{}
    }
    public = make(map[string]any, len(full))
    private = make(map[string]any)
    for k, v := range full {
        if _, ok := secrets[k]; ok {
            private[k] = v
        } else {
            public[k] = v
        }
    }
    return public, private
}

// MergeConfig is the inverse: returns a new map equal to public ∪ private.
// Used by the worker dispatcher.
func MergeConfig(public, private map[string]any) map[string]any { /* ... */ }
```

For `IntegrationConnection` and `AuthProvider` (which don't go through the checker registry), define `SecretFields` on each settings struct (`SlackSettings.SecretFields() []string { return []string{"access_token"} }`, etc.) or, since they have only a few cases, hardcode a small map keyed by `ConnectionType` / `AuthProviderType` in a single place.

### 3. Schema changes

Migration `005_credentials_encryption.{up,down}.sql` in **both** `server/internal/db/postgres/migrations/` and `server/internal/db/sqlite/migrations/`:

```sql
-- postgres
ALTER TABLE checks                  ADD COLUMN config_private      TEXT;
ALTER TABLE checks                  ADD COLUMN config_private_keys TEXT;
ALTER TABLE check_jobs              ADD COLUMN config_private      TEXT;
ALTER TABLE check_jobs              ADD COLUMN config_private_keys TEXT;
ALTER TABLE integration_connections ADD COLUMN settings_private    TEXT;
ALTER TABLE integration_connections ADD COLUMN settings_private_keys TEXT;
ALTER TABLE auth_providers          ADD COLUMN config_private      TEXT;
ALTER TABLE auth_providers          ADD COLUMN config_private_keys TEXT;
```

`config_private` is `TEXT` (the envelope JSON), nullable. NULL means "no encrypted secrets exist for this row" — distinct from "encryption is disabled at the server" (the latter is a server-level fact). `config_private_keys` is a JSON array of key names (strings) — non-secret, used by the dashboard read path so we don't have to decrypt to render placeholder hints. We **deliberately do not add an `encrypted bool`** column on `checks` — the existing `check_jobs.encrypted` column was a partial earlier attempt and we will repurpose it (set to `true` when `config_private` is non-NULL).

The `JSONMap` model fields gain siblings:

```go
// models/check.go
type Check struct {
    // ...
    Config            JSONMap `bun:"config,type:jsonb,nullzero"`
    ConfigPrivate     *string `bun:"config_private,type:text,nullzero"`        // envelope JSON
    ConfigPrivateKeys *string `bun:"config_private_keys,type:text,nullzero"`   // JSON array of key names
}
```

Same shape on `IntegrationConnection.SettingsPrivate(+Keys)`, `AuthProvider.ConfigPrivate(+Keys)`, `CheckJob.ConfigPrivate(+Keys)`.

### 4. Write path (create / update)

`POST /api/v1/orgs/:org/checks` and `PATCH /api/v1/orgs/:org/checks/:check`:

1. Resolve `Checker` from registry, get `SecretFields()`.
2. Build the **effective config** the user intends:
   - On POST: just the request body's `config`.
   - On PATCH: read existing check, decrypt its `config_private` if present, merge into existing public `config` to get the existing effective config. Then layer the request's `config` on top **only for keys present in the request** (this is the merge rule that solves the secret-preservation problem). If a secret key is sent as `null` or empty string, drop it.
3. Run validation (existing flow, unchanged).
4. Call `SplitConfig(checker, effective)` → `(public, private)`.
5. If encryption is enabled and `len(private) > 0`: `envelope, err := credService.EncryptForOrg(orgUID, private)`. Persist `config = public`, `config_private = envelope`, `config_private_keys = json(keys(private))`.
6. If encryption is disabled: persist the merged map back into `config`, leave `config_private = NULL`. **Plaintext fallback is intentional** — we want self-hosted users without `SP_ENCRYPTION_MASTER_KEY` to keep working today. Logged once at startup.

Same flow for the connections handler and the auth-providers handler.

### 5. Read path (dashboard)

`GET /api/v1/orgs/:org/checks/:slug` and list endpoints:

`convertCheckToResponse` (`server/internal/handlers/checks/service.go:1157`) changes:

```go
// before:  Config: check.Config,
// after:
resp.Config = check.Config // public part only
if check.ConfigPrivateKeys != nil {
    resp.ConfigPrivateKeys = parseJSONStringArray(*check.ConfigPrivateKeys) // non-secret list
}
// resp.ConfigPrivate is NEVER set on outbound responses.
```

The read path is stateless — no decryption needed for list endpoints. The `ConfigPrivateKeys` list is non-secret by construction (it's just key names like `"password"`, `"private_key"`).

The dashboard renders an input as a placeholder ("●●●●●●●● — leave blank to keep") for any field name appearing in `configPrivateKeys`.

### 6. Worker dispatch

`server/internal/handlers/workers/handler.go:ClaimJobs`:

When building the `ClaimJobsResponse`, for each `CheckJob`:

```go
fullConfig := job.Config
if job.ConfigPrivate != nil {
    if !credService.Enabled() {
        // Master key not configured but row has encrypted secrets.
        // Skip the job, log once per (org, check) per startup window.
        log.Warn("skipping encrypted job — SP_ENCRYPTION_MASTER_KEY not set",
            "org", job.OrganizationUID, "check", job.CheckUID)
        continue
    }
    private, err := credService.DecryptForOrg(ctx, job.OrganizationUID, *job.ConfigPrivate)
    if err != nil {
        // Decrypt failure (wrong key, tampered ciphertext, missing org DEK).
        // Skip — never send half-credentials to a worker.
        log.Error("failed to decrypt job config", "err", err,
            "org", job.OrganizationUID, "check", job.CheckUID)
        continue
    }
    fullConfig = checkerdef.MergeConfig(job.Config, private)
}
job.Config = fullConfig
job.ConfigPrivate = nil // never ship the envelope to the worker
job.Encrypted = false
```

Behavior contract for the missing-key case (admin lost `SP_ENCRYPTION_MASTER_KEY`):

- Server starts normally — does **not** refuse to start.
- Startup logs a single `WARN` with the count of rows that have non-NULL `config_private` / `settings_private` across the whole DB.
- Encrypted checks/connections continue to be readable in the dashboard (`config` + `configPrivateKeys`); only the run-time path (workers) skips them.
- Plaintext checks keep working unchanged.
- Recovery: re-supply the master key and the next claim tick picks them up — no DB repair needed.

The worker continues to receive a single merged config map — no protocol change. Workers are trusted. Document this explicitly.

`CheckJob` rows are written by the scheduler when a check fires; that path also goes through `SplitConfig` + `EncryptForOrg`, so jobs in the queue are encrypted at rest just like the source `Check`.

### 7. One-shot migration command

```bash
./solidping encrypt-credentials [--dry-run]
```

Iterates: organizations → checks → integration connections → auth providers → check_jobs. For each row whose `config_private` (or `settings_private`) is NULL but whose public field contains keys appearing in the type's `SecretFields()`, splits and encrypts. Idempotent.

Run automatically once at startup if `SP_ENCRYPTION_MASTER_KEY` is set and we detect any plaintext secret. Behind a `SP_ENCRYPTION_AUTO_MIGRATE=false` flag for users who want to control timing.

### 8. Export / import

`GET /api/v1/orgs/:org/checks/export`: strip secret keys entirely from the exported JSON. Add a top-level note in the export payload (`"_secretsStripped": true`). This is the safe default and avoids the question "should we re-encrypt for a different KEK on import".

`POST /api/v1/orgs/:org/checks/import`: accept configs without secret keys, persist them with `config_private = NULL`. Users re-enter secrets after import. Document this behavior on the export response.

## Verification

1. **Unit**:
   - `crypto/credentials`: round-trip encrypt → decrypt, wrong-org decrypt fails, tampered ciphertext fails (GCM auth tag), empty-private-map handling, KEK rotation hook (interface only).
   - `checkerdef.SplitConfig` / `MergeConfig`: secret keys move correctly, non-secret keys are preserved, empty private map yields `nil` envelope.
   - Each checker's `SecretFields()` is unit-tested against a sample config.

2. **Integration** (testcontainers, both Postgres and SQLite):
   - Create check with password → row in DB has `config.password` absent and `config_private` non-NULL.
   - GET on the check returns `config` without password and `configPrivateKeys: ["password"]`.
   - PATCH with `{name: "renamed"}` (no `config`) preserves the encrypted password.
   - PATCH with `{config: {url: "...", username: "u"}}` (no password) preserves the encrypted password — this is the headline test.
   - PATCH with `{config: {password: ""}}` clears the password.
   - PATCH with `{config: {password: "new"}}` re-encrypts the new value.
   - Worker `claim-jobs` returns the merged plaintext.
   - Restart the server with master key removed: the encrypted check still loads, but `claim-jobs` skips it with a logged error.

3. **One-shot migration**:
   - Seed a DB with plaintext secrets (current state). Set `SP_ENCRYPTION_MASTER_KEY`. Run `./solidping encrypt-credentials --dry-run` — log the count, no writes. Run for real — re-running is a no-op.

4. **Dashboard smoke**:
   - `make dev`, log in as admin, edit an HTTP check with basic auth. Confirm password field renders as masked, save with no change leaves it intact, save with a new value updates it, save with the field cleared blanks it.

5. **Lint + tests**: `make lint test` clean.

## Future work

- Master-key rotation: `solidping rotate-master-key` reads old + new env vars, re-wraps every per-org DEK. Versioned envelope already supports it.
- Per-org DEK rotation: `solidping rotate-org-key --org=<slug>` re-encrypts every secret in that org. Same envelope versioning supports it.
- KMS-backed KEK provider: pull `EncryptionConfig` behind a small `KEKProvider` interface; AWS-KMS and GCP-KMS impls drop in, the rest of the code is unchanged.
- HTTP `headers` / `body` selective encryption: per-check `secretHeaders: ["Authorization"]` array, splitter walks the headers map.
- Audit log of decrypt events (`who`, `when`, `which check`) — most useful in multi-tenant SaaS.
- Rich dashboard UX for "rotate this credential": today the user has to re-type; tomorrow the dashboard could prompt for a new value without showing the old.

---

## Implementation Plan

1. **Crypto primitive** — create `server/internal/crypto/credentials/` with the AES-256-GCM envelope, `Service` interface, in-memory DEK cache, and `Enabled()` short-circuit. Unit tests for round-trip and tampering.

2. **Config wiring** — add `EncryptionConfig` to `server/internal/config/config.go` (`master_key`, `master_key_file`); env vars `SP_ENCRYPTION_MASTER_KEY`, `SP_ENCRYPTION_MASTER_KEY_FILE`; validate decoded key is 32 bytes; instantiate the credentials service in `services.Registry`; log `WARN` at startup when disabled and any plaintext secret exists, or when encrypted rows exist but no master key is configured.

3. **Schema migration** — `005_credentials_encryption.up.sql` + `down.sql` in both `postgres/migrations/` and `sqlite/migrations/`. Adds nullable `config_private TEXT`, `config_private_keys TEXT` (JSON array) to `checks`, `check_jobs`, plus `settings_private`/`settings_private_keys` on `integration_connections` and `config_private`/`config_private_keys` on `auth_providers`. Update Bun models accordingly.

4. **`SecretFields()` interface** — add the method to `checkerdef.Config`. Implement on all 32 checker config structs (one tiny method per file). Unit-test each. Add `SplitConfig`/`MergeConfig` helpers to `checkerdef`.

5. **Connections + auth-providers secret declaration** — small registry mapping `ConnectionType` → secret keys (slack: `access_token`, discord: `webhook_url`, opsgenie: `api_key`, ntfy: `auth_token`, pushover: `user_key`+`api_token`, …) and similarly for `AuthProvider.Type`.

6. **Write path** — update the create/update services in `handlers/checks`, `handlers/connections`, `handlers/auth_providers`. Implement the PATCH-merge rule. Persist `config` (public), `config_private` (envelope), `config_private_keys` (the list of keys, non-secret). Same for connections and auth providers.

7. **Read path** — update `convertCheckToResponse` + equivalents to return `config` + `configPrivateKeys`, never `config_private`. Update dashboard typings if needed (out of scope here, but flag it for the dash team).

8. **Worker dispatch** — in `handlers/workers/handler.go:ClaimJobs`, decrypt + merge per job before sending; clear `ConfigPrivate` and `Encrypted` on the outbound payload.

9. **Job creation path** — when the scheduler writes new `check_jobs`, copy `config_private` + `config_private_keys` from the source `Check`, set `Encrypted=true`. No re-encryption needed.

10. **One-shot migration command** — `solidping encrypt-credentials` walks every org and every secret-bearing row, encrypts, persists. Idempotent. Auto-run at startup unless `SP_ENCRYPTION_AUTO_MIGRATE=false`.

11. **Export/import** — strip secret keys from export, document on response. No import-side change beyond accepting the stripped shape.

12. **Tests + lint** — unit tests at every layer, integration tests against both Postgres and SQLite via testcontainers, focus on the PATCH-preserves-secret case. `make lint test` clean.

13. **Changelog + docs** — update `CLAUDE.md` (root + server) with the new env vars and the threat-model caveat from the "Honest opinion" section.
