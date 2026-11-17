-- v0.52.0 — Postgres migration.
--
-- Frozen once tagged, like every migration: a later change goes in 039.
--
--   SECTION: localized-generated-text   incidents / incident_publications / status_updates *_i18n

-- ==========================================================================
-- SECTION: localized-generated-text
-- ==========================================================================
-- Text the server writes by itself (an incident title, an auto-published
-- status-page entry and its updates) was stored as one literal sentence, in
-- English or in the page language, so a reader in another language saw it
-- untranslated. Each such column now has a sibling recipe, {"key": "...",
-- "params": {...}}, that the dashboard and the status page render in the
-- reader's language. The literal columns are unchanged and stay the fallback.
alter table incidents add column if not exists title_i18n jsonb;

--bun:split

alter table incident_publications add column if not exists public_title_i18n jsonb;

--bun:split

alter table status_updates add column if not exists title_i18n jsonb;

--bun:split

alter table status_updates add column if not exists body_i18n jsonb;

--bun:split

comment on column incidents.title_i18n is 'Recipe of a generated title: {"key": "incident.title.<kind>", "params": {...}}, rendered in the reader''s language. NULL for a title written by a person, or stored before v0.52.0 in another shape than "<name> is down".';

--bun:split

comment on column incident_publications.public_title_i18n is 'Recipe of a templated public title: {"key": "publication.<template>", "params": {"name": "<public resource name>"}}, rendered in the visitor''s language. NULL once a person retitled the publication, and for hand-typed titles.';

--bun:split

comment on column status_updates.title_i18n is 'Recipe of a machine-written update title (auto-publish pipeline): {"key": "publication.<template>", "params": {...}}. NULL for hand-written updates and once a person edited the title.';

--bun:split

comment on column status_updates.body_i18n is 'Recipe of a machine-written update body (auto-publish pipeline): {"key": "publication.<template>", "params": {...}}. NULL for hand-written updates and once a person edited the body.';

--bun:split

-- Existing generated outage titles ("<name> is down", English only: the
-- server never wrote another language there) get their recipe too.
update incidents
set title_i18n = jsonb_build_object('key', 'incident.title.downUnnamed')
where title_i18n is null and title = 'Check is down';

--bun:split

update incidents
set title_i18n = jsonb_build_object(
    'key', 'incident.title.down',
    'params', jsonb_build_object('name', left(title, length(title) - length(' is down')))
  )
where title_i18n is null
  and title like '% is down'
  and title <> 'Check is down'
  and length(title) > length(' is down');
