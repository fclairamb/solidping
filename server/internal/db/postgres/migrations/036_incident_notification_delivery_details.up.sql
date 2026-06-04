-- Capture structured delivery artifacts per notification attempt (HTTP status
-- code, request URL with secrets/query stripped, capped request/response bodies,
-- duration, allowlisted response headers). Nullable: pre-migration rows and
-- channels that produce no artifacts leave it NULL. No backfill (new attempts
-- only). A single JSON blob avoids per-channel column churn.
ALTER TABLE incident_notifications ADD COLUMN delivery_details jsonb;

comment on column incident_notifications.delivery_details is
    'Structured per-attempt delivery artifacts (status, url, capped bodies, duration); secrets never stored.';
