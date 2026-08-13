-- Down migration for the incidents.number reference. Teardown/parity only.

drop index if exists incidents_organization_number_idx;

--bun:split

alter table incidents drop column if exists number;
