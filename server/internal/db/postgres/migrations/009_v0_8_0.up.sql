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
