-- Teardown/parity only — never run in production. Reverses 008_v0_7_0.up.sql.
-- Reverse order: later-appended feature blocks are torn down before the
-- earlier ones they were stacked on top of.

-- reverse results table tier-1 storage trim (spec 2026-07-24-02)
-- No backfill is performed or needed: availability_pct is recomputable at any
-- time from successful_checks / total_checks, and last_for_status is
-- repopulated by the next result insert (the feature it fed has been removed
-- anyway).
alter table results add column last_for_status boolean;
alter table results add column availability_pct double precision;

create index idx_results_last_for_status on results (check_uid, status) where last_for_status = true;

comment on column results.last_for_status is 'Marks the most recent result per check+status combination (raw only).';
comment on column results.availability_pct is 'Uptime percentage for this period (aggregated only).';

-- reverse monthly SMS/voice usage counters (spec 2026-07-22-02)
drop table if exists org_usage_counters;

-- reverse phone contact verification (spec 2026-07-22-02)
alter table user_contacts drop column verify_attempts;
alter table user_contacts drop column verify_expires_at;
alter table user_contacts drop column verify_code_hash;

-- reverse custom domains for status pages (spec 2026-07-22-01)
drop index if exists status_pages_custom_domain_idx;
alter table status_pages drop column custom_domain_failures;
alter table status_pages drop column custom_domain_checked_at;
alter table status_pages drop column custom_domain_verified_at;
alter table status_pages drop column custom_domain_token;
alter table status_pages drop column custom_domain;

-- ---------------------------------------------------------------------------
-- (append further v0.7.0 teardown blocks below this line)
-- ---------------------------------------------------------------------------
