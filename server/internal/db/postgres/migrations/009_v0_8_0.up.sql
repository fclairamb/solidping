-- solidping v0.8.0 — consolidated release migration.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

-- ---------------------------------------------------------------------------
-- Per-status-page availability color thresholds (spec 2026-08-03-01)
-- ---------------------------------------------------------------------------
-- Generic per-page customization column, typed on the Go model
-- (models.StatusPageSettings) rather than a free-form map, so keys stay
-- discoverable and validation lives in one place. Today it only carries
-- availability.thresholdUp/thresholdDegraded, but it is the home for future
-- per-page customization knobs without a two-dialect migration each time.

alter table status_pages add column settings jsonb not null default '{}';

comment on column status_pages.settings is
  'Per-page display customization (e.g. availability color thresholds). Typed on the Go model as StatusPageSettings; unknown keys rejected on write. See spec 2026-08-03-01.';

-- ---------------------------------------------------------------------------
-- Entity slug max length raised to 100 chars (spec 2026-08-04-01)
-- ---------------------------------------------------------------------------
-- check_groups/checks/status_pages/status_page_sections had regex CHECK
-- constraints capped at 40/50 chars that drifted from, and now block, the
-- widened application-level slugRegex (3-100 chars) in
-- server/internal/handlers/{checks,checkgroups,statuspages}/service.go.
-- Severities has no slug CHECK constraint, so it needs no change here. Org
-- slugs, worker slugs, and other resources are deliberately left untouched.

alter table check_groups drop constraint check_groups_slug_check;
alter table check_groups add constraint check_groups_slug_check
  check (slug ~ '^[a-z][a-z0-9-]{2,99}$');

alter table checks drop constraint checks_slug_check;
alter table checks add constraint checks_slug_check
  check (slug is null or slug ~ '^[a-z][a-z0-9-]{2,99}$');

alter table status_pages drop constraint status_pages_slug_check;
alter table status_pages add constraint status_pages_slug_check
  check (slug ~ '^[a-z][a-z0-9-]{2,99}$');

alter table status_page_sections drop constraint status_page_sections_slug_check;
alter table status_page_sections add constraint status_page_sections_slug_check
  check (slug ~ '^[a-z][a-z0-9-]{2,99}$');
