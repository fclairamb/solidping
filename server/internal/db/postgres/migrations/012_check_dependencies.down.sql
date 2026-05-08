DROP INDEX IF EXISTS idx_incidents_caused_by;

ALTER TABLE incidents
  DROP COLUMN IF EXISTS paging_suppressed,
  DROP COLUMN IF EXISTS caused_by_incident_uid;

DROP INDEX IF EXISTS idx_check_dependencies_org;
DROP INDEX IF EXISTS idx_check_dependencies_parent;
DROP INDEX IF EXISTS idx_check_dependencies_child;

DROP TABLE IF EXISTS check_dependencies;
