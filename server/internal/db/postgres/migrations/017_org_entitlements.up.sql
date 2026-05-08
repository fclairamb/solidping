-- Per-org entitlements model: limits + features stored as a single
-- versioned JSONB payload. The struct
-- (`models.EntitlementsPayload`) is the schema; absent fields mean
-- "use default". Source / external_ref / expires_at / last_synced_at
-- stay as columns because they are queried, indexed, or constrained
-- at the SQL level.

CREATE TABLE org_entitlements (
  uid               uuid PRIMARY KEY,
  organization_uid  uuid NOT NULL UNIQUE REFERENCES organizations(uid) ON DELETE CASCADE,

  payload           jsonb NOT NULL DEFAULT '{}'::jsonb,

  external_ref      text,
  expires_at        timestamptz,
  last_synced_at    timestamptz,
  metadata          jsonb NOT NULL DEFAULT '{}'::jsonb,

  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX org_entitlements_external_ref_idx
  ON org_entitlements (external_ref) WHERE external_ref IS NOT NULL;

-- Audit log: one row per write. Cheap insurance against billing disputes
-- and useful for debugging the sync. Pruning is handled by the existing
-- aggregation/cleanup runner; retention configurable via system parameter.
CREATE TABLE org_entitlement_audits (
  uid              uuid PRIMARY KEY,
  organization_uid uuid NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  source           text NOT NULL,
  actor            text NOT NULL,
  before_snapshot  jsonb,
  after_snapshot   jsonb NOT NULL,
  reason           text,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX org_entitlement_audits_org_idx
  ON org_entitlement_audits (organization_uid, created_at DESC);
