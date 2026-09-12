-- v0.28.0 — the ONE consolidated SQLite migration for the (still unreleased)
-- v0.28.0 release.
--
-- Of the three sections below, the postgres twin (021_v0_28_0) carries the
-- second and the mirror of the third. The first exists precisely to bring SQLite
-- onto a rule Postgres has enforced since its 001 baseline, so replicating it
-- there would be a no-op cluttering bun_migrations; the second is a data
-- backfill and applies to both; the third is the other half of one change — the
-- twin WIDENS a CHECK Postgres already had, this ADDS the widened rule here,
-- where there has never been one.
--
--   SECTION: label-key-check       labels.key / labels.value CHECK parity with Postgres
--   SECTION: check-name-backfill   checks.name = slug where the name is blank
--   SECTION: parameter-key-check   parameters.key CHECK parity with Postgres
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 021 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or delete the SQLite file.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything, so the database would report 021 as
-- applied while the check-name backfill below never ran and its nameless
-- checks stayed nameless. A database that looks correct and is not. See
-- wiki/conventions/database.md, "The unreleased series".

-- ==========================================================================
-- SECTION: label-key-check  (spec 2026-09-10-01)
--
-- Postgres has enforced `key ~ '^[a-z][a-z0-9-]{2,50}$'` on labels.key since
-- the 001 baseline. SQLite's baseline only checked `length(key) between 1 and
-- 50`, so the two backends disagreed about what a label IS: a document with a
-- key like `os`, `1abc` or `k8s.cluster` imported cleanly here and failed with
-- a raw SQLSTATE there. This brings SQLite onto the Postgres rule.
--
-- SQLite has no ALTER COLUMN / DROP CONSTRAINT, so the table is rebuilt with
-- the established *_new pattern (same technique and same FK rationale as
-- 005_v0_4_0 and 009_v0_8_0). labels is FK-referenced by check_labels with ON
-- DELETE CASCADE; with foreign_keys=ON a DROP TABLE on the parent fires that
-- cascade against still-live child rows before the rebuilt table is swapped
-- back in, so the PRAGMA statements are isolated with --bun:split to run on
-- the migration connection in autocommit (a PRAGMA foreign_keys issued inside
-- a transaction is silently a no-op).
--
-- Two data fix-ups run BEFORE the rebuild, because rows violating the new
-- CHECK cannot be carried across it:
--
--   1. `solidping.io/managed` → `solidping-managed`. That is /apply's reserved
--      config-as-code ownership label. Its old spelling carries a dot and a
--      slash, so Postgres has ALWAYS refused it — apply and every importer
--      that stamps it were broken there and only appeared to work against this
--      laxer backend. Renaming keeps the managed scope of existing SQLite
--      deployments intact instead of orphaning every managed check.
--      The rename is skipped for a row that would collide with an already
--      existing `solidping-managed` label of the same value (the unique index
--      is on (organization_uid, key, value)); the leftover is then dropped by
--      step 2, and its check_labels rows fall to the cascade — which is
--      correct, the surviving label carries the identical meaning.
--
--   The value CHECK mirrors Postgres EXACTLY (length <= 200, empty allowed).
--   The stricter "non-empty" rule lives in Go (models.ValidateLabelValue), on
--   purpose: making SQLite refuse what Postgres accepts would just be the same
--   divergence pointing the other way, and would delete existing rows below
--   for a rule Postgres never had.
--
--   2. Anything still non-conformant is deleted, with its check_labels rows
--      going by cascade. These are rows Postgres could never have held and
--      that the Go-level gate (models.ValidateLabels, enforced on every write
--      path as of this release) will never mint again; carrying them would
--      mean keeping the divergence this section exists to close. The blast
--      radius is bounded to SQLite deployments that hand-authored such keys —
--      the dashboard has always refused them.
-- ==========================================================================

update labels
   set key = 'solidping-managed'
 where key = 'solidping.io/managed'
   and not exists (
     select 1 from labels other
      where other.organization_uid = labels.organization_uid
        and other.key = 'solidping-managed'
        and other.value = labels.value
   );

--bun:split

delete from labels
 where not (
   length(key) between 3 and 51
   and key glob '[a-z]*'
   and key not glob '*[^a-z0-9-]*'
   and length(value) <= 200
 );

--bun:split

PRAGMA foreign_keys=OFF;

--bun:split

create table labels_new (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization
  key               text not null constraint labels_key_check check (
                      length(key) between 3 and 51
                      and key glob '[a-z]*'
                      and key not glob '*[^a-z0-9-]*'
                    ), -- Label key (e.g., environment, team, tier). Mirrors the Postgres regex ^[a-z][a-z0-9-]{2,50}$
  value             text not null constraint labels_value_check check (
                      length(value) <= 200
                    ), -- Label value (max 200 characters) — mirrors Postgres exactly
  created_at        text not null default (datetime('now')),
  deleted_at        text
);

--bun:split

insert into labels_new (uid, organization_uid, key, value, created_at, deleted_at)
select uid, organization_uid, key, value, created_at, deleted_at from labels;

--bun:split

drop table labels;

--bun:split

alter table labels_new rename to labels;

--bun:split

create unique index labels_org_key_value_idx on labels (organization_uid, key, value) where deleted_at is null;

--bun:split

create index labels_org_key_idx on labels (organization_uid, key) where deleted_at is null;

--bun:split

PRAGMA foreign_keys=ON;

--bun:split

-- ==========================================================================
-- SECTION: check-name-backfill  (spec 2026-09-11-02)
--
-- Give every nameless check a name, so the org's own export is a document the
-- server can consume.
--
-- `name` was validated as "present" rather than "non-blank", so the API
-- accepted `""` (and a create that never resolved one left it NULL). Neither
-- shape is merely cosmetic: the v2 exporter omits an empty name, and both
-- ValidateDocument and the import path require `name` — so the instance
-- produced a config-as-code file it would itself reject with
-- `missing required key 'name'`. One tracked org hit exactly that and had to
-- drop the offending check out of its manifest.
--
-- The slug is the right fill: it is non-empty by construction, unique in the
-- org, and already the human-facing identifier of the check everywhere a name
-- is absent (see checkDisplayName, which has always fallen back to it). So
-- this changes nothing anyone sees — it writes down what the UI was already
-- displaying.
--
-- NULL is backfilled alongside `''` because it fails in exactly the same way:
-- both export as an absent `name`. Going forward the write paths refuse a
-- blank name on create and on update, so this runs once and never has work
-- again.
-- ==========================================================================

update checks
   set name = slug
 where slug is not null
   and slug <> ''
   and (name is null or trim(name) = '');


--bun:split

-- ==========================================================================
-- SECTION: parameter-key-check  (spec 2026-09-11-03, batch E2E gate)
--
-- Give `parameters.key` the CHECK Postgres has, so the two engines agree about
-- what a parameter key IS.
--
-- This is the divergence that let a feature ship broken. Postgres has enforced
-- `key ~ '^[a-z0-9_\.]+$'` since its 001 baseline; SQLite's baseline enforced
-- nothing at all. So when spec 2026-09-11-03 opened this table to org admins
-- with a key rule that allows hyphens, every test — all SQLite — passed, and
-- every PUT of the feature's own documented example
-- (`usr.sso-authtest-password`) returned a 500 on Postgres. Three review rounds
-- did not catch it, because nothing they could run would.
--
-- The rule added here is the WIDENED one (hyphen included — see the twin's
-- parameter-key-hyphens section), not the historical one: the point is that the
-- two engines end this migration enforcing the same sentence. A future drift
-- then fails on both backends instead of hiding in one.
--
-- SQLite cannot add a CHECK to an existing table, so `parameters` is rebuilt
-- with the established *_new pattern, exactly as the label-key-check section
-- above does (same technique as 005_v0_4_0 and 009_v0_8_0). No table references
-- `parameters`, so there is no cascade to isolate — the PRAGMA statements are
-- kept anyway, split onto the migration connection in autocommit, because the
-- table's own FK to `organizations` makes the rebuild order matter and copying
-- the working pattern is cheaper than reasoning about the difference.
--
-- Non-conformant rows are deleted before the rebuild, because a row violating
-- the new CHECK cannot be carried across it. That is safe here in a way it
-- would not be for an arbitrary table, and the reason is worth stating: a
-- parameter row can hold an organization's wrapped encryption DEK, and deleting
-- that would make every credential it protects unrecoverable. But no key the
-- server ever READS can be non-conformant — every internal key is a dotted
-- snake_case constant (`encryption.dek`, `auth.session.max_duration`), and every
-- org-managed key is `usr.` plus a key that already passed
-- paramkeys.Validate. A non-conformant row is therefore unreachable by every
-- code path in the server: inert data a lax backend accepted, that Postgres
-- could never have held, and that the only remaining writer of arbitrary keys
-- (the super-admin system-parameters route) will hit this CHECK on from now on.
-- Carrying it forward would mean keeping the divergence this section exists to
-- close.
-- ==========================================================================

delete from parameters
 where not (
   length(key) >= 1
   and key not glob '*[^a-z0-9_.-]*'
 );

--bun:split

PRAGMA foreign_keys=OFF;

--bun:split

create table parameters_new (
  uid               text primary key,
  organization_uid  text references organizations(uid) on delete cascade, -- Owning organization. NULL for system-wide parameters
  key               text not null constraint parameters_key_check check (
                      length(key) >= 1
                      and key not glob '*[^a-z0-9_.-]*'
                    ), -- Configuration key. Mirrors the Postgres regex ^[a-z0-9_.\-]+$
  value             text not null, -- Configuration value as JSON
  secret            integer, -- Whether this value is sensitive and should be masked in API responses
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

--bun:split

insert into parameters_new (uid, organization_uid, key, value, secret, created_at, updated_at, deleted_at)
select uid, organization_uid, key, value, secret, created_at, updated_at, deleted_at from parameters;

--bun:split

drop table parameters;

--bun:split

alter table parameters_new rename to parameters;

--bun:split

create unique index parameters_org_key_idx on parameters (organization_uid, key)
  where deleted_at is null and organization_uid is not null;

--bun:split

create unique index parameters_system_key_idx on parameters (key)
  where deleted_at is null and organization_uid is null;

--bun:split

PRAGMA foreign_keys=ON;
