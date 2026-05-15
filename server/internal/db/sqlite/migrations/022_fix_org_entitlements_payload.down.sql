DROP TABLE IF EXISTS org_entitlements;

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
