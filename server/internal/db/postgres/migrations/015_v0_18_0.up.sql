-- v0.18.0 — the ONE consolidated migration for the (still unreleased) v0.18.0
-- release. 014_v0_17_0 is the last RELEASED migration (tag v0.17.0), so
-- everything this cycle produces lands here, in a single file per dialect, per
-- the repo convention documented in wiki/conventions/database.md.
--
-- It is organised into SECTIONs, each one a scratch migration folded in at
-- consolidation time and each one preserving that migration's own rationale
-- verbatim:
--
--   SECTION: generic-attachments        files.topic/details attachment link
--   SECTION: status-page-branding       status_pages logo/favicon/hide_branding
--   SECTION: status-page-password       status_pages password_hash
--
-- ORDER IS LOAD-BEARING. Sections run top to bottom and later ones build on
-- earlier ones. The .down.sql unwinds them in the exact reverse order.
--
-- The SECTION banners are also a machine-readable anchor: migration tests slice
-- one section out of this file so they can replay just that block against a
-- populated database. Renaming a section renames a test fixture — see
-- migrationSection() in the db test packages.

-- ==========================================================================
-- SECTION: generic-attachments
-- Was scratch migration 020_generic_attachments (spec 2026-08-21-01). Written
-- against 014_v0_17_0 on the stale premise that v0.17.0 was unreleased; moved
-- here verbatim once the tag proved it had shipped — a released migration is
-- never modified, because a database that already ran it never re-runs it.
-- ==========================================================================

-- Generic file attachments: a `files` row can now say WHAT IT IS ATTACHED TO
-- (spec 2026-08-21-01).
--
-- Until now a `files` row was an island. Nothing linked it to an owning
-- entity, so "list the attachments of this incident" was unanswerable and
-- there was no GC story at all — a blob whose owner was deleted stayed on the
-- bill forever. That gap blocks every capture feature (incident screenshots
-- first, HAR files and packet captures later), so it is fixed once, generically,
-- rather than once per capture kind.
--
-- `topic` is a PATH-LIKE ATTACHMENT KEY, `<entity>/<uid>/<kind>`, e.g.
--   incidents/9a1eb273-0a95-4d6b-b967-9af076c1f8e8/screenshot
-- Path-like on purpose: it makes both queries the feature needs cheap on the
-- same index — an EXACT match lists one entity's attachments of one kind, and
-- a PREFIX match (`incidents/<uid>/`) reaps everything hanging off an entity
-- when it is deleted. A pair of (entity_type, entity_uid) columns would have
-- needed a third column per kind and a wider index for the same two queries.
--
-- NULL is the norm, not an exception: every file that is not an attachment
-- (org logos, feedback screenshots) keeps a NULL topic and is untouched by
-- everything here. That is why the column is nullable with no default and no
-- backfill.
--
-- `details` is a free metadata bag, jsonb, deliberately unconstrained. For a
-- screenshot it carries {"capturedAt", "region", "checkUid", "trigger"}. It
-- exists so the NEXT attachment kind does not need a migration.
--
-- SECURITY NOTE: attachments are org-operational evidence, exactly like
-- `incidents.details`. Neither the topic, the details bag, nor a signed
-- download URL may ever reach a public surface (status pages, subscriber
-- payloads) — that rule is pinned by the never-public audit in
-- internal/handlers/statuspages/details_never_public_test.go.
alter table files add column if not exists topic text;

--bun:split

alter table files add column if not exists details jsonb;

--bun:split

-- Partial index, not a plain one: the overwhelming majority of `files` rows are
-- NOT attachments (topic IS NULL) and none of them can ever match an
-- attachment lookup, so keeping them out of the index keeps it proportional to
-- the attachment count rather than to the whole table. `deleted_at is null` is
-- in the predicate for the same reason — every attachment query is
-- live-rows-only, and a soft-deleted screenshot must never come back in a list.
create index if not exists files_org_topic_idx
  on files (organization_uid, topic)
  where deleted_at is null and topic is not null;

