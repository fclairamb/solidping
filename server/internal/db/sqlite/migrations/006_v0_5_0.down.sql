-- Teardown/parity only — never run in production. Reverses 006_v0_5_0.up.sql
-- in the opposite order it was applied: (3) escalation default, then
-- (2) deported agents, then (1) aggregation hardening (not reversible —
-- dropped duplicate rows are gone, but the schema shape is restored).

-- ============================================================
-- reverse (3) org-level default escalation policy
-- ============================================================
alter table organizations drop column default_escalation_policy_uid;

-- ============================================================
-- reverse (2) deported (org-scoped) check agents over WebSocket
-- ============================================================
alter table checks drop column config_sealed;
alter table check_jobs drop column config_sealed;

alter table workers add column token text;
create index idx_workers_token on workers (token);

drop table if exists agent_enrollment_tokens;
drop table if exists agents;

-- ============================================================
-- reverse (1) aggregation hardening
-- ============================================================
drop index if exists results_aggregated_unique_idx;

create unique index results_aggregated_unique_idx
  on results (organization_uid, check_uid, region, period_type, period_start)
  where period_type != 'raw';
