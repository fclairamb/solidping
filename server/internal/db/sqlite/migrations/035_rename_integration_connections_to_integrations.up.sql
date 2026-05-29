-- migrate: up
-- Rename integration_connections -> integrations and check_connections ->
-- check_channels (connection_uid -> integration_uid). SQLite auto-updates FK
-- references and index column references on RENAME, but index NAMES are not
-- changed by a table rename, so they are dropped and recreated under the new
-- names. See specs/done/2026/05/2026-05-29-01-channels-to-integrations-rename.md
-- (PR-D).

ALTER TABLE integration_connections RENAME TO integrations;
ALTER TABLE check_connections RENAME TO check_channels;
ALTER TABLE check_channels RENAME COLUMN connection_uid TO integration_uid;

-- Recreate the integrations indexes under the new names.
DROP INDEX IF EXISTS idx_integration_connections_org_type;
DROP INDEX IF EXISTS idx_integration_connections_org_default;
CREATE INDEX idx_integrations_org_type ON integrations (organization_uid, type)
    WHERE deleted_at IS NULL;
CREATE INDEX idx_integrations_org_default ON integrations (organization_uid)
    WHERE deleted_at IS NULL AND is_default = 1;

-- Recreate the binding-table indexes under the new names/column.
DROP INDEX IF EXISTS check_connections_check_connection_idx;
DROP INDEX IF EXISTS check_connections_connection_idx;
DROP INDEX IF EXISTS check_connections_org_idx;
CREATE UNIQUE INDEX check_channels_check_integration_idx ON check_channels (check_uid, integration_uid);
CREATE INDEX check_channels_integration_idx ON check_channels (integration_uid);
CREATE INDEX check_channels_org_idx ON check_channels (organization_uid);
