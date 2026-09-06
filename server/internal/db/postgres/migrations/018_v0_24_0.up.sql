-- v0.24.0 — the ONE consolidated migration for the (still unreleased) v0.24.0
-- release. 017_v0_21_0 is the last RELEASED migration (it shipped with v0.22.0
-- and is what every deployed database up to v0.23.1 sits on), so everything
-- this cycle produces lands here, in a single file per dialect, per the repo
-- convention documented in wiki/conventions/database.md.
--
--   SECTION: worker-slug-leading-digit   workers.slug CHECK admits a leading digit

-- ==========================================================================
-- SECTION: worker-slug-leading-digit
--
-- `docker run ghcr.io/fclairamb/solidping` refused to boot roughly three
-- times in four. Docker's default hostname is the 12-character hex container
-- ID, 10 of its 16 possible first characters are digits, and the original
-- CHECK `^[a-z][a-z0-9-]{2,20}$` — mirrored by config.WorkerSlugPattern,
-- which is what actually rejected it, at startup, before any INSERT —
-- demanded a leading letter (spec 2026-09-05-04).
--
-- Nothing needs that letter. The slug is an opaque upsert key: it is never a
-- DNS label, a Go identifier, or a token in any other grammar that forbids a
-- leading digit. So the constraint is relaxed to `^[a-z0-9][a-z0-9-]{2,20}$`
-- instead of rewriting the hostname into a name the operator never chose.
--
-- Every slug that satisfied the old CHECK satisfies the new one, so no
-- existing row moves and no existing deployment changes identity. Postgres
-- auto-named the inline column CHECK of 001_v0_1_0 `workers_slug_check`.
-- ==========================================================================

alter table workers drop constraint workers_slug_check;

--bun:split

alter table workers add constraint workers_slug_check
  check (slug ~ '^[a-z0-9][a-z0-9-]{2,20}$');
