-- Teardown/parity only — never run in production. Reverses 011_owner_role.up.sql.

update organization_members set role = 'admin' where role = 'owner';

alter table organization_members drop constraint if exists organization_members_role_check;
alter table organization_members
  add constraint organization_members_role_check
  check (role in ('admin', 'user', 'viewer'));

comment on column organization_members.role is
  'Role: admin (full access), user (read/write), viewer (read-only).';
