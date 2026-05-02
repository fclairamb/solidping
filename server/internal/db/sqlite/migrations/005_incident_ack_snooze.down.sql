DROP INDEX IF EXISTS idx_jobs_incident_uid_pending;
DROP INDEX IF EXISTS idx_incidents_snoozed_until;

ALTER TABLE incidents DROP COLUMN resolution_type;
ALTER TABLE incidents DROP COLUMN resolved_by;
ALTER TABLE incidents DROP COLUMN snooze_reason;
ALTER TABLE incidents DROP COLUMN snoozed_by;
ALTER TABLE incidents DROP COLUMN snoozed_until;
