-- Down migration for the incidents.number reference. Teardown/parity only.
-- SQLite mirror of postgres/migrations/013_incident_number.down.sql.

drop index if exists incidents_organization_number_idx;

--bun:split

alter table incidents drop column number;
