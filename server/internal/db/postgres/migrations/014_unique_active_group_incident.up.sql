-- Enforce at most one active group incident per (organization, check_group)
-- to prevent the race where two concurrent failures each open their own.
CREATE UNIQUE INDEX IF NOT EXISTS uq_active_group_incident
  ON incidents (organization_uid, check_group_uid)
  WHERE state = 1
    AND check_group_uid IS NOT NULL
    AND deleted_at IS NULL;
