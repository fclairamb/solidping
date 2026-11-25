---
model: opus
effort: high
---

# Encrypt the secret system parameters at rest, starting with the JWT signing key

## Problem

Found by the security scan of 2026-11-25 (finding AX1).

- **Severity:** Medium.
- **Who can trigger it:** anyone who obtains a read of the database: a leaked backup or volume snapshot, a support dump, a read-only replica credential. That is exactly the threat the credential envelope was built for. The wiki describes that threat model as "protects against DB theft only" (`wiki/database-model/schema-notes.md:95`).

What the docs promise:

- `web/docs/docs/configuration/security.md:29`: "Because the master key lives outside the database, a database dump alone never reveals plaintext secrets."
- `wiki/features/email-inbox-checks.md:212`: the `email.inbox` `password` "is stored encrypted at rest when `SP_ENCRYPTION_MASTER_KEY` is set".

Neither holds for the system parameters (`parameters` rows with `organization_uid IS NULL`):

- `SetSystemParameter` stores the raw JSON (`server/internal/db/postgres/postgres.go:5300-5337`, `server/internal/db/sqlite/sqlite.go:5246`). `secret=true` is only a display flag: `handlers/system/service.go` `toResponse` masks it in API responses. No encryption happens at any layer.
- `jmap.Manager.loadConfig` (`server/internal/jmap/manager.go:730-741`) decodes `email.inbox`, password included, straight from the row. Nothing decrypts it, so the wiki claim is wrong.
- The credential envelope (`server/internal/crypto/credentials/`) covers only `checks.config`, `check_jobs.config` and `integration_connections.settings`. Encrypting `system_parameters` was put out of scope in spec `specs/done/2026/05/2026-05-02-18-credentials-encryption.md:65`, as a follow-up. No follow-up spec was ever written.

The secret-flagged system keys in `server/internal/systemconfig/systemconfig.go` (`Secret: true`):

- `auth.jwt_secret`;
- the email (SMTP) password;
- the Google, GitHub, GitLab, Microsoft, Slack, Discord and OIDC client secrets;
- the Slack signing secret and app token, the Discord bot token, the MS Teams app secret;
- the LDAP bind password, the PostHog personal API key, the metrics scrape token.

`email.inbox` also carries a password inside its JSON object.

Impact. The worst case is the JWT signing key:

- When `SP_AUTH_JWT_SECRET` is unset or a placeholder, `ensureJWTSecret` generates the key and persists it in plaintext (`server/internal/systemconfig/systemconfig.go:1667-1711`). That is the default for a deployment that never set the variable.
- Bearer tokens are verified statelessly with that key (`handlers/auth/service.go` `ValidateToken`, HS256). The purpose keys are HKDF-derived from it.
- So a DB read alone lets the attacker sign a valid access token for any user uid in the dump, super admin included, and take over the running instance. That defeats the "DB theft" protection the per-org DEKs give the check and integration secrets.
- The SSO client secrets, the LDAP bind password and the SMTP password leak the same way, with no forging needed.

## Proposal

1. Add a system data key: a random DEK wrapped by the KEK like the per-org ones, stored as its own `parameters` row (for example `encryption.system_dek`, `secret=true`).
   - Reuse the envelope and versioning of `server/internal/crypto/credentials/`.
   - Do not invent a second scheme.
2. Encrypt and decrypt at one choke point, so none of the ~40 system-parameter readers changes. The readers are in `systemconfig`, `handlers/system`, `servicesig`, `middleware/auth.go`, `jmap`, `opsnotify` and others.
   - Recommended: a decorator around the system-parameter methods of `db.Service`, installed at startup when a master key is configured.
   - On write (`SetSystemParameter` with `secret=true`), it seals `value` into an envelope.
   - On read (`GetSystemParameter` / `ListSystemParameters`), it opens it.
   - For `email.inbox`, seal the `password` field (or the whole value) the same way.
3. Startup migration, in the same pass as `SP_ENCRYPTION_AUTO_MIGRATE`: seal every plaintext secret-flagged system row, the generated `auth.jwt_secret` included. Keep reading plaintext rows during the transition.
   - A sealed row that cannot be opened (no master key on this process, wrong key) fails the boot with a message naming the fix.
   - It must never fall back to generating a new JWT secret. That would silently log everyone out on one replica and not the others.
4. With no master key configured, keep today's behaviour (the documented plaintext fallback). Add `auth.jwt_secret` to the startup `WARN` listing plaintext secrets, and recommend setting `SP_AUTH_JWT_SECRET` or a master key.
5. Correct the docs:
   - `security.md`: say which values the envelope covers and that `SP_AUTH_JWT_SECRET` from the environment keeps the signing key out of the database.
   - `email-inbox-checks.md:212`: true once this ships.

## Tests

- systemconfig / db (both dialects): with a master key, `ensureJWTSecret` on an empty DB stores a row whose raw `value` does not contain the generated secret. Read the row with a plain SELECT, bypassing the decorator. The config still ends up with that secret. This fails before the fix.
- Same test for `SetSystemParameter("auth.oidc.client_secret", …, true)` and for `email.inbox` with a password. The raw rows hold no plaintext, and `GetSystemParameter` / `jmap` `loadConfig` return the plaintext.
- Migration: a pre-existing plaintext `auth.jwt_secret` row is sealed at startup, and tokens signed before the restart still validate after it.
- Positive control: a non-secret system parameter (`secret=false`, for example `auth.local_login.enabled`) is stored and read back unchanged, as plain JSON.
- A sealed row with the master key removed fails the boot with an explicit error, and does not generate a new JWT secret.

## Open questions

- Should the JWT secret move out of the DB entirely (always env or file, generated once into a mounted file) instead of being sealed? Recommended: seal it as above. It keeps zero-config installs working, and the env variable still wins.
