-- Files: generic content storage seam used by bug reports (screenshots),
-- and reserved for future use cases like status-page snapshot history and
-- result-rollup blobs. The actual bytes live behind a pluggable storage
-- backend referenced by `file_uri` (e.g. file://orgUid/group/fileId,
-- s3://orgUid/group/fileId).

CREATE TABLE files (
  uid               uuid PRIMARY KEY,
  organization_uid  uuid NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  name              text NOT NULL,
  mime_type         text NOT NULL,
  size              bigint NOT NULL,
  file_uri          text NOT NULL,
  sha256            text NULL,
  created_by        uuid NULL REFERENCES users(uid) ON DELETE SET NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz NULL
);

CREATE INDEX files_org_created_idx
  ON files (organization_uid, created_at DESC)
  WHERE deleted_at IS NULL;
