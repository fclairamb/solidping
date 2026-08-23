-- v0.18.0 (third file) — custom-domain lifecycle state machine (spec 2026-08-23-03).
--
-- SQLite mirror of postgres/migrations/017_v0_18_0.up.sql. See the Postgres file
-- for what each column means and for why this is a THIRD file rather than an
-- append to 015/016 (Bun keys applied migrations on the numeric prefix only).
--
--   SECTION: custom-domain-state   status_pages lifecycle columns
--
-- Dialect differences: timestamptz -> text, no `comment on column`, and SQLite's
-- ALTER TABLE ADD COLUMN has no IF NOT EXISTS — the column is new in this
-- release, so a plain ADD is correct and any re-run would be a bug elsewhere.

-- ==========================================================================
-- SECTION: custom-domain-state
-- Split "temporarily failing" from "gone", and make recovery possible.
-- ==========================================================================

alter table status_pages add column custom_domain_state text not null default 'none';

--bun:split

alter table status_pages add column custom_domain_successes integer not null default 0;

--bun:split

alter table status_pages add column custom_domain_grace_since text;

--bun:split

alter table status_pages add column custom_domain_last_check text;

--bun:split

update status_pages
   set custom_domain_state = case
         when custom_domain is null then 'none'
         when custom_domain_verified_at is not null then 'active'
         when custom_domain_checked_at is not null then 'demoted'
         else 'pending'
       end
 where custom_domain_state = 'none';
