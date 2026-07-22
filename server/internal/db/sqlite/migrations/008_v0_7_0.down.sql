-- Teardown/parity only — never run in production. Reverses 008_v0_7_0.up.sql.
-- Reverse order: later-appended feature blocks are torn down before the
-- earlier ones they were stacked on top of.

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
