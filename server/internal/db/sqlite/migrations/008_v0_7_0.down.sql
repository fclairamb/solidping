-- Teardown/parity only — never run in production. Reverses 008_v0_7_0.up.sql.

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
