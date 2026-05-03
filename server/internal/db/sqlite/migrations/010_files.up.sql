CREATE TABLE files (
  uid               text PRIMARY KEY,
  organization_uid  text NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  name              text NOT NULL,
  mime_type         text NOT NULL,
  size              integer NOT NULL,
  file_uri          text NOT NULL,
  sha256            text,
  created_by        text REFERENCES users(uid) ON DELETE SET NULL,
  created_at        text NOT NULL DEFAULT (datetime('now')),
  deleted_at        text
);

CREATE INDEX files_org_created_idx
  ON files (organization_uid, created_at DESC)
  WHERE deleted_at IS NULL;
