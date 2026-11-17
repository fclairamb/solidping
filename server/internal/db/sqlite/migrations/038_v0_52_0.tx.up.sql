-- v0.52.0 — SQLite migration.
--
-- Frozen once tagged, like every migration: a later change goes in 039.
--
--   SECTION: localized-generated-text   incidents / incident_publications / status_updates *_i18n

-- ==========================================================================
-- SECTION: localized-generated-text
-- ==========================================================================
-- Generated text (incident titles, auto-published status-page entries and
-- their updates) gets a sibling recipe, {"key": "...", "params": {...}}, that
-- the dashboard and the status page render in the reader's language. The
-- literal columns stay the fallback. See the Postgres file for the comments.
alter table incidents add column title_i18n text;

--bun:split

alter table incident_publications add column public_title_i18n text;

--bun:split

alter table status_updates add column title_i18n text;

--bun:split

alter table status_updates add column body_i18n text;

--bun:split

update incidents
set title_i18n = json_object('key', 'incident.title.downUnnamed')
where title_i18n is null and title = 'Check is down';

--bun:split

update incidents
set title_i18n = json_object(
    'key', 'incident.title.down',
    'params', json_object('name', substr(title, 1, length(title) - length(' is down')))
  )
where title_i18n is null
  and title like '% is down'
  and title <> 'Check is down'
  and length(title) > length(' is down');
