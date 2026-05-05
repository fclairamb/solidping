-- Credentials encryption at rest. Each row's secret keys move out of the
-- public `config`/`settings` JSONB column into an opaque envelope in the
-- `*_private` TEXT column. The companion `*_private_keys` column stores a
-- JSON array of the key names so the dashboard can render placeholder hints
-- without decrypting.
--
-- Both private columns are NULL when no secrets are encrypted on the row —
-- distinct from "encryption is disabled at the server" (a server-level
-- fact). Plaintext fallback is intentional for installs without
-- SP_ENCRYPTION_MASTER_KEY.

ALTER TABLE checks
  ADD COLUMN config_private      TEXT NULL,
  ADD COLUMN config_private_keys TEXT NULL;

ALTER TABLE check_jobs
  ADD COLUMN config_private      TEXT NULL,
  ADD COLUMN config_private_keys TEXT NULL;

ALTER TABLE integration_connections
  ADD COLUMN settings_private      TEXT NULL,
  ADD COLUMN settings_private_keys TEXT NULL;

-- The "auth_providers" table mentioned in the spec is named
-- organization_providers in this codebase; OAuth client secrets land in
-- metadata.
ALTER TABLE organization_providers
  ADD COLUMN metadata_private      TEXT NULL,
  ADD COLUMN metadata_private_keys TEXT NULL;
