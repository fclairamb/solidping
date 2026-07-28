-- Teardown/parity only — never run in production. Reverses 009_v0_8_0.up.sql.
-- Reverse order: later-appended feature blocks are torn down before the
-- earlier ones they were stacked on top of.

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
