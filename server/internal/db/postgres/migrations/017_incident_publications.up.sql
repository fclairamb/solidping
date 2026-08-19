-- Incident publications: the publication overlay that finally lets an
-- automatic incident reach a status page (spec 2026-08-19-08).
--
-- The operational `incidents` row stays exactly as it is. It carries ack /
-- snooze metadata, auto-generated internal titles ("api-eu is down") and probe
-- diagnostics in `details` — none of which may ever be shown to a customer.
-- This table is the thin, deliberately public-only overlay on top of it:
-- "incident X is visible on page Y, under this customer-readable title, in
-- this state". Nothing here is derived from the incident's diagnostics, and
-- there is no column a probe's output could ever be written into. That is the
-- security boundary of the feature, expressed in the schema rather than in a
-- code review comment.
--
-- Numbering: scratch migration for the still-unreleased v0.17.0 cycle. 013 is
-- the last RELEASED migration; 014_v0_17_0 is that cycle's consolidated file,
-- 015 removed the Opsgenie integration type and 016 added workers.version.
-- 017 is simply the next free scratch number — it folds into the final
-- 014_v0_17_0 at release consolidation, same as its siblings.

create table if not exists incident_publications (
  uid                 uuid primary key default gen_random_uuid(),
  organization_uid    uuid not null references organizations(uid),
  incident_uid        uuid references incidents(uid),
  status_page_uid     uuid not null references status_pages(uid),
  public_title        text not null,
  public_state        text not null default 'investigating'
                        check (public_state in ('investigating', 'identified', 'monitoring', 'resolved')),
  severity            text check (severity is null or severity in ('minor', 'major', 'critical')),
  auto_created        boolean not null default false,
  human_touched_at    timestamptz,
  published_at        timestamptz not null default now(),
  resolved_at         timestamptz,
  notify_window_start timestamptz,
  notify_window_count integer not null default 0,
  created_at          timestamptz not null default now(),
  updated_at          timestamptz not null default now(),
  deleted_at          timestamptz
);

--bun:split

-- One publication per (incident, page). This index is what makes the debounce
-- job idempotent: a concurrent job fire and a manual publish race into the same
-- row, and the loser gets a unique violation it can recover from by re-reading,
-- rather than minting a duplicate public incident. Hand-authored publications
-- (incident_uid NULL) are exempt — an operator may post as many as they like.
create unique index if not exists uq_incident_publications_incident_page
  on incident_publications (incident_uid, status_page_uid)
  where incident_uid is not null and deleted_at is null;

--bun:split

create index if not exists idx_incident_publications_page_published
  on incident_publications (status_page_uid, published_at desc)
  where deleted_at is null;

--bun:split

create index if not exists idx_incident_publications_incident
  on incident_publications (incident_uid)
  where incident_uid is not null and deleted_at is null;

--bun:split

comment on table incident_publications is
  'Public-facing publication overlay for incidents (spec 2026-08-19-08). Every column is safe to render publicly except notify_window_*; probe output and error strings must never be written here.';

--bun:split

comment on column incident_publications.incident_uid is
  'Operational incident this publication tracks. NULL = hand-authored, tracks nothing.';

--bun:split

comment on column incident_publications.human_touched_at is
  'First manual edit / manual update. Drives the if_untouched auto-resolve policy.';

--bun:split

comment on column incident_publications.notify_window_count is
  'Subscriber fan-out waves sent inside the rolling hour starting at notify_window_start (storm cap). Internal bookkeeping, never public.';

--bun:split

-- Page-level auto-publish settings. auto_publish defaults to FALSE at the DDL
-- level ON PURPOSE: an existing installation upgrading to this release must not
-- discover that its internal blips are now public. New pages opt in through the
-- application's create path (models.NewStatusPage), so the migration stays
-- honest about existing rows.
alter table status_pages add column if not exists auto_publish boolean not null default false;

--bun:split

alter table status_pages add column if not exists auto_publish_delay_seconds integer not null default 60;

--bun:split

alter table status_pages add column if not exists auto_resolve text not null default 'if_untouched';

--bun:split

alter table status_pages drop constraint if exists status_pages_auto_resolve_valid;

--bun:split

alter table status_pages add constraint status_pages_auto_resolve_valid
  check (auto_resolve in ('always', 'if_untouched', 'never'));

--bun:split

alter table status_pages drop constraint if exists status_pages_auto_publish_delay_nonneg;

--bun:split

alter table status_pages add constraint status_pages_auto_publish_delay_nonneg
  check (auto_publish_delay_seconds >= 0 and auto_publish_delay_seconds <= 86400);

--bun:split

comment on column status_pages.auto_publish is
  'Auto-publish incidents affecting this page. FALSE for every pre-existing row by design; new pages default TRUE in the application create path.';

--bun:split

comment on column status_pages.auto_publish_delay_seconds is
  'Debounce before an incident is published. 0 publishes immediately; a shorter outage than this never reaches the public page.';

--bun:split

-- Three-state per-resource override: NULL = inherit the page. Making it
-- nullable rather than defaulting to the page value means flipping the page
-- setting does not silently rewrite every resource's intent.
alter table status_page_resources add column if not exists auto_publish boolean;

--bun:split

comment on column status_page_resources.auto_publish is
  'Per-resource auto-publish override. NULL = inherit the status page setting.';

--bun:split

-- Narrative rows stay in status_updates. Two changes are needed:
--   * a link to the publication, so a HAND-AUTHORED publication (which has no
--     incident_uid to thread on) still has a thread;
--   * author_uid becomes nullable, because an auto-generated update has no
--     author. Attributing a machine post to whichever human happens to own the
--     org would be a lie the UI then renders.
alter table status_updates add column if not exists incident_publication_uid uuid
  references incident_publications(uid);

--bun:split

alter table status_updates alter column author_uid drop not null;

--bun:split

create index if not exists idx_status_updates_publication
  on status_updates (incident_publication_uid)
  where incident_publication_uid is not null and deleted_at is null;

--bun:split

comment on column status_updates.author_uid is
  'User who posted the update. NULL = generated by the auto-publish pipeline (no human author).';
