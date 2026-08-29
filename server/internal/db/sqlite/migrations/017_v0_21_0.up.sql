-- v0.21.0 — the ONE consolidated migration for the (still unreleased) v0.21.0
-- release. 016_v0_19_0 is the last RELEASED migration, so everything this
-- cycle produces lands here, in a single file per dialect, per the repo
-- convention documented in wiki/conventions/database.md.
--
--   SECTION: status-page-kiosk-token   status_pages kiosk_token_hash

-- ==========================================================================
-- SECTION: status-page-kiosk-token
--
-- SQLite half of the PostgreSQL migration of the same name — see
-- postgres/migrations/017_v0_21_0.up.sql for the full rationale. In short: a
-- TV wallboard needs to render a non-public page unattended for months, which
-- neither the 12 h unlock cookie nor `private` (404 everywhere) can do. The
-- column stores the sha256 hex of a 32-byte CSPRNG token; NULL means no token.
--
-- A plain ADD COLUMN, not a table rebuild: the column is nullable with no
-- default and no constraint, so nothing about the existing shape changes.
-- ==========================================================================

alter table status_pages add column kiosk_token_hash text; -- sha256 hex of the page kiosk token (spec 2026-08-29-08). NULL = no token
