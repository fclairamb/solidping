-- v0.26.0 — SQLite mirror of the ONE consolidated migration for the (still
-- unreleased) v0.26.0 release. See postgres/migrations/020_v0_26_0.up.sql for
-- the full rationale of each section.
--
--   SECTION: signup-attribution   users.signup_attribution

-- ==========================================================================
-- SECTION: signup-attribution
--
-- SQLite has no jsonb type; like users.totp_recovery_codes this is a text
-- column holding a JSON document. Nullable, which is the only shape SQLite's
-- ALTER TABLE accepts on a populated table anyway.
-- ==========================================================================

alter table users add column signup_attribution text;
