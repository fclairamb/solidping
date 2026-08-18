drop index if exists idx_results_lifecycle_pending;

--bun:split

alter table results drop column if exists abandoned;
