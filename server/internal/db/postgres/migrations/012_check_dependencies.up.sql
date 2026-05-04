-- Check dependencies enable rollup of cascading failures: when a hard
-- parent is down, the child's incident is suppressed for paging purposes
-- and attributed to the root cause. Soft edges are informational only.

CREATE TABLE check_dependencies (
  uid              uuid PRIMARY KEY,
  organization_uid uuid NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  parent_check_uid uuid NOT NULL REFERENCES checks(uid) ON DELETE CASCADE,
  child_check_uid  uuid NOT NULL REFERENCES checks(uid) ON DELETE CASCADE,
  kind             text NOT NULL CHECK (kind IN ('hard', 'soft')),
  description      text NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  deleted_at       timestamptz NULL,
  CONSTRAINT check_dependencies_no_self CHECK (parent_check_uid <> child_check_uid),
  CONSTRAINT check_dependencies_unique_edge UNIQUE (parent_check_uid, child_check_uid)
);

CREATE INDEX idx_check_dependencies_child
  ON check_dependencies (child_check_uid)
  WHERE deleted_at IS NULL;

CREATE INDEX idx_check_dependencies_parent
  ON check_dependencies (parent_check_uid)
  WHERE deleted_at IS NULL;

CREATE INDEX idx_check_dependencies_org
  ON check_dependencies (organization_uid)
  WHERE deleted_at IS NULL;

ALTER TABLE incidents
  ADD COLUMN caused_by_incident_uid uuid NULL
    REFERENCES incidents(uid) ON DELETE SET NULL,
  ADD COLUMN paging_suppressed boolean NOT NULL DEFAULT FALSE;

CREATE INDEX idx_incidents_caused_by
  ON incidents (caused_by_incident_uid)
  WHERE caused_by_incident_uid IS NOT NULL AND deleted_at IS NULL;

COMMENT ON COLUMN incidents.caused_by_incident_uid IS
  'Root-cause incident this one was rolled up under at open time.';
COMMENT ON COLUMN incidents.paging_suppressed IS
  'TRUE when notifications/escalation must skip; flips back to FALSE on parent resolve if still down.';
