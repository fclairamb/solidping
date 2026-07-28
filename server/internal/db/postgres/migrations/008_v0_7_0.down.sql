-- Teardown/parity only — never run in production. Reverses 008_v0_7_0.up.sql.
-- Reverse order: later-appended feature blocks are torn down before the
-- earlier ones they were stacked on top of.

-- reverse per-status-page custom CSS (spec 2026-07-27-02)
alter table status_pages drop column custom_css;

-- reverse platform-operated "system agents" (spec 2026-07-27-01)
-- System agents have no organization, so they cannot survive the NOT NULL
-- restore — they are dropped along with their tokens.
drop table if exists agent_nonces;

alter table agent_enrollment_tokens drop constraint if exists agent_enrollment_tokens_one_shot_check;
alter table agent_enrollment_tokens drop constraint if exists agent_enrollment_tokens_kind_org_check;
alter table agent_enrollment_tokens drop constraint if exists agent_enrollment_tokens_kind_check;
delete from agent_enrollment_tokens where kind = 'system';
alter table agent_enrollment_tokens drop column if exists use_count;
alter table agent_enrollment_tokens drop column if exists max_uses;
alter table agent_enrollment_tokens drop column if exists kind;
alter table agent_enrollment_tokens alter column organization_uid set not null;

alter table agents drop constraint if exists agents_kind_org_check;
alter table agents drop constraint if exists agents_kind_check;
delete from agents where kind = 'system';
drop index if exists idx_agents_kind_region;
alter table agents drop column if exists kind;
alter table agents alter column organization_uid set not null;

-- reverse in-server ACME/TLS asset storage (spec 2026-07-26-01)
-- Dropping tls_storage discards every issued certificate and account key; the
-- next start simply re-issues on demand.
drop table if exists tls_storage_locks;
drop table if exists tls_storage;

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
