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

select 1;

--bun:split

-- ==========================================================================
-- SECTION: parameter-key-hyphens
--
-- Restores the pre-v0.28.0 CHECK (no hyphen). This is a NARROWING, so it fails
-- loudly if any org has since created a hyphenated parameter — which is the
-- correct behaviour for a rollback that would otherwise leave the database
-- holding rows its own constraint forbids. Delete or rename those keys first.
-- ==========================================================================

alter table parameters drop constraint if exists parameters_key_check;

--bun:split

alter table parameters
  add constraint parameters_key_check check (key ~ '^[a-z0-9_\.]+$');

--bun:split

-- ==========================================================================
-- SECTION: check-name-backfill
--
-- Deliberately NOT reversed. The up-migration copied each nameless check's
-- slug into its name; nothing records which rows were blank beforehand, and
-- re-blanking every name equal to its slug would wipe names operators chose.
-- A schema rollback does not need it either — the column is unchanged.
-- ==========================================================================

select 1;
