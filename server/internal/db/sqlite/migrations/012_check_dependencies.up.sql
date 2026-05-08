CREATE TABLE check_dependencies (
  uid              text PRIMARY KEY,
  organization_uid text NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  parent_check_uid text NOT NULL REFERENCES checks(uid) ON DELETE CASCADE,
  child_check_uid  text NOT NULL REFERENCES checks(uid) ON DELETE CASCADE,
  kind             text NOT NULL CHECK (kind IN ('hard', 'soft')),
  description      text NULL,
  created_at       text NOT NULL DEFAULT (datetime('now')),
  updated_at       text NOT NULL DEFAULT (datetime('now')),
  deleted_at       text NULL,
  CHECK (parent_check_uid <> child_check_uid),
  UNIQUE (parent_check_uid, child_check_uid)
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
  ADD COLUMN caused_by_incident_uid text NULL
    REFERENCES incidents(uid) ON DELETE SET NULL;

ALTER TABLE incidents
  ADD COLUMN paging_suppressed integer NOT NULL DEFAULT 0;

CREATE INDEX idx_incidents_caused_by
  ON incidents (caused_by_incident_uid)
  WHERE caused_by_incident_uid IS NOT NULL AND deleted_at IS NULL;
