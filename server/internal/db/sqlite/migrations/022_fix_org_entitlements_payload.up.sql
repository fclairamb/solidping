-- The original 016_org_entitlements migration was modified after being applied
-- on some databases. Those databases still have the old column-per-limit schema.
-- This migration drops and recreates the table with the correct payload column.
-- No data migration is needed: the table is always empty at migration time
-- (entitlements are seeded lazily on first access).

DROP TABLE IF EXISTS org_entitlements;

CREATE TABLE org_entitlements (
  uid               text PRIMARY KEY,
  organization_uid  text NOT NULL UNIQUE REFERENCES organizations(uid) ON DELETE CASCADE,

  payload           text NOT NULL DEFAULT '{}',

  external_ref      text,
  expires_at        text,
  last_synced_at    text,
  metadata          text NOT NULL DEFAULT '{}',

  created_at        text NOT NULL DEFAULT (datetime('now')),
  updated_at        text NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX org_entitlements_external_ref_idx
  ON org_entitlements (external_ref);
