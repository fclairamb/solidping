ALTER TABLE checks                  DROP COLUMN IF EXISTS config_private,
                                    DROP COLUMN IF EXISTS config_private_keys;
ALTER TABLE check_jobs              DROP COLUMN IF EXISTS config_private,
                                    DROP COLUMN IF EXISTS config_private_keys;
ALTER TABLE integration_connections DROP COLUMN IF EXISTS settings_private,
                                    DROP COLUMN IF EXISTS settings_private_keys;
ALTER TABLE organization_providers  DROP COLUMN IF EXISTS metadata_private,
                                    DROP COLUMN IF EXISTS metadata_private_keys;
