-- Teardown/parity half of the consolidated v0.28.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 021_v0_28_0.up.sql.

-- ==========================================================================
-- SECTION: period-below-floor-backfill
--
-- Deliberately NOT reversed, for the same reason check-name-backfill below
-- isn't: the up-migration raised each affected row's period from the flat
-- 1-minute constant to its type's own default, and nothing records which rows
-- were at 1m immediately beforehand versus a value an operator set moments
-- before a rollback. A schema rollback does not need it either — the column
-- is unchanged.
-- ==========================================================================

-- ==========================================================================
-- SECTION: parameter-key-check
--
-- Drops the CHECK by rebuilding the table without it, restoring the laxer
-- pre-v0.28.0 shape (no constraint on `key` at all). The rows the up-migration
-- deleted stay deleted: a down migration cannot invent them back, and they were
-- rows Postgres could never have held.
-- ==========================================================================

PRAGMA foreign_keys=OFF;

--bun:split

create table parameters_old (
  uid               text primary key,
  organization_uid  text references organizations(uid) on delete cascade, -- Owning organization. NULL for system-wide parameters
  key               text not null, -- Dot-separated configuration key (e.g., smtp.host, slack.default_channel)
  value             text not null, -- Configuration value as JSON
  secret            integer, -- Whether this value is sensitive and should be masked in API responses
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

--bun:split

insert into parameters_old (uid, organization_uid, key, value, secret, created_at, updated_at, deleted_at)
select uid, organization_uid, key, value, secret, created_at, updated_at, deleted_at from parameters;

--bun:split

drop table parameters;

--bun:split

alter table parameters_old rename to parameters;

--bun:split

create unique index parameters_org_key_idx on parameters (organization_uid, key)
  where deleted_at is null and organization_uid is not null;

--bun:split

create unique index parameters_system_key_idx on parameters (key)
  where deleted_at is null and organization_uid is null;

--bun:split

PRAGMA foreign_keys=ON;

--bun:split

-- ==========================================================================
-- SECTION: check-name-backfill
--
-- Deliberately NOT reversed. The up-migration copied each nameless check's
-- slug into its name; nothing records which rows were blank beforehand, and
-- re-blanking every name equal to its slug would wipe names operators chose.
-- A schema rollback does not need it either — the column is unchanged.
-- ==========================================================================

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
