-- v0.53.0 — postgres rollback (parity only; downs are never run in production).

-- SECTION: apply-all-kinds
drop index if exists idx_maintenance_windows_org_slug;
drop index if exists idx_integrations_org_slug;
alter table status_pages drop column if exists managed_by;
alter table maintenance_windows drop column if exists managed_by;
alter table maintenance_windows drop column if exists slug;
alter table integrations drop column if exists managed_by;
alter table integrations drop column if exists slug;

-- SECTION: oidc-only-mode
alter table users drop column if exists super_admin_source;
alter table organization_members drop column if exists role_source;
