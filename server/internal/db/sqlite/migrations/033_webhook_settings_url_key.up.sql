-- migrate: up
-- Rename the legacy webhook_url key to url in integration_connections.settings
-- for rows where type = 'webhook'. SQLite stores settings as TEXT JSON; use
-- json_set + json_remove to copy the value and drop the old key atomically.
UPDATE integration_connections
SET settings = json_remove(
    json_set(settings, '$.url', json_extract(settings, '$.webhook_url')),
    '$.webhook_url'
)
WHERE type = 'webhook'
  AND json_extract(settings, '$.webhook_url') IS NOT NULL
  AND json_extract(settings, '$.webhook_url') <> '';
