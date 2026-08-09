-- Organization owner role (spec 2026-08-08-11).
--
-- Adds `owner` above `admin` in the member-role hierarchy
-- (owner > admin > user > viewer). The org creator becomes its owner, and only
-- an owner may delete the organization or grant/revoke ownership.
--
-- Scratch migration for the in-flight v0.10.0 cycle; it is folded into the
-- consolidated release migration at release time (see wiki/conventions/database.md).

alter table organization_members drop constraint if exists organization_members_role_check;
alter table organization_members
  add constraint organization_members_role_check
  check (role in ('owner', 'admin', 'user', 'viewer'));

comment on column organization_members.role is
  'Role: owner (full access + org deletion + ownership grants), admin (full access), user (read/write), viewer (read-only).';

-- Backfill: every pre-existing org ends up with exactly one owner — its oldest
-- live admin. Orgs that somehow have no admin at all are left alone (there is
-- nobody to promote); orgs that already have an owner are skipped.
update organization_members as m
set role = 'owner',
    updated_at = now()
from (
  select distinct on (om.organization_uid) om.uid
  from organization_members om
  where om.role = 'admin'
    and om.deleted_at is null
    and not exists (
      select 1
      from organization_members owner_m
      where owner_m.organization_uid = om.organization_uid
        and owner_m.role = 'owner'
        and owner_m.deleted_at is null
    )
  order by om.organization_uid, om.created_at asc, om.uid asc
) as oldest
where m.uid = oldest.uid;