--bun:split

comment on column files.topic is
  'Attachment key, <entity>/<uid>/<kind> (spec 2026-08-21-01). NULL for non-attachment files. Prefix-matched for entity-deletion reaping.';

--bun:split

comment on column files.details is
  'Free metadata bag for the attachment kind. Org-operational evidence — never serialized onto a public surface.';

-- ==========================================================================
-- SECTION: status-page-branding
-- Status-page brand assets and the white-label opt-in (spec 2026-08-21-07).
-- ==========================================================================

-- A status page can now carry its OWN logo and favicon instead of wearing
-- SolidPing's, and can opt out of the "powered by SolidPing" footer.
--
-- The two asset columns hold a `files.uid`, not a URL. That is deliberate and
-- mirrors `organizations.logo_file_uid`: the public route
-- /pub/status-page-assets/:uid is unsigned, and its ENTIRE authorization story
-- is "this file is the current logo or favicon of a live, enabled status page".
-- Storing the file UID is what makes that a single indexed lookup, and it is
-- what makes replacing or clearing an asset un-publish the old blob in the
-- same write. A URL column would have made the route either unauthorizable or
-- permanently public.
--
-- `hide_branding` is only the page's HALF of the white-label decision. The
-- other half is the org's `whiteLabel` entitlement, and the footer disappears
-- only when both are true — which is why this column is a plain opt-in with a
-- FALSE default and carries no entitlement state of its own. An org that
-- downgrades keeps the flag set and simply gets the badge back; nothing has to
-- be rewritten on the billing side.
alter table status_pages add column if not exists logo_file_uid text;

--bun:split

alter table status_pages add column if not exists favicon_file_uid text;

--bun:split

alter table status_pages add column if not exists hide_branding boolean not null default false;

--bun:split

-- Partial indexes: the columns are NULL on the overwhelming majority of rows
-- (a page with no uploaded asset), and every lookup that uses them is the
-- public-route authorization check, which is by definition non-NULL and
-- live-rows-only.
create index if not exists status_pages_logo_file_idx
  on status_pages (logo_file_uid)
  where deleted_at is null and logo_file_uid is not null;

--bun:split

create index if not exists status_pages_favicon_file_idx
  on status_pages (favicon_file_uid)
  where deleted_at is null and favicon_file_uid is not null;

--bun:split

comment on column status_pages.logo_file_uid is
  'files.uid of the page''s uploaded logo (spec 2026-08-21-07). NULL = wear the SolidPing logo.';

--bun:split

comment on column status_pages.favicon_file_uid is
  'files.uid of the page''s uploaded favicon (spec 2026-08-21-07). NULL = the default favicon.';

--bun:split

comment on column status_pages.hide_branding is
  'Page-level white-label opt-in. Effective only when the org also holds the whiteLabel entitlement.';

-- ==========================================================================
-- SECTION: status-page-password
-- Password-protected status pages (spec 2026-08-21-07).
-- ==========================================================================

-- `visibility` gains a third value, `password`. There is deliberately NO check
-- constraint listing the three values: `visibility` has always been free-form
-- text validated by the API, and adding a constraint here would turn every
-- future value into a migration on a table other deployments are actively
-- serving from. models.ValidStatusPageVisibility is the single gate.
--
-- The hash is bcrypt, and it is never returned by any endpoint — reads expose
-- a `hasPassword` boolean instead. It doubles as the unlock cookie's HMAC key
-- (sha256 of the hash), which is what makes "change the password" invalidate
-- every outstanding cookie without a second column, a revocation list, or a
-- new server secret.
alter table status_pages add column if not exists password_hash text;

--bun:split

comment on column status_pages.password_hash is
  'bcrypt hash gating a visibility=password page (spec 2026-08-21-07). NEVER serialized; reads expose hasPassword only.';
