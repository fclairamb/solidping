---
model: opus
effort: xhigh
---

# Check config secrets leak through the API when encryption at rest is disabled

## Problem

The API returns credentials stored in check configs in plaintext. Two live
examples on the dev instance (`/dash0/orgs/default/checks/...`):

- `a347e725-2f89-4069-bc32-ba275df3d681` (type `ssh`): `GET
  /api/v1/orgs/default/checks/{uid}` returns the **full OpenSSH private key**
  in `config.private_key`.
- `4d7dda60-6af0-47dd-bf3e-cf4a996e0495` (type `http`): the response contains
  `config.basicAuth` = `user:password` in cleartext.

Both checkers correctly declare these fields as secrets
(`server/internal/checkers/checkssh/secret_fields.go`,
`server/internal/checkers/checkhttp/config.go:431`). The leak is not a missing
declaration — it is that **API-side redaction is coupled to encryption at
rest**:

1. On write, `applyEncryption`
   ([service.go:3203-3217](server/internal/handlers/checks/service.go:3203))
   hits the plaintext fallback when no `SP_ENCRYPTION_MASTER_KEY` is
   configured: it merges the secret fields **back into the public `Config`
   column** and clears `ConfigPrivateKeys`.
2. On read, `convertCheckToResponse`
   ([service.go:2131-2149](server/internal/handlers/checks/service.go:2131))
   returns `check.Config` verbatim. With encryption enabled the secrets live
   in `ConfigPrivate` and only placeholder key names are returned; with it
   disabled, every GET/LIST response ships the credentials to any org member
   (including `viewer` role), to browser dev tools, proxies, and logs.

The same `!s.creds.Enabled()` plaintext fallback exists for integration
connection settings
([integrations/service.go:505-552](server/internal/handlers/integrations/service.go:505))
— Slack/SMTP/webhook credentials leak the same way through the connections
endpoints when no master key is set.

The intended invariant is already documented in `server/CLAUDE.md` ("The
dashboard never sees secrets") and already implemented unconditionally on one
surface: `stripSecretKeysForExport`
([service.go:2559-2587](server/internal/handlers/checks/service.go:2559))
strips type-declared secret keys from exports regardless of encryption mode.
The GET/LIST path must hold the same invariant.

> Note: the two example checks above exposed what look like real credentials
> (an SSH key for a LAN host, an AWS MQ basic-auth pair). After the fix, those
> should be rotated by the operator; rotation itself is out of scope here.

## Proposal

Make the invariant unconditional and structural: **a secret field declared by
a checker's `SecretFields()` never appears in any API response, in any
storage-encryption mode.** The checkers already identify what is secret
(`credentials.SecretFielder`,
[secret_fields.go:12](server/internal/crypto/credentials/secret_fields.go:12));
the server must enforce the separation even when it cannot encrypt.

1. **Separate storage even without a master key.** In `applyEncryption` (and
   the integrations equivalent), stop merging secrets back into the public
   `Config` column. Always split via `credentials.SplitConfig` and store the
   private side in `config_private` — as a clearly-marked *plaintext envelope*
   variant (e.g. a new envelope version alongside the AES-GCM and age-sealed
   ones in `internal/crypto/credentials/`) when encryption is disabled. Set
   `config_private_keys` in all modes. The claim/dispatch merge
   (`checkjobsvc.MergeJobSecrets`, `DirectBackend.ClaimJobs`) must open the
   plaintext envelope so checks keep executing; the existing rule "decrypt and
   merge only at the claim/dispatch boundary" is unchanged.
2. **Migrate existing mixed rows.** Extend `internal/credmigrate/` (which
   already reconciles plaintext→encrypted when a key appears) with a startup
   pass that, for rows whose public `config` contains keys the checker
   declares secret, moves those values into `config_private` and records
   `config_private_keys`. Covers `checks.config`, `check_jobs.config`, and
   `integration_connections.settings`.
3. **Defense-in-depth redaction at read time.** Even after the storage split,
   make `convertCheckToResponse` (and the integrations response builder) strip
   any key in `SecretFieldsFor(type)` from the outgoing config and union it
   into the returned `configPrivateKeys` — mirroring
   `stripSecretKeysForExport`. This protects not-yet-migrated rows and any
   future write path that forgets the split.
4. **Round-trip must not lose secrets.** With redacted GETs, the dashboard
   edit form sends back configs without secret values; the existing
   preserve-absent-secrets PATCH merge (`credentials.MergePatch`,
   `applyConfigUpdate`) must be verified to apply in plaintext-envelope mode
   too, so saving an untouched form never wipes a working credential. The
   dash0 placeholder-dot rendering driven by `configPrivateKeys` already
   exists for the encrypted mode and should light up unchanged.
5. **Audit all ~40 checkers for undeclared secrets.** Sweep
   `server/internal/checkers/*/config.go` for credential-bearing fields
   (password, token, key, secret, auth, dsn/connection strings…) missing from
   `SecretFields()` — e.g. verify `checkkafka`, `checkgrpc` (TLS client key),
   `checkdocker`, `checkbrowser`/`checkjs` headers, `checkkubernetes` (inline
   declaration). Add a tripwire unit test that scans every registered checker
   config for suspiciously-named fields not declared secret, so a future
   checker can't reintroduce the leak silently.

### Acceptance criteria

- With **no** `SP_ENCRYPTION_MASTER_KEY` set: create an `ssh` check with a
  private key and an `http` check with `basicAuth`; `GET`/`LIST` responses
  contain neither value, both list the keys in `configPrivateKeys`, the checks
  still execute successfully, and a PATCH that omits the secrets preserves
  them (negative-proof tests with a positive control asserting the check ran
  with the credential).
- Pre-existing rows with secrets in the public column are migrated on startup
  and no longer leak, including via `checks` LIST and check-jobs surfaces.
- Same assertions for an integration connection secret (e.g. Slack token).
- Playwright: the dash0 check edit form shows placeholder dots, never the
  secret, in plaintext mode; saving without touching them keeps the check up.

### Decisions

- **The plaintext envelope IS stored in `config_private`** (decided
  2026-07-18). Plaintext mode must not rely on read-time redaction alone: the
  public `config` column never carries secret values, in any mode, and there
  is one code path for split/merge regardless of whether a master key is
  configured. Read-time redaction (point 3) stays as defense-in-depth, not as
  the primary mechanism. Do not descope to read-time-only redaction.

## Implementation Plan

Design seam: a new **plaintext envelope** (v3, alg `"plaintext"`) lives in the
same `config_private` / `settings_private` TEXT column alongside the v1 AES-GCM
and v2 age-sealed envelopes. It needs no master key to open, so every
server-side decrypt seam routes through it transparently.

### Item 1 — Separate storage even without a master key
- `credentials/plaintext_envelope.go`: `SealPlaintext(map)`, `OpenPlaintext(str)`,
  `IsPlaintextEnvelope(str)`, `RequiresKey(str)` (false only for plaintext).
- `credentials/service.go` `DecryptForOrg`: open a plaintext envelope up-front
  (no key needed), even on a disabled service — the single centralized seam so
  every existing `DecryptForOrg` caller (notifications, kubernetes, reseal,
  reconcile, PATCH loaders) transparently handles the new envelope.
- `checks/service.go` `applyEncryption`: drop the "merge secrets back into public
  Config" fallback; when `!creds.Enabled()` and there are private keys (and not
  sealed-only) store a `SealPlaintext` envelope in `ConfigPrivate` and set
  `ConfigPrivateKeys`. Public `Config` never carries secrets.
