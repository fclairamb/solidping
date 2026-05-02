-- Acknowledgment / snooze / manual-resolve metadata for incidents.
-- All nullable so existing rows remain valid; absence means "no ack / no
-- snooze / auto-resolved" depending on column.
ALTER TABLE incidents
  ADD COLUMN snoozed_until    timestamptz NULL,
  ADD COLUMN snoozed_by       text NULL,
  ADD COLUMN snooze_reason    text NULL,
  ADD COLUMN resolved_by      text NULL,
  ADD COLUMN resolution_type  text NULL;

COMMENT ON COLUMN incidents.snoozed_until IS 'NULL = not snoozed. Sweeper unsnoozes when NOW() > snoozed_until.';
COMMENT ON COLUMN incidents.resolution_type IS 'auto | manual | expired. NULL until resolved_at is set.';

-- Speeds up the auto-unsnooze sweeper, which scans active incidents whose
-- snooze window has passed.
CREATE INDEX idx_incidents_snoozed_until
  ON incidents (snoozed_until)
  WHERE snoozed_until IS NOT NULL AND deleted_at IS NULL;

-- Used by the cancellation sweep that fires whenever an incident is
-- ack'd / snoozed / resolved. The job table stores the incident UID inside
-- its JSONB config under the "incident_uid" key for notification jobs.
CREATE INDEX idx_jobs_incident_uid_pending
  ON jobs ((config->>'incident_uid'))
  WHERE status IN ('pending', 'running')
    AND config ? 'incident_uid'
    AND deleted_at IS NULL;
