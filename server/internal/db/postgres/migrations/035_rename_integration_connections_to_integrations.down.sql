-- migrate: down
-- Reverse 035: integrations -> integration_connections, check_channels ->
-- check_connections, integration_uid -> connection_uid, indexes and FK
-- constraint back to their prior names.

ALTER TABLE check_channels
    RENAME CONSTRAINT check_channels_integration_uid_fkey TO check_connections_connection_uid_fkey;

ALTER INDEX check_channels_org_idx RENAME TO check_connections_org_idx;
ALTER INDEX check_channels_integration_idx RENAME TO check_connections_connection_idx;
ALTER INDEX check_channels_check_integration_idx RENAME TO check_connections_check_connection_idx;

ALTER INDEX idx_integrations_settings_team_id RENAME TO idx_integration_connections_settings_team_id;
ALTER INDEX idx_integrations_org_default RENAME TO idx_integration_connections_org_default;
ALTER INDEX idx_integrations_org_type RENAME TO idx_integration_connections_org_type;

ALTER TABLE check_channels RENAME COLUMN integration_uid TO connection_uid;
ALTER TABLE check_channels RENAME TO check_connections;
ALTER TABLE integrations RENAME TO integration_connections;
