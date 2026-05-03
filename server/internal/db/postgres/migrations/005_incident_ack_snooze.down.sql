DROP INDEX IF EXISTS idx_jobs_incident_uid_pending;
DROP INDEX IF EXISTS idx_incidents_snoozed_until;

ALTER TABLE incidents
  DROP COLUMN IF EXISTS resolution_type,
  DROP COLUMN IF EXISTS resolved_by,
  DROP COLUMN IF EXISTS snooze_reason,
  DROP COLUMN IF EXISTS snoozed_by,
  DROP COLUMN IF EXISTS snoozed_until;
