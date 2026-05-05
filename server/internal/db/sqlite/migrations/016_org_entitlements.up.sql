-- SQLite mirror of the postgres org_entitlements migration. SQLite has no
-- native array type, so allowed_check_types is stored as a JSON-encoded
-- text column; the model code wraps marshal/unmarshal.

CREATE TABLE org_entitlements (
  uid                       text PRIMARY KEY,
  organization_uid          text NOT NULL UNIQUE REFERENCES organizations(uid) ON DELETE CASCADE,

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

  feature_sso               integer,
  feature_mcp               integer,
  feature_custom_branding   integer,
  feature_priority_support  integer,
  feature_multi_region      integer,
  feature_advanced_alerts   integer,

  allowed_check_types       text,

  source                    text NOT NULL CHECK (source IN
                              ('default','self-hosted','admin','billing-service')),
  display_name              text,
  external_ref              text,
  metadata                  text NOT NULL DEFAULT '{}',

  expires_at                text,
  last_synced_at            text,
  created_at                text NOT NULL DEFAULT (datetime('now')),
  updated_at                text NOT NULL DEFAULT (datetime('now'))
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
