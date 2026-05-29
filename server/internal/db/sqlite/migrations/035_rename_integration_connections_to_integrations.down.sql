-- migrate: down
-- Reverse 035 for SQLite: integrations -> integration_connections,
-- check_channels -> check_connections, integration_uid -> connection_uid, and
-- the indexes back to their prior names.

DROP INDEX IF EXISTS check_channels_check_integration_idx;
DROP INDEX IF EXISTS check_channels_integration_idx;
DROP INDEX IF EXISTS check_channels_org_idx;
DROP INDEX IF EXISTS idx_integrations_org_type;
DROP INDEX IF EXISTS idx_integrations_org_default;

ALTER TABLE check_channels RENAME COLUMN integration_uid TO connection_uid;
ALTER TABLE check_channels RENAME TO check_connections;
ALTER TABLE integrations RENAME TO integration_connections;

CREATE INDEX idx_integration_connections_org_type ON integration_connections (organization_uid, type)
    WHERE deleted_at IS NULL;
CREATE INDEX idx_integration_connections_org_default ON integration_connections (organization_uid)
    WHERE deleted_at IS NULL AND is_default = 1;

CREATE UNIQUE INDEX check_connections_check_connection_idx ON check_connections (check_uid, connection_uid);
CREATE INDEX check_connections_connection_idx ON check_connections (connection_uid);
CREATE INDEX check_connections_org_idx ON check_connections (organization_uid);
