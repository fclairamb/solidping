-- Teardown/parity half of the consolidated v0.28.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 021_v0_28_0.up.sql.

-- ==========================================================================
-- SECTION: label-key-check
--
-- Restores the laxer pre-v0.28.0 CHECK (`length(key) between 1 and 50`). The
-- data fix-ups the up-migration ran are NOT reversed: the renamed
-- `solidping-managed` rows stay renamed (reverting them would re-break /apply)
-- and the deleted non-conformant rows stay deleted (they are gone; a down
-- migration cannot invent them back). This is a schema rollback, not a data
-- rollback — which is why it is not a production procedure.
-- ==========================================================================

PRAGMA foreign_keys=OFF;

--bun:split

create table labels_old (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization
  key               text not null check (length(key) >= 1 and length(key) <= 50), -- Label key (e.g., environment, team, tier)
  value             text not null check (length(value) <= 200), -- Label value (max 200 characters)
  created_at        text not null default (datetime('now')),
  deleted_at        text
);

--bun:split

insert into labels_old (uid, organization_uid, key, value, created_at, deleted_at)
select uid, organization_uid, key, value, created_at, deleted_at from labels;

--bun:split

drop table labels;

--bun:split

alter table labels_old rename to labels;

--bun:split

create unique index labels_org_key_value_idx on labels (organization_uid, key, value) where deleted_at is null;

--bun:split

create index labels_org_key_idx on labels (organization_uid, key) where deleted_at is null;

--bun:split

PRAGMA foreign_keys=ON;