- `integrations/service.go` `applySettingsEncryption`: same for connection
  settings → `SettingsPrivate` plaintext envelope + `SettingsPrivateKeys`.
- `checkjobsvc.MergeJobSecrets`: open a plaintext envelope before the
  `!creds.Enabled()` gate so cloud/in-process dispatch keeps executing.
- The remaining pre-`Enabled()` gates (`checks.loadDecryptedConfig`,
  `integrations.loadDecryptedSettings`, `job_notification`, `kubernetes.client`)
  gate on `RequiresKey(envelope)` so a plaintext envelope is always openable.
  `ToAgentJob` already omits `ConfigPrivate`, so the plaintext envelope never
  leaves the server; deported agents keep using the sealed blob.

### Item 2 — Migrate existing mixed rows on startup
- `credmigrate`: add a plaintext-mode pass (`RunPlaintext`) that, when creds are
  disabled, splits any public `config` / `settings` carrying declared-secret
  keys into a plaintext envelope across `checks.config`, `check_jobs.config`,
  and `integration_connections.settings`. Wire it into
  `Server.MaybeAutoMigrateEncryption` (which currently early-returns when creds
  disabled).

### Item 3 — Defense-in-depth read-time redaction
- `checks/service.go` `convertCheckToResponse`: strip any `SecretFieldsFor(type)`
  key (and any `ConfigPrivateKeys` entry) from the outgoing config, union the
  removed keys into the returned `configPrivateKeys` — mirrors
  `stripSecretKeysForExport`. Protects not-yet-migrated rows.
- `integrations/service.go` `toResponse` already strips declared-secret keys;
  extend it to union stripped-but-present keys into `settingsPrivateKeys`.

### Item 4 — Round-trip preserves secrets (tested)
- Verify PATCH-omit-secret preserves the plaintext-envelope credential in both
  checks and connections (service tests below), plus a positive control that the
  merged job config actually carries the credential.

### Item 5 — Checker secret-field audit + tripwire
- Audit finding: every genuine credential field across all ~40 checkers is
  already declared secret (`password`, `token`, `secretHeaders`, `basicAuth`,
  `private_key`, `saslPassword`, `authPassword`/`privPassword`). `checkgrpc` has
  no TLS client-key field, `checkdocker`/`checkbrowser` carry no credentials,
  `checkkubernetes` correctly declares none (its creds live on the connection).
- `registry/secret_audit_test.go`: reflect over every `ParseConfig` struct's
  top-level json tags; any name matching a credential pattern
  (`password`/`secret`/`token`/`private_key`/`passphrase`/`apikey`/`basicauth`…)
  must appear in that config's `SecretFields()`.

### Tests / QA
- `credentials`: plaintext round-trip, detection, disabled-service open.
- `checkjobsvc`: MergeJobSecrets opens a plaintext envelope (positive control:
  job.Config carries the credential) with a nil/disabled creds service.
- `checks`: disabled-creds CreateCheck(ssh/http) → no secret in public config or
  GET response, `configPrivateKeys` set; PATCH-omit preserves; not-yet-migrated
  row redacted at read time.
- `integrations`: disabled-creds Slack connection → token absent from response,
  listed in `settingsPrivateKeys`; PATCH-omit preserves; notification send path
  reconstitutes the token.
- `credmigrate`: seed public-secret rows in all 3 tables, run plaintext pass,
  assert public column clean + plaintext envelope written.
- Playwright: SSH private-key check in plaintext mode shows placeholder, never
  the secret; save-without-touch keeps it. Update `check-http-basic-auth.spec.ts`
  whose old `else` branch asserted the now-fixed leak.
