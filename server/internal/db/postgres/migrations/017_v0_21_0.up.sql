-- v0.21.0 — the ONE consolidated migration for the (still unreleased) v0.21.0
-- release. 016_v0_19_0 is the last RELEASED migration, so everything this
-- cycle produces lands here, in a single file per dialect, per the repo
-- convention documented in wiki/conventions/database.md.
--
--   SECTION: status-page-kiosk-token   status_pages kiosk_token_hash

-- ==========================================================================
-- SECTION: status-page-kiosk-token
--
-- A wallboard/TV screen has to render a status page unattended, for months.
-- Neither existing access mode can do that: `password` issues a 12 h unlock
-- cookie (somebody re-types the password on the TV every morning), and
-- `private` 404s on every public endpoint. The kiosk token is the third
-- answer — one revocable, long-lived, per-page secret that grants READ-ONLY
-- view of exactly one page.
--
-- Stored HASHED (sha256 hex of the emitted token), never in the clear, for the
-- same reason password_hash is: the column is a credential. sha256 rather than
-- argon2id because the token is 32 bytes of CSPRNG output rather than a
-- human-chosen secret — there is no dictionary to slow down, and the TV polls
-- it every 15-30 s, which an argon2 verification per request would make
-- needlessly expensive.
--
-- NULL means "no kiosk token" — which is the correct default for every
-- existing page, and the state a revoke returns the page to.
-- ==========================================================================

alter table status_pages add column if not exists kiosk_token_hash text;

--bun:split

comment on column status_pages.kiosk_token_hash is
  'sha256 hex of the page kiosk token (spec 2026-08-29-08). NULL = no token. NEVER serialized; reads expose hasKioskToken only.';
