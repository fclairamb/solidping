-- solidping v0.8.0 — see the postgres twin for the full rationale.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

-- ---------------------------------------------------------------------------
-- Per-status-page availability color thresholds (spec 2026-08-03-01)
-- ---------------------------------------------------------------------------
-- Generic per-page customization column, typed on the Go model
-- (models.StatusPageSettings). SQLite has no jsonb type, so it is stored as
-- text (the cross-database convention used across these migrations).

alter table status_pages add column settings text not null default '{}'; -- Per-page display customization (availability thresholds today). Typed on the Go model.
