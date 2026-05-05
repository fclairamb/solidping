-- Remove the dead "from" key from email connection settings. Same intent as
-- the postgres companion migration; SQLite stores settings as TEXT JSON, so
-- json_remove is the right tool. Idempotent — re-runs are no-ops.

UPDATE integration_connections
   SET settings = json_remove(settings, '$.from')
 WHERE type = 'email' AND json_extract(settings, '$.from') IS NOT NULL;
