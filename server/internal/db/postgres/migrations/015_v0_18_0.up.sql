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
