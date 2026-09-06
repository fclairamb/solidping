-- v0.25.0 — the ONE consolidated migration for the (still unreleased) v0.25.0
-- release. 018_v0_24_0 shipped with v0.24.0, so everything this cycle produces
-- lands here, in a single file per dialect, per the repo convention documented
-- in wiki/conventions/database.md.
--
--   SECTION: demo-account   users.demo + checks.created_by

-- ==========================================================================
-- SECTION: demo-account
--
-- The public live demo (spec 2026-09-06-02) is a real organization and a real
-- user on the production server, reached through the ordinary login with a
-- published password. Two columns carry it.
--
-- users.demo marks the shared demo principal. It is a USER-LEVEL capability,
-- not "the account whose email happens to be demo@": the claims minted for it
-- carry the flag, the write guard keys off the flag, and the cleanup job
-- reconciles whatever the flag names. Nothing keys on the email.
--
-- checks.created_by records who created a check. Checks were the one resource
-- with no creator at all (maintenance_window, on_call_schedule, agent and file
-- all carry one), which is what "a demo visitor may edit only what they
-- created" needs. It is deliberately recorded for EVERY creator, not just demo
-- sessions — it is useful audit data in its own right — and stays NULL for the
-- startup job's seeded checks, which is precisely what makes them immutable to
-- a demo session with no "protected" flag anywhere.
--
-- No foreign key on created_by: a check must outlive the account that created
-- it, and users are soft-deleted. The column is a historical attribution, not
-- a live reference.
-- ==========================================================================

alter table users add column if not exists demo boolean not null default false;

--bun:split

alter table checks add column if not exists created_by varchar(36);
