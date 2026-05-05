-- Credentials encryption at rest. Mirrors the postgres migration; sqlite
-- requires one ADD COLUMN per ALTER statement.

ALTER TABLE checks                  ADD COLUMN config_private      TEXT;
ALTER TABLE checks                  ADD COLUMN config_private_keys TEXT;

ALTER TABLE check_jobs              ADD COLUMN config_private      TEXT;
ALTER TABLE check_jobs              ADD COLUMN config_private_keys TEXT;

ALTER TABLE integration_connections ADD COLUMN settings_private      TEXT;
ALTER TABLE integration_connections ADD COLUMN settings_private_keys TEXT;

-- The "auth_providers" table mentioned in the spec is named
-- organization_providers in this codebase; OAuth client secrets land in
-- metadata.
ALTER TABLE organization_providers  ADD COLUMN metadata_private      TEXT;
ALTER TABLE organization_providers  ADD COLUMN metadata_private_keys TEXT;
