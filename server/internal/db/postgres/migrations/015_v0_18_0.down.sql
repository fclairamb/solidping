-- Teardown/parity half of the consolidated v0.18.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of 015_v0_18_0.up.sql,
-- so each one unwinds on a schema that still has everything the sections above
-- it created.
--
-- Several sections are lossy on the way down; each says so in its own note.

-- ==========================================================================
-- SECTION: status-page-password
-- Teardown half of the status-page-password section (spec 2026-08-21-07).
-- ==========================================================================

-- LOSSY: dropping the hash makes every password page unopenable rather than
-- public — the API refuses to serve a `password` page it cannot check a
-- password for. That is the safe direction, but it does mean a down-migration
-- takes those pages offline until an operator re-sets a password or moves them
-- back to `public`.

-- Move any password page back to `private` BEFORE narrowing the constraint:
-- the rows would otherwise violate it, and letting them fall back to `public`
-- would publish a page that was deliberately shared behind a secret.
update status_pages set visibility = 'private' where visibility = 'password';

--bun:split

alter table status_pages drop constraint if exists status_pages_visibility_check;

--bun:split

alter table status_pages
  add constraint status_pages_visibility_check
  check (visibility in ('public', 'private'));

--bun:split

alter table status_pages drop column if exists password_hash;

-- ==========================================================================
-- SECTION: status-page-branding
-- Teardown half of the status-page-branding section (spec 2026-08-21-07).
-- ==========================================================================

-- LOSSY: dropping these columns forgets which blob was a page's logo/favicon
-- and every white-label opt-in. The `files` rows and their blobs survive, but
-- nothing points at them any more, so they stop being publicly reachable —
-- which is the safe direction to fail in.

drop index if exists status_pages_favicon_file_idx;

--bun:split

drop index if exists status_pages_logo_file_idx;

--bun:split

alter table status_pages drop column if exists hide_branding;

--bun:split

alter table status_pages drop column if exists favicon_file_uid;

--bun:split

alter table status_pages drop column if exists logo_file_uid;

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
