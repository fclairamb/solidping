-- v0.18.0 (third file) — custom-domain lifecycle state machine (spec 2026-08-23-03).
--
-- A THIRD file for the same unreleased release. 015 and 016 are already applied
-- on developer databases and Bun keys applied migrations on the NUMERIC PREFIX
-- only, so appending to either would be silently skipped by every database that
-- recorded it — and `internal/db/migrationguard` would fail the boot on the
-- changed checksum anyway. Two (or three) files sharing a version label is fine;
-- a renumber or an in-place edit is not.
--
--   SECTION: custom-domain-state   status_pages lifecycle columns
--
-- ORDER IS LOAD-BEARING. The .down.sql unwinds in the exact reverse order.

-- ==========================================================================
-- SECTION: custom-domain-state
-- Split "temporarily failing" from "gone", and make recovery possible.
-- ==========================================================================

-- Until now a custom domain had exactly one counter (custom_domain_failures)
-- doing two jobs: "this is flaky right now" and "this domain has been taken
-- away from us". At three consecutive failures the page's verification was
-- cleared and it went dark — permanently, because the sweep only ever demoted.
-- A DNS blip spanning three six-hourly cycles took a customer's status page
-- offline until a human noticed and clicked Verify.
--
-- These columns make the lifecycle explicit and reversible:
--
--   none     no domain configured
--   pending  a domain is set but has never verified (only an operator promotes)
--   active   verified and serving
--   grace    verified, serving, but re-checks are currently failing
--   demoted  hard demotion: verification cleared, not served
--
-- grace KEEPS custom_domain_verified_at set, which is what keeps the page
-- serving through a blip. Hard demotion is what clears it.
alter table status_pages add column if not exists custom_domain_state text not null default 'none';

--bun:split

-- Consecutive SUCCESSFUL re-checks. The mirror image of custom_domain_failures,
-- and the counter re-promotion is earned with: a demoted domain needs several
-- consecutive successes (not one) before it is trusted again.
alter table status_pages add column if not exists custom_domain_successes integer not null default 0;

--bun:split

-- When the domain last entered `grace`. Makes "has stayed unreachable well past
-- the grace window" a readable fact instead of an inference from a counter and
-- a job interval.
alter table status_pages add column if not exists custom_domain_grace_since timestamptz;

--bun:split

-- Human-readable diagnostic from the last re-check: which mode was used, what
-- target was expected, what DNS actually returned, and the lookup error if any.
-- Without it, "verification fails but dig says the CNAME is right" can only be
-- investigated by correlating pod logs with manual dig runs.
alter table status_pages add column if not exists custom_domain_last_check text;

--bun:split

-- Backfill the state of existing rows from the columns that already encode it,
-- so an upgraded installation does not start every configured domain at 'none'.
update status_pages
   set custom_domain_state = case
         when custom_domain is null then 'none'
         when custom_domain_verified_at is not null then 'active'
         when custom_domain_checked_at is not null then 'demoted'
         else 'pending'
       end
 where custom_domain_state = 'none';

--bun:split

comment on column status_pages.custom_domain_state is
  'Custom-domain lifecycle: none | pending | active | grace | demoted. grace keeps serving; only demoted clears custom_domain_verified_at.';
