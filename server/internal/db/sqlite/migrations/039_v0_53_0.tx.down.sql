-- v0.53.0 — sqlite rollback (parity only; downs are never run in production).

-- SECTION: apply-all-kinds
drop index if exists idx_maintenance_windows_org_slug;
drop index if exists idx_integrations_org_slug;
alter table status_pages drop column managed_by;
alter table maintenance_windows drop column managed_by;
alter table maintenance_windows drop column slug;
alter table integrations drop column managed_by;
alter table integrations drop column slug;

-- SECTION: oidc-only-mode
alter table users drop column super_admin_source;
