-- Teardown/parity half of the consolidated v0.17.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of 014_v0_17_0.up.sql,
-- so each one unwinds on a schema that still has everything the sections above
-- it created (on SQLite in particular, slo-reporting drops its `results`
-- columns before results-status-domain rebuilds the table without them).
--
-- Several sections are lossy on the way down; each says so in its own note.

-- ==========================================================================
-- SECTION: dangling-notification-routes
-- Was scratch migration 019_dangling_notification_routes (spec 2026-08-20-02). Teardown half.
-- ==========================================================================

-- Deliberately a no-op, and the FIRST section here because the down file
-- unwinds in reverse order.
--
-- The up half is a pure data cleanup: it deletes notification-route rows whose
-- contact no longer exists. There is nothing to restore — the rows carried no
-- information beyond "this dead contact once had a route", they were never
-- reachable through the API, and re-creating them would put the ghost rows
-- back on every affected user's notifications page. Down migrations undo
-- SCHEMA, not garbage collection.

select 1;

--bun:split

-- ==========================================================================
-- SECTION: slo-reporting
-- Was scratch migration 018_slo_reporting (spec 2026-08-20-01). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. Reverses
-- the slo-reporting section of 014_v0_17_0.up.sql.

drop table if exists report_schedules;

--bun:split

drop table if exists slos;

--bun:split

alter table results drop column if exists maintenance_successful_checks;

--bun:split

alter table results drop column if exists maintenance_checks;

--bun:split

alter table results drop column if exists maintenance;

--bun:split

-- ==========================================================================
-- SECTION: incident-publications
-- Was scratch migration 017_incident_publications (spec 2026-08-19-08). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. Reverses
-- the incident-publications section of 014_v0_17_0.up.sql.

drop index if exists idx_status_updates_publication;

--bun:split

alter table status_updates drop column if exists incident_publication_uid;

--bun:split

-- Rows created by the auto-publish pipeline have no author; the column cannot
-- go back to NOT NULL while they exist, so they are dropped first.
delete from status_updates where author_uid is null;

--bun:split

alter table status_updates alter column author_uid set not null;

--bun:split

alter table status_page_resources drop column if exists auto_publish;

--bun:split

alter table status_pages drop constraint if exists status_pages_auto_resolve_valid;

--bun:split

alter table status_pages drop constraint if exists status_pages_auto_publish_delay_nonneg;

--bun:split

alter table status_pages drop column if exists auto_resolve;

--bun:split

alter table status_pages drop column if exists auto_publish_delay_seconds;

--bun:split

alter table status_pages drop column if exists auto_publish;

--bun:split

drop table if exists incident_publications;

--bun:split

-- ==========================================================================
-- SECTION: worker-version
-- Was scratch migration 016_worker_version (spec 2026-08-19-07). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. Reverses
-- the worker-version section of 014_v0_17_0.up.sql.

alter table workers drop constraint if exists workers_version_not_empty;

--bun:split

alter table workers drop column if exists version;

--bun:split

-- ==========================================================================
-- SECTION: remove-opsgenie-integrations
-- Was scratch migration 015_remove_opsgenie_integrations (spec 2026-08-19-02). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. Reverses
-- the remove-opsgenie-integrations section of 014_v0_17_0.up.sql.
--
-- There is no schema to revert: the up migration only deleted data. Deleted
-- rows cannot be un-deleted, so this is intentionally a no-op — the same
-- spirit as every other hard-delete migration in this repo (pre-1.0, no
-- tombstones). A database that ran the up migration and then this down
-- migration is NOT restored to its prior state; it is simply marked as
-- having reversed the migration.

select 1;

--bun:split

-- ==========================================================================
-- SECTION: results-status-domain
-- The v0.17.0 file's original body (specs 2026-08-18-03 and 2026-08-18-10). Teardown half.
-- ==========================================================================

-- Parity only: drop the reaper's index and narrow the status CHECK domain back
-- to what 013 left behind. Any row the reaper minted has to be folded back
-- into a value the narrower domain accepts before the constraint can be
-- re-added — plain `error` is the closest pre-9 encoding, and the distinction
-- this release introduced is simply lost on the way down.

drop index if exists idx_results_lifecycle_pending;

--bun:split

update results
set status = 6
where period_type = 'raw'
  and status = 9;

--bun:split

alter table results drop constraint if exists results_status_check;

--bun:split

alter table results
  add constraint results_status_check check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8));

--bun:split

comment on column results.status is
  '0=initial, 1=up, 2=down, 3=timeout, 4=error, 5=running (raw only).';
