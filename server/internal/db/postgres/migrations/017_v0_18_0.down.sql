-- Teardown/parity half of 017_v0_18_0.up.sql — never run in production.
-- Sections appear in the EXACT REVERSE order of the up file.

-- ==========================================================================
-- SECTION: custom-domain-state
-- Teardown half of the custom-domain lifecycle columns (spec 2026-08-23-03).
-- ==========================================================================

-- LOSSY: the grace/demoted distinction and the last diagnostic are dropped. The
-- pre-existing custom_domain_verified_at / custom_domain_failures columns still
-- describe the domain, so a downgraded installation degrades to the old one-way
-- behaviour rather than to a broken one.
alter table status_pages drop column if exists custom_domain_last_check;

--bun:split

alter table status_pages drop column if exists custom_domain_grace_since;

--bun:split

alter table status_pages drop column if exists custom_domain_successes;

--bun:split

alter table status_pages drop column if exists custom_domain_state;
