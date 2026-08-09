-- Teardown/parity only — never run in production. Reverses 012_org_profile.up.sql.

drop index if exists organization_previous_slugs_organization_idx;
drop index if exists organization_previous_slugs_slug_idx;
drop table if exists organization_previous_slugs;

alter table organizations drop column if exists logo_file_uid;
alter table organizations drop column if exists logo_url;


-- --- from 011_owner_role ---------------------------------------------------

-- Teardown/parity only — never run in production. Reverses 011_owner_role.up.sql.

update organization_members set role = 'admin' where role = 'owner';

alter table organization_members drop constraint if exists organization_members_role_check;
alter table organization_members
  add constraint organization_members_role_check
  check (role in ('admin', 'user', 'viewer'));

comment on column organization_members.role is
  'Role: admin (full access), user (read/write), viewer (read-only).';


-- --- original 010 ----------------------------------------------------------

-- Teardown/parity only — never run in production. Reverses 010_v0_10_0.up.sql.

-- reverse OAuth 2.0 device authorization grant (spec 2026-08-08-02)
drop table if exists device_auth_requests;
