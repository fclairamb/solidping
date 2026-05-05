-- Per-org entitlements model: limits + boolean features. Owned by an
-- external billing service in SaaS, by org admins in self-hosted. The
-- OSS knows nothing about plan SKUs — only raw numbers and booleans.
-- NULL on any limit means "unlimited"; NULL on any boolean means "use
-- default". Defaults live in code (entitlements.DefaultEntitlements).

CREATE TABLE org_entitlements (
  uid                       uuid PRIMARY KEY,
  organization_uid          uuid NOT NULL UNIQUE REFERENCES organizations(uid) ON DELETE CASCADE,

  max_checks                integer,
  max_members               integer,
  max_status_pages          integer,
  max_check_groups          integer,
  max_maintenance_windows   integer,
  max_connections           integer,
  max_workers               integer,
  max_api_tokens            integer,
  retention_days_raw        integer,
  retention_days_aggregated integer,
  min_check_period_seconds  integer,

  feature_sso               boolean,
  feature_mcp               boolean,
  feature_custom_branding   boolean,
  feature_priority_support  boolean,
  feature_multi_region      boolean,
  feature_advanced_alerts   boolean,

  allowed_check_types       text[],

  source                    text NOT NULL CHECK (source IN
                              ('default','self-hosted','admin','billing-service')),
  display_name              text,
  external_ref              text,
  metadata                  jsonb NOT NULL DEFAULT '{}'::jsonb,

  expires_at                timestamptz,
  last_synced_at            timestamptz,
  created_at                timestamptz NOT NULL DEFAULT now(),
  updated_at                timestamptz NOT NULL DEFAULT now()
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
