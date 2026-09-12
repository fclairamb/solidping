-- v0.28.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.28.0 release. 020_v0_26_0 shipped earlier, so everything this cycle
-- produces lands here, in a single file per dialect, per the repo convention
-- documented in wiki/conventions/database.md.
--
-- The SQLite twin (021_v0_28_0) carries one EXTRA section, label-key-check:
-- it brings SQLite onto a labels.key rule Postgres has enforced since its 001
-- baseline, so there is nothing to do here for it.
--
--   SECTION: check-name-backfill   checks.name = slug where the name is blank
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 021 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or drop and recreate the database.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything, so the database would report 021 as
-- applied while the check-name backfill below never ran and its nameless
-- checks stayed nameless. A database that looks correct and is not. See
-- wiki/conventions/database.md, "The unreleased series".

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
   and (name is null or btrim(name) = '');
