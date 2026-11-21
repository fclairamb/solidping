-- v0.53.0 — sqlite migration.
--
--   SECTION: apply-all-kinds   integrations / maintenance_windows slug, managed_by on three tables
--   SECTION: oidc-only-mode    users.super_admin_source
--
-- ==========================================================================
-- SECTION: apply-all-kinds
-- ==========================================================================
-- `sp apply` now reconciles channels, maintenance windows and status pages as
-- well as checks (spec 2026-11-21-01). A channel and a maintenance window get
-- an optional, per-organization-unique `slug` (the manifest key; a row
-- without one is never touched by apply). All three tables get `managed_by`,
-- the manifest name that owns the row: prune only deletes rows whose
-- `managed_by` equals the manifest. Status pages already have a slug.
alter table integrations add column slug varchar(100);

--bun:split

alter table integrations add column managed_by varchar(255);

--bun:split

alter table maintenance_windows add column slug varchar(100);

--bun:split

alter table maintenance_windows add column managed_by varchar(255);

--bun:split

alter table status_pages add column managed_by varchar(255);

--bun:split

create unique index if not exists idx_integrations_org_slug
    on integrations (organization_uid, slug)
    where slug is not null and deleted_at is null;

--bun:split

create unique index if not exists idx_maintenance_windows_org_slug
    on maintenance_windows (organization_uid, slug)
    where slug is not null and deleted_at is null;

--bun:split

-- ==========================================================================
-- SECTION: oidc-only-mode
-- ==========================================================================
-- Who granted users.super_admin (spec 2026-11-21-02). NULL: granted locally
-- (seeded, or by a super admin), never touched by an IdP claim. 'oidc':
-- granted by the OIDC super-admin group claim, and revoked by the next login
-- that no longer carries the group.
alter table users add column super_admin_source varchar(20);
