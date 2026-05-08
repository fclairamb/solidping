-- SQLite mirror of the postgres org_entitlements migration. Limits and
-- features live inside the `payload` text column (JSON-encoded);
-- absent fields mean "use default". Source / external_ref / expires_at
-- / last_synced_at stay as columns because they are queried or
-- constrained at the SQL level.

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

CREATE TABLE org_entitlement_audits (
  uid              text PRIMARY KEY,
  organization_uid text NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  source           text NOT NULL,
  actor            text NOT NULL,
  before_snapshot  text,
  after_snapshot   text NOT NULL,
  reason           text,
  created_at       text NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX org_entitlement_audits_org_idx
  ON org_entitlement_audits (organization_uid, created_at DESC);
