-- v0.26.0 — the ONE consolidated migration for the (still unreleased) v0.26.0
-- release. 019_v0_25_0 shipped with v0.25.0, so everything this cycle produces
-- lands here, in a single file per dialect, per the repo convention documented
-- in wiki/conventions/database.md.
--
--   SECTION: signup-attribution   users.signup_attribution

-- ==========================================================================
-- SECTION: signup-attribution
--
-- The marketing site forwards the ad click identifier (gclid and friends) and
-- the utm_* campaign tags on every link into the dashboard (spec
-- 2026-09-07-03). Without somewhere to keep them, a paid click can produce an
-- account and nobody can ever tell which campaign paid for it.
--
-- One nullable JSON column, set exactly once at account creation and never
-- updated. NULL is the normal case: an account that did not arrive from a
-- tagged link. JSON rather than seven columns because the shape is owned by
-- the ad networks (a new click-id parameter should not need a migration) and
-- because nothing ever filters on these fields — they are read back one user
-- at a time, for an offline conversion upload or a support question.
--
-- Nothing in here identifies the person: campaign names are chosen by us,
-- the click id is a random token minted by the ad network.
-- ==========================================================================

alter table users add column if not exists signup_attribution jsonb;

--bun:split

comment on column users.signup_attribution is 'Campaign tags and ad click id forwarded by the marketing site at signup. Set once at account creation; NULL when the signup did not come from a tagged link.';
