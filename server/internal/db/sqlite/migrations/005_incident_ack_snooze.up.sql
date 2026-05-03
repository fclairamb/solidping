-- Acknowledgment / snooze / manual-resolve metadata for incidents.
-- All nullable so existing rows remain valid.
ALTER TABLE incidents ADD COLUMN snoozed_until text;
ALTER TABLE incidents ADD COLUMN snoozed_by text;
ALTER TABLE incidents ADD COLUMN snooze_reason text;
ALTER TABLE incidents ADD COLUMN resolved_by text;
ALTER TABLE incidents ADD COLUMN resolution_type text;

CREATE INDEX idx_incidents_snoozed_until
  ON incidents (snoozed_until)
  WHERE snoozed_until IS NOT NULL AND deleted_at IS NULL;

-- SQLite stores config as JSON text; json_extract is the equivalent of
-- Postgres' ->> operator. Notification jobs serialize the incident UID
-- under the "incidentUid" key (NotificationJobConfig).
CREATE INDEX idx_jobs_incident_uid_pending
  ON jobs (json_extract(config, '$.incidentUid'))
  WHERE status IN ('pending', 'running')
    AND deleted_at IS NULL;
