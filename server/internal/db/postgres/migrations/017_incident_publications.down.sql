-- Teardown/parity only — never run in production. Reverses
-- 017_incident_publications.up.sql.

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
