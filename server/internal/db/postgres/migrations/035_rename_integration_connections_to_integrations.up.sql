-- migrate: up
-- Rename integration_connections -> integrations (the umbrella entity) and the
-- binding table check_connections -> check_channels (the notify role), with the
-- FK column connection_uid -> integration_uid. Indexes and the FK constraint
-- are renamed to match. See
-- specs/done/2026/05/2026-05-29-01-channels-to-integrations-rename.md (PR-D).

ALTER TABLE integration_connections RENAME TO integrations;
ALTER TABLE check_connections RENAME TO check_channels;
ALTER TABLE check_channels RENAME COLUMN connection_uid TO integration_uid;

-- Rename indexes on the renamed integrations table.
ALTER INDEX idx_integration_connections_org_type RENAME TO idx_integrations_org_type;
ALTER INDEX idx_integration_connections_org_default RENAME TO idx_integrations_org_default;
ALTER INDEX idx_integration_connections_settings_team_id RENAME TO idx_integrations_settings_team_id;

-- Rename indexes on the renamed binding table.
ALTER INDEX check_connections_check_connection_idx RENAME TO check_channels_check_integration_idx;
ALTER INDEX check_connections_connection_idx RENAME TO check_channels_integration_idx;
ALTER INDEX check_connections_org_idx RENAME TO check_channels_org_idx;

-- Realign the FK constraint name to the new column/table. Postgres carries the
-- constraint through the table/column rename, but the name still references the
-- old column; rename it so future inspection matches the schema.
ALTER TABLE check_channels
    RENAME CONSTRAINT check_connections_connection_uid_fkey TO check_channels_integration_uid_fkey;
