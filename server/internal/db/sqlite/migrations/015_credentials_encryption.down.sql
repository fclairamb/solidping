-- SQLite < 3.35 doesn't support DROP COLUMN; on >=3.35 the column drops
-- work directly. Each block is in its own statement so partial failure
-- doesn't break the rest.

ALTER TABLE checks                  DROP COLUMN config_private;
ALTER TABLE checks                  DROP COLUMN config_private_keys;

ALTER TABLE check_jobs              DROP COLUMN config_private;
ALTER TABLE check_jobs              DROP COLUMN config_private_keys;

ALTER TABLE integration_connections DROP COLUMN settings_private;
ALTER TABLE integration_connections DROP COLUMN settings_private_keys;

ALTER TABLE organization_providers  DROP COLUMN metadata_private;
ALTER TABLE organization_providers  DROP COLUMN metadata_private_keys;
