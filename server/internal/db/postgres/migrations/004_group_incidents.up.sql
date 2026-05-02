-- Group-based incident correlation: tag incidents with their group and
-- track per-member state in a separate table. NULL check_group_uid keeps
-- the existing per-check incident behavior unchanged.
ALTER TABLE incidents
  ADD COLUMN check_group_uid uuid NULL
    REFERENCES check_groups(uid) ON DELETE SET NULL;

CREATE INDEX idx_incidents_active_by_group
  ON incidents (check_group_uid, state)
  WHERE check_group_uid IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE incident_member_checks (
  incident_uid       uuid NOT NULL REFERENCES incidents(uid) ON DELETE CASCADE,
  check_uid          uuid NOT NULL REFERENCES checks(uid) ON DELETE CASCADE,
  joined_at          timestamptz NOT NULL DEFAULT now(),
  first_failure_at   timestamptz NOT NULL,
  last_failure_at    timestamptz NOT NULL,
  last_recovery_at   timestamptz NULL,
  failure_count      integer NOT NULL DEFAULT 1,
  currently_failing  boolean NOT NULL DEFAULT TRUE,
  PRIMARY KEY (incident_uid, check_uid)
);

CREATE INDEX idx_incident_member_checks_check
  ON incident_member_checks (check_uid)
  WHERE currently_failing = TRUE;

COMMENT ON TABLE incident_member_checks IS 'Per-member state inside a group incident.';
COMMENT ON COLUMN incidents.check_group_uid IS 'NULL = traditional per-check incident.';
