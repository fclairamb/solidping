-- Capture structured delivery artifacts per notification attempt (HTTP status
-- code, request URL with secrets/query stripped, capped request/response bodies,
-- duration, allowlisted response headers). Nullable: pre-migration rows and
-- channels that produce no artifacts leave it NULL. No backfill (new attempts
-- only). SQLite stores the JSON blob as text to mirror the Postgres jsonb column.
ALTER TABLE incident_notifications ADD COLUMN delivery_details text; -- JSON delivery artifacts; secrets never stored
