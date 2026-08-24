-- Teardown/parity half of the consolidated v0.18.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of 015_v0_18_0.up.sql,
-- so each one unwinds on a schema that still has everything the sections above
-- it created.
--
-- Several sections are lossy on the way down; each says so in its own note.

-- ==========================================================================
-- SECTION: per-check-incidents
-- Teardown half of the group-incident retirement (spec 2026-08-24-14).
-- ==========================================================================

-- NO-OP, and deliberately so. The up half changed DATA, not schema: it closed
-- every group incident that was still active. There is no record of which rows
-- those were, and re-opening them would resurrect incidents whose checks have
-- long since recovered. A downgraded binary brings the group state machine
-- back; it simply starts from "no group incident is currently open", which is
-- a legal state it already handles (the next failing member opens one).


-- ==========================================================================
-- SECTION: flap-level
-- Teardown half of the flap-level snapshot (spec 2026-08-24-05).
-- ==========================================================================

-- LOSSY: which flap level each incident opened/reopened at is discarded. The
-- underlying checks.flap_count / last_outage_at (added earlier, spec
-- 2026-06-30-07) are untouched, so the live adaptive-recovery behavior itself
-- is unaffected — only the historical per-incident record is lost.
alter table incidents drop column if exists flap_level;

-- ==========================================================================
-- SECTION: must-change-password
-- Teardown half of the forced-rotation flag (spec 2026-08-23-04).
-- ==========================================================================

-- LOSSY, and lossy in the unsafe direction: dropping the column silently
-- un-forces every pending rotation, including the seeded bootstrap admin's.
-- A downgraded installation is back to a standing exposure.
alter table users drop column if exists must_change_password;

-- ==========================================================================
-- SECTION: custom-domain-state
-- Teardown half of the custom-domain lifecycle columns (spec 2026-08-23-03).
-- ==========================================================================

-- LOSSY: the grace/demoted distinction and the last diagnostic are dropped. The
-- pre-existing custom_domain_verified_at / custom_domain_failures columns still
-- describe the domain, so a downgraded installation degrades to the old one-way
-- behaviour rather than to a broken one.
alter table status_pages drop column if exists custom_domain_last_check;

--bun:split

alter table status_pages drop column if exists custom_domain_grace_since;

--bun:split

alter table status_pages drop column if exists custom_domain_successes;

--bun:split

alter table status_pages drop column if exists custom_domain_state;

-- ==========================================================================
-- SECTION: support-inbox
-- Teardown half of the support-inbox section (spec 2026-08-22-02).
-- ==========================================================================

-- LOSSY, and unusually so: this drops every captured human message. There is no
-- downgrade that keeps them — the whole point of the feature is that these rows
-- exist nowhere else.
drop table if exists support_messages;

--bun:split

drop table if exists support_threads;

-- ==========================================================================
-- SECTION: traceroute-diagnostics
-- Teardown half of the traceroute-diagnostics section (spec 2026-08-21-10).
-- ==========================================================================

-- LOSSY: per-check overrides are dropped with the column. On the way back up
-- every check returns to inheriting the org default, which is the behaviour
-- that predates the feature.
alter table checks drop column if exists traceroute_on_failure;

--bun:split

-- ==========================================================================
-- SECTION: audit-actor-metadata
-- Teardown half of the audit-actor-metadata section (spec 2026-08-21-09).
-- ==========================================================================

-- LOSSY: every event whose actor_type is one of the two kinds this section
-- introduced is DELETED, not downgraded. The narrowed constraint below has no
-- room for them, and rewriting them to 'system' would silently turn "a token
-- did this" into "the server did this" inside an audit trail — a worse outcome
-- than losing the row on a rollback that is never run in production anyway.
delete from events where actor_type in ('api_token', 'service');

--bun:split

drop index if exists idx_events_created;

--bun:split

drop index if exists idx_events_org_type_created;

--bun:split

alter table events drop constraint if exists events_actor_type_check;

--bun:split

alter table events add constraint events_actor_type_check
  check (actor_type in ('system', 'user'));

--bun:split

alter table events drop column if exists user_agent;

--bun:split

alter table events drop column if exists source_ip;

--bun:split

-- ==========================================================================
-- SECTION: slo-burn-alerts
-- Teardown half of the slo-burn-alerts section (spec 2026-08-21-08).
-- ==========================================================================

-- LOSSY: every burn incident is DELETED, not downgraded. The schema below has
-- no `kind` column, so a surviving row would silently become an ordinary check
-- incident on its routing anchor — i.e. a rollback would invent an outage that
-- never happened. Deleting is the honest outcome.

delete from incidents where kind = 'slo_burn';

--bun:split

drop index if exists idx_incidents_kind_check_uid;

--bun:split

drop index if exists uq_active_slo_burn_incident;

--bun:split

alter table incidents drop constraint if exists incidents_kind_check;

--bun:split

alter table incidents drop column if exists slo_alert_policy_uid;

--bun:split

alter table incidents drop column if exists slo_uid;

--bun:split

alter table incidents drop column if exists kind;

--bun:split

drop table if exists slo_alert_policies;

--bun:split

-- ==========================================================================
-- SECTION: status-subscriber-channels
-- Teardown half of the status-subscriber-channels section (spec 2026-08-21-07).
-- ==========================================================================

-- LOSSY: every webhook and Slack subscription is DELETED, not merely
-- downgraded. There is nowhere for them to go — the schema below has no column
-- that can hold a delivery endpoint — and leaving the rows behind with a NULL
-- email would violate the NOT NULL this restores. Deleting them is also the
-- honest outcome: after a rollback nothing would deliver to them anyway.

delete from status_page_subscriber where channel <> 'email';

--bun:split

drop index if exists idx_status_page_subscriber_live;

--bun:split

create unique index idx_status_page_subscriber_live
  on status_page_subscriber (status_page_uid, email, scope, coalesce(incident_uid::text, ''))
  where deleted_at is null;

--bun:split

alter table status_page_subscriber drop constraint if exists status_page_subscriber_channel_check;

--bun:split

alter table status_page_subscriber drop column if exists disabled_at;

--bun:split

alter table status_page_subscriber drop column if exists failure_count;

--bun:split

alter table status_page_subscriber drop column if exists endpoint_key;

--bun:split

alter table status_page_subscriber drop column if exists endpoint_hint;

--bun:split

alter table status_page_subscriber drop column if exists endpoint_private;

--bun:split

alter table status_page_subscriber drop column if exists channel;

--bun:split

alter table status_page_subscriber alter column email set not null;

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
-- Teardown half of the status-page-branding section
-- (specs 2026-08-21-07, 2026-08-22-03).
-- ==========================================================================

-- There are no columns to drop: branding lives in `status_pages.settings`,
-- a column this migration did not create. What the up half changed is the
-- org-logo serving route, so that is what is reversed here.
--
-- LOSSY in one direction only: a logo uploaded AFTER this migration ran gets
-- its `logo_url` rewritten back to /pub/org-logos/<uid>, a route the rolled-back
-- binary does serve. Page branding written into `settings` is left alone — the
-- older binary simply ignores the `branding` key.

update organizations
   set logo_url = '/pub/org-logos/' || logo_file_uid
 where logo_file_uid is not null
   and logo_url like '/pub/assets/%';

--bun:split

-- Drop the topic again: the rolled-back binary authorizes org logos by state,
-- not by topic, and a stray attachment key would only confuse the reaper.
update files
   set topic = null
 where topic like 'organizations/%/logo';

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
