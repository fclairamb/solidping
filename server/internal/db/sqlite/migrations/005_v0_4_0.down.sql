-- Reverse of 005_v0_4_0.up.sql: re-add the slug columns and their per-org
-- uniqueness. Teardown/parity only — never run in production. The original
-- slug values are gone, so backfill from uid (globally unique, so per-org
-- unique too) to satisfy NOT NULL and the unique index.
alter table escalation_policies add column slug text not null default '';
update escalation_policies set slug = uid;
create unique index escalation_policies_org_slug_idx on escalation_policies (organization_uid, slug);

alter table on_call_schedules add column slug text not null default '';
update on_call_schedules set slug = uid;
create unique index on_call_schedules_org_slug_idx on on_call_schedules (organization_uid, slug);
