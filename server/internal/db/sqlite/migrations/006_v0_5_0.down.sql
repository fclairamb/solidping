-- Reverse of 006_v0_5_0.up.sql: restore the region-based unique index.
-- Teardown/parity only — never run in production. The dedupe is not reversible
-- (dropped duplicate rows are gone), but the schema shape is restored.
drop index if exists results_aggregated_unique_idx;

create unique index results_aggregated_unique_idx
  on results (organization_uid, check_uid, region, period_type, period_start)
  where period_type != 'raw';
