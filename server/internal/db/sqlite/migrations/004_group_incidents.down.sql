DROP INDEX IF EXISTS idx_incident_member_checks_check;
DROP TABLE IF EXISTS incident_member_checks;
DROP INDEX IF EXISTS idx_incidents_active_by_group;
ALTER TABLE incidents DROP COLUMN check_group_uid;
