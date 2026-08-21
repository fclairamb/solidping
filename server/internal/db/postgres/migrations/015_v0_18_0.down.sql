-- Teardown/parity half of the consolidated v0.18.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of 015_v0_18_0.up.sql,
-- so each one unwinds on a schema that still has everything the sections above
-- it created.
--
-- Several sections are lossy on the way down; each says so in its own note.

-- ==========================================================================
-- SECTION: generic-attachments
-- Was scratch migration 020_generic_attachments (spec 2026-08-21-01). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. Reverses the
-- generic-attachments section of 015_v0_18_0.up.sql.
--
-- LOSSY: dropping `topic` and `details` destroys every attachment link. The
-- blobs themselves survive in the storage backend and the `files` rows survive
-- in the table, but nothing knows what they were attached to any more, so a
-- re-migration starts from "no attachments exist".

drop index if exists files_org_topic_idx;

--bun:split

alter table files drop column if exists details;

--bun:split

alter table files drop column if exists topic;
