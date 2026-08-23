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
--   SECTION: status-page-branding       org-logo topic + public-route backfill
--   SECTION: status-page-password       status_pages password_hash
--   SECTION: status-subscriber-channels status_page_subscriber webhook/slack
--   SECTION: slo-burn-alerts            slo_alert_policies + incidents.kind/slo binding
--   SECTION: audit-actor-metadata       events source_ip/user_agent + wider actor_type
--   SECTION: traceroute-diagnostics     checks.traceroute_on_failure per-check override
--   SECTION: support-inbox              support_threads + support_messages
--   SECTION: custom-domain-state        status_pages lifecycle columns
--   SECTION: must-change-password       users.must_change_password
--
-- CONSOLIDATION NOTE (2026-08-24): the support-inbox, custom-domain-state and
-- must-change-password sections were folded in from what were briefly separate
-- 016/017/018 files for this same unreleased release. Nothing in the wild had
-- run them - v0.17.0 is the last tag, so every deployed database sits at 014 -
-- but a DEVELOPER database that already recorded 015 will NOT replay the folded
-- sections, because Bun keys applied migrations on the numeric prefix alone.
-- internal/db/migrationguard turns that into a loud checksum failure at boot
-- rather than a silent skip. The fix is to RESET the dev database; do NOT run
-- `solidping migrate repair`, which would re-record the checksum without ever
-- running the folded SQL and bake the missing tables in permanently.
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
-- Brand assets: where they are stored, and how they are authorized
-- (specs 2026-08-21-07, 2026-08-22-03).
-- ==========================================================================

-- A status page can carry its OWN logo and favicon instead of wearing
-- SolidPing's, and can opt out of the "powered by SolidPing" footer.
--
-- None of that is a column. All three knobs live in `status_pages.settings`
-- under a `branding` key, next to `availability`, because a per-page knob read
-- only while RENDERING the page belongs in settings — see the rule in
-- wiki/conventions/database.md. Spec 2026-08-21-07 originally added
-- `logo_file_uid`, `favicon_file_uid`, `hide_branding` and two partial indexes
-- here; 2026-08-22-03 removed them from THIS file rather than adding a `016`
-- that drops them, since 015 is unreleased and shipping add-then-drop in one
-- release is noise in the permanent record.
--
-- The indexes went with the columns because nothing looks a page up by its
-- asset UID any more: a blob is public when the FILE's own `topic` is on the
-- allowlist in internal/handlers/files/publictopics.go, which is one rule for
-- every publicly-served asset kind instead of one state query per kind.
--
-- What is left in this section is the migration that move requires: existing
-- organization logos have to acquire that topic, or they stop resolving.

-- Give every uploaded org logo the topic that authorizes it. Without this an
-- already-uploaded logo 404s after the deploy, which spec 2026-08-22-03 calls
-- a regression rather than an acceptable migration cost.
--
-- `o.deleted_at is null` is a SECURITY filter, not tidiness. The check this
-- replaces required a LIVE organization, and deleting an org did NOT reap its
-- files, so every historically-deleted org still owns a live `files` row whose
-- logo stopped being reachable purely because the org row went away. Granting
-- those rows a public topic would RE-publish them — an authorization widening
-- nobody asked for, and one the spec's Today/After table does not list. The
-- reap added to org deletion only covers deletes from here on; this clause is
-- what covers the ones that already happened.
update files
   set topic = 'organizations/' || o.uid || '/logo'
  from organizations o
 where o.logo_file_uid = files.uid
   and o.deleted_at is null
   and files.topic is null;

--bun:split

-- ...and point the stored URL at the route that now serves it. The bespoke
-- /pub/org-logos/:uid handler and its "is this some live org's current logo"
-- state query are retired; there is one public asset route.
--
-- Same `deleted_at is null` scope as the topic backfill above. A deleted org's
-- stored URL is harmless either way (both routes 404 for it), but a row that
-- was deliberately NOT granted a topic must not be left claiming a URL on the
-- route that requires one.
update organizations
   set logo_url = '/pub/assets/' || logo_file_uid
 where logo_file_uid is not null
   and deleted_at is null
   and logo_url like '/pub/org-logos/%';

-- ==========================================================================
-- SECTION: status-page-password
-- Password-protected status pages (spec 2026-08-21-07).
-- ==========================================================================

-- `visibility` gains a third value, `password`. The column has carried a CHECK
-- constraint since 001 (`visibility in ('public','private')`), so admitting the
-- new value means replacing that constraint — dropping it and re-adding the
-- widened one, which Postgres does in place with no table rewrite.
--
-- The hash comes from internal/utils/passwords (argon2id by default, bcrypt
-- selectable — the same policy user passwords use), and it is never returned
-- by any endpoint — reads expose
-- a `hasPassword` boolean instead. It doubles as the unlock cookie's HMAC key
-- (sha256 of the hash), which is what makes "change the password" invalidate
-- every outstanding cookie without a second column, a revocation list, or a
-- new server secret.
alter table status_pages drop constraint if exists status_pages_visibility_check;

--bun:split

alter table status_pages
  add constraint status_pages_visibility_check
  check (visibility in ('public', 'private', 'password'));

--bun:split

alter table status_pages add column if not exists password_hash text;

--bun:split

comment on column status_pages.password_hash is
  'Password hash (internal/utils/passwords, argon2id by default) gating a visibility=password page (spec 2026-08-21-07). NEVER serialized; reads expose hasPassword only.';

--bun:split

comment on column status_pages.visibility is
  'Access control: public (anyone), private (hidden entirely, 404), or password (shared with a secret, 401 until unlocked).';

-- ==========================================================================
-- SECTION: status-subscriber-channels
-- Webhook and Slack status-page subscriptions (spec 2026-08-21-07).
-- ==========================================================================

-- A status-page subscriber is no longer necessarily an email address.
--
-- `channel` names what the row delivers to. `email` (the only prior kind) is
-- the DDL default so every existing row keeps its meaning without a backfill.
--
-- The endpoint URL is treated with exactly the same opacity as a subscriber
-- email, and then some: an incoming-webhook URL IS a credential — anyone
-- holding it can post into the customer's channel. So it never lands in a
-- readable column. `endpoint_private` holds the AES-256-GCM envelope written by
-- internal/crypto/credentials (per-org DEK, same machinery as
-- integration_connections.settings_private), carrying both the URL and the
-- optional per-subscriber signing secret. `endpoint_hint` is the only part any
-- API response may echo: a masked remnant, enough for an operator to recognise
-- which webhook a row is, useless to anyone who steals the response.
--
-- `endpoint_key` is a sha256 of the normalized URL. It exists purely so the
-- live-uniqueness index can dedupe webhook rows the way `email` dedupes mail
-- rows, WITHOUT putting the URL itself in an index (indexes end up in backups,
-- query plans and error messages).
--
-- `failure_count` / `disabled_at` are the delivery-failure circuit breaker: a
-- webhook whose endpoint has gone away must stop being retried forever, and
-- the operator must be able to see that it stopped and why — hence the
-- accompanying `statuspage.subscriber.disabled` event, not just a silent flag.
alter table status_page_subscriber alter column email drop not null;

--bun:split

alter table status_page_subscriber
  add column if not exists channel text not null default 'email';

--bun:split

-- Dropped first so the whole section is re-runnable: `add constraint` has no
-- IF NOT EXISTS, and a section that cannot be replayed cannot be exercised
-- against a populated database by a test either.
alter table status_page_subscriber drop constraint if exists status_page_subscriber_channel_check;

--bun:split

alter table status_page_subscriber
  add constraint status_page_subscriber_channel_check
  check (channel in ('email', 'webhook', 'slack'));

--bun:split

alter table status_page_subscriber add column if not exists endpoint_private text;

--bun:split

alter table status_page_subscriber add column if not exists endpoint_hint text;

--bun:split

alter table status_page_subscriber add column if not exists endpoint_key text;

--bun:split

alter table status_page_subscriber add column if not exists failure_count integer not null default 0;

--bun:split

alter table status_page_subscriber add column if not exists disabled_at timestamptz;

--bun:split

-- The live-uniqueness index has to become channel-aware. The old one keyed on
-- (page, email, scope, incident) and would collide every webhook row against
-- every other one now that `email` is null for them.
drop index if exists idx_status_page_subscriber_live;

--bun:split

create unique index idx_status_page_subscriber_live
  on status_page_subscriber (
    status_page_uid, channel, coalesce(email, ''), coalesce(endpoint_key, ''),
    scope, coalesce(incident_uid::text, '')
  )
  where deleted_at is null;

--bun:split

comment on column status_page_subscriber.endpoint_private is
  'Encrypted envelope holding the delivery URL and optional signing secret. NEVER returned by any endpoint.';

--bun:split

comment on column status_page_subscriber.endpoint_hint is
  'Masked remnant of the delivery URL — the only part an API response may echo.';

-- ==========================================================================
-- SECTION: slo-burn-alerts
-- Multiwindow burn-rate alerting for SLOs (spec 2026-08-21-08).
-- ==========================================================================

-- An SLO's burn rate has been computed since 2026-08-20-01 and consumed by
-- nobody. These two tables' worth of columns are what turn it into a page.
--
-- One row per (SLO, built-in policy). There are exactly two built-ins — a fast
-- burn and a slow burn — and they are rows rather than constants for one
-- reason: the SRE-workbook thresholds are a starting point, not a law. An org
-- whose traffic shape makes 14.4x too twitchy has to be able to change it
-- without a code deploy, and the value it changed has to survive a restart.
--
-- The windows are stored for the same reason. "Fast burn" is not intrinsically
-- 1h/5m; it is intrinsically "a long window that proves significance and a
-- short window that proves it is still happening". Deriving the pair from the
-- kind would hard-code one org's opinion into everyone's schema.
--
-- `enabled` defaults to FALSE. Upgrading to a version that adds alerting must
-- never start paging on its own: an operator opts in per policy, per SLO.
create table if not exists slo_alert_policies (
  uid                   uuid primary key default gen_random_uuid(),
  organization_uid      uuid not null references organizations(uid),
  slo_uid               uuid not null references slos(uid) on delete cascade,
  -- The built-in identity. Unique per SLO: these are the two named policies
  -- the product ships, not a free-form list.
  kind                  text not null,
  enabled               boolean not null default false,
  -- The long window proves significance; the short window proves the burn is
  -- still happening right now. An alert needs BOTH to be over threshold, which
  -- is what stops a long-resolved spike from paging for the rest of the hour.
  long_window_seconds   integer not null,
  short_window_seconds  integer not null,
  -- Burn-rate multiple: 1.0 spends the budget exactly by the end of the
  -- calendar window, 14.4 spends a 30-day budget in ~2h.
  threshold             numeric(8,3) not null,
  severity              text not null,
  -- Sparse data must not fabricate an alert. A window carrying fewer than this
  -- many countable probes is INCONCLUSIVE — it does not fire, and it also does
  -- not count as "below threshold" for the auto-resolve hysteresis. Three is
  -- deliberately low: a check whose period exceeds short_window/min_samples can
  -- never satisfy its own short window, and silently never alerting is worse
  -- than alerting slightly noisily.
  min_samples           integer not null default 3,
  -- Live readout, refreshed on every evaluation. Stored so the dashboard's
  -- Alerting section and the evaluator cannot disagree about what the burn
  -- rate was a minute ago.
  last_evaluated_at     timestamptz,
  last_long_burn_rate   numeric(12,4),
  last_short_burn_rate  numeric(12,4),
  -- Hysteresis anchor: the instant BOTH windows first dropped below threshold.
  -- Resolution waits until that has held for a full short window. Cleared the
  -- moment either window goes back over, so a flapping burn never resolves.
  below_threshold_since timestamptz,
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now(),
  constraint slo_alert_policies_kind_check check (kind in ('fast', 'slow')),
  constraint slo_alert_policies_severity_check check (severity in ('critical', 'warning')),
  constraint slo_alert_policies_windows_check check (
    long_window_seconds > 0
    and short_window_seconds > 0
    and short_window_seconds <= long_window_seconds
  ),
  constraint slo_alert_policies_threshold_check check (threshold > 0),
  constraint slo_alert_policies_min_samples_check check (min_samples >= 1)
);

--bun:split

create unique index if not exists uq_slo_alert_policies_slo_kind
  on slo_alert_policies (slo_uid, kind);

--bun:split

-- The evaluator's work queue: every enabled policy across every org, once a
-- minute. Partial so the index only carries the rows the sweep actually reads.
create index if not exists idx_slo_alert_policies_enabled
  on slo_alert_policies (enabled)
  where enabled = true;

--bun:split

comment on table slo_alert_policies is
  'Per-SLO multiwindow burn-rate alert policies. Thresholds and windows are stored, never derived, so operators can tune them.';

--bun:split

-- A burn alert is an INCIDENT, not a new object. That is the whole design: it
-- inherits ack, snooze, manual resolve, escalation policies, severity-gated
-- channel routing, group correlation and the incident timeline for free,
-- because it goes through exactly the same service.
--
-- `kind` is the discriminator. 'check' is the pre-existing meaning and the
-- default, so every existing row keeps it with no backfill.
alter table incidents add column if not exists kind text not null default 'check';

--bun:split

alter table incidents drop constraint if exists incidents_kind_check;

--bun:split

alter table incidents add constraint incidents_kind_check check (kind in ('check', 'slo_burn'));

--bun:split

-- The binding. Nullable because only a burn incident has one.
--
-- `on delete set null` rather than cascade on both: deleting an SLO or
-- retuning a policy must not erase the history of the pages it sent. The
-- incident survives as a record of what happened, merely unbound.
alter table incidents add column if not exists slo_uid uuid references slos(uid) on delete set null;

--bun:split

alter table incidents
  add column if not exists slo_alert_policy_uid uuid references slo_alert_policies(uid) on delete set null;

--bun:split

-- Dedup enforced by the database, not merely by the evaluator: at most ONE
-- open burn incident per (SLO, policy). Two evaluator replicas racing on the
-- same minute both see "no open incident" and both insert; this is what makes
-- the loser fail instead of double-paging.
create unique index if not exists uq_active_slo_burn_incident
  on incidents (slo_uid, slo_alert_policy_uid)
  where state = 1 and kind = 'slo_burn' and deleted_at is null;

--bun:split

-- The check state machine looks up "is there an open incident on this check"
-- by check_uid. A burn incident carries a representative check for routing, so
-- without kind in the index that lookup would start returning burn incidents
-- and a burning SLO would make its own check look permanently down.
create index if not exists idx_incidents_kind_check_uid
  on incidents (kind, check_uid)
  where deleted_at is null;

--bun:split

comment on column incidents.kind is
  'check = a failing check (the original meaning); slo_burn = an SLO error-budget burn-rate alert.';

-- ==========================================================================
-- SECTION: audit-actor-metadata
-- Actor metadata on events, for the security/config audit trail
-- (spec 2026-08-21-09).
-- ==========================================================================

-- The spec asks for `actor_user_uid`. That column already exists under its
-- original name: `actor_uid uuid references users(uid)`, added in 001. A second
-- column of identical meaning would only create a split brain (which one does a
-- reader trust?), so the column keeps its name and the API exposes it under the
-- spec's `actorUserUid` parameter name instead. What is genuinely new here is
-- the request provenance — source_ip / user_agent — plus two extra actor kinds.

alter table events add column if not exists source_ip varchar(45);

--bun:split

alter table events add column if not exists user_agent text;

--bun:split

-- Widen the actor_type domain. The original inline check in 001 admitted only
-- ('system','user'); audit events can also be caused by a personal access
-- token / agent key, or by a signed service-to-service call.
alter table events drop constraint if exists events_actor_type_check;

--bun:split

alter table events add constraint events_actor_type_check
  check (actor_type in ('system', 'user', 'api_token', 'service'));

--bun:split

-- The audit UI's primary query is "this org's events, of this family, newest
-- first". Until now an org-scoped event_type filter had no covering index —
-- idx_events_org_created is org+time only and idx_events_type_created is
-- type+time across every org.
create index if not exists idx_events_org_type_created
  on events (organization_uid, event_type, created_at desc);

--bun:split

-- The retention sweep deletes by created_at alone, across all orgs; every
-- existing index leads with another column, so it had nothing to walk.
create index if not exists idx_events_created on events (created_at);

--bun:split

comment on column events.actor_uid is
  'Acting user (the spec 2026-08-21-09 "actor_user_uid"). NULL for system-originated events.';

--bun:split

comment on column events.source_ip is
  'Client address of the request that caused the event. NULL when unknown or when audit.capture_ip is off.';

--bun:split

-- ==========================================================================
-- SECTION: traceroute-diagnostics
-- Per-check opt-out for the MTR-style path capture taken when a check goes
-- down on a network-reachability failure (spec 2026-08-21-10).
-- ==========================================================================

-- NULLABLE ON PURPOSE — three states, not two:
--
--   NULL   inherit the org default (org parameter `diagnostics.traceroute.enabled`,
--          which itself defaults to ON per the spec)
--   true   always trace this check
--   false  never trace this check, whatever the org says
--
-- A NOT NULL DEFAULT true would collapse "the operator has not decided" into
-- "the operator said yes", and every existing check would silently become an
-- explicit opt-in that the org-level switch could no longer turn off.
alter table checks add column if not exists traceroute_on_failure boolean;

--bun:split

comment on column checks.traceroute_on_failure is
  'Per-check path-trace-on-failure override. NULL inherits the org default (spec 2026-08-21-10).';

-- ==========================================================================
-- SECTION: support-inbox
-- Instance-level capture of human messages our bots cannot parse
-- (spec 2026-08-22-02).
-- ==========================================================================

-- Every inbound channel SolidPing owns used to drop anything that was not a
-- recognised command. A person replying to an alert was talking to a black
-- hole. These two tables are where that traffic lands instead.
--
-- THREADS BELONG TO THE INSTANCE, NOT TO AN ORGANIZATION. The sender of an
-- inbound WhatsApp message is a phone number; frequently there is no org to
-- attribute it to at all, and a message from a stranger must not be dropped for
-- lack of one. Org-scoping would force a catch-all org and make the
-- unattributable case the broken case. `organization_uid` / `user_uid` are
-- therefore NULLABLE ATTRIBUTION — a hint for the operator, never an
-- access-control boundary. Visibility is SuperAdmin, enforced in the API.
create table if not exists support_threads (
  uid               uuid primary key default gen_random_uuid(),
  -- whatsapp | telegram | sms | slack | discord | email
  channel           text not null,
  -- E.164 number, telegram chat id, slack user id, discord user id, email addr.
  channel_identity  text not null,
  -- Everything the reply adapter needs to answer that is NOT the identity:
  -- the Slack connection uid + IM channel id, the Discord DM channel id, the
  -- Telegram chat id when it differs from the identity. Opaque to the schema on
  -- purpose — each adapter owns its own keys and a new channel needs no
  -- migration.
  channel_context   jsonb,
  subject           text not null default '',
  -- open | pending | closed. Set by the OPERATOR, deliberately. This is NOT the
  -- reply-window axis: a thread can be open (unanswered) and yet expired
  -- (WhatsApp will no longer accept a free-form reply). The window is DERIVED
  -- from last_inbound_at at read time and never stored as truth, so it cannot
  -- go stale.
  status            text not null default 'open',
  organization_uid  uuid references organizations(uid),
  user_uid          uuid references users(uid),
  last_message_at   timestamptz not null default now(),
  -- Anchor of the derived reply window (WhatsApp's free 24h customer-service
  -- window). A timestamp, not a verdict.
  last_inbound_at   timestamptz,
  unread_count      integer not null default 0,
  -- Mirror-notification throttle state. These are publicly reachable numbers:
  -- someone texting a hundred times must not produce a hundred emails.
  last_mirror_at    timestamptz,
  pending_mirrors   integer not null default 0,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  deleted_at        timestamptz,
  constraint support_threads_channel_check
    check (channel in ('whatsapp', 'telegram', 'sms', 'slack', 'discord', 'email')),
  constraint support_threads_status_check
    check (status in ('open', 'pending', 'closed'))
);

--bun:split

-- One LIVE thread per (channel, identity) — but only while it is not closed.
-- The partial predicate is what makes a reply continue an existing conversation
-- while a new message after closure opens a fresh thread. Without it a number
-- could never come back a second time.
create unique index if not exists uq_support_threads_live_identity
  on support_threads (channel, channel_identity)
  where status <> 'closed' and deleted_at is null;

--bun:split

create index if not exists idx_support_threads_last_message
  on support_threads (last_message_at desc)
  where deleted_at is null;

--bun:split

-- Retention sweep: "closed for longer than the retention period". Narrow on
-- purpose — the purge job is the only reader.
create index if not exists idx_support_threads_status_updated
  on support_threads (status, updated_at)
  where deleted_at is null;

--bun:split

comment on table support_threads is
  'Instance-level support conversations captured from inbound bot channels. organization_uid/user_uid are attribution hints, never an access boundary.';

--bun:split

create table if not exists support_messages (
  uid          uuid primary key default gen_random_uuid(),
  thread_uid   uuid not null references support_threads(uid) on delete cascade,
  -- Denormalised from the thread so the idempotency index below is a plain
  -- two-column unique instead of a join.
  channel      text not null,
  direction    text not null,
  body         text not null,
  -- Bodies are attacker-influenced. Over the cap the body is stored truncated
  -- and flagged, rather than rejected — a truncated record still beats a lost
  -- one.
  truncated    boolean not null default false,
  -- text | image | audio | video | document | location | sticker | unsupported
  raw_type     text not null default 'text',
  -- Provider message id. Meta and Twilio BOTH retry on any non-2xx, so a
  -- webhook replay is a guaranteed occurrence, not an edge case.
  external_id  text,
  -- The SuperAdmin who sent an outbound reply. NULL for inbound.
  author_uid   uuid references users(uid),
  -- Delivery status of an outbound reply, same shape the notification path uses.
  delivery     jsonb,
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now(),
  constraint support_messages_direction_check
    check (direction in ('inbound', 'outbound'))
);

--bun:split

create index if not exists idx_support_messages_thread_created
  on support_messages (thread_uid, created_at);

--bun:split

-- Idempotency. Scoped per channel because provider ids are only unique within
-- a provider. Partial so outbound rows (which have no provider id until the
-- send returns, and may have none at all) do not collide on NULL.
create unique index if not exists uq_support_messages_external
  on support_messages (channel, external_id)
  where external_id is not null;

--bun:split

comment on table support_messages is
  'Messages within a support thread. (channel, external_id) is unique so a provider webhook retry cannot double-insert.';

-- ==========================================================================
-- SECTION: custom-domain-state
-- Split "temporarily failing" from "gone", and make recovery possible.
-- ==========================================================================

-- Until now a custom domain had exactly one counter (custom_domain_failures)
-- doing two jobs: "this is flaky right now" and "this domain has been taken
-- away from us". At three consecutive failures the page's verification was
-- cleared and it went dark — permanently, because the sweep only ever demoted.
-- A DNS blip spanning three six-hourly cycles took a customer's status page
-- offline until a human noticed and clicked Verify.
--
-- These columns make the lifecycle explicit and reversible:
--
--   none     no domain configured
--   pending  a domain is set but has never verified (only an operator promotes)
--   active   verified and serving
--   grace    verified, serving, but re-checks are currently failing
--   demoted  hard demotion: verification cleared, not served
--
-- grace KEEPS custom_domain_verified_at set, which is what keeps the page
-- serving through a blip. Hard demotion is what clears it.
alter table status_pages add column if not exists custom_domain_state text not null default 'none';

--bun:split

-- Consecutive SUCCESSFUL re-checks. The mirror image of custom_domain_failures,
-- and the counter re-promotion is earned with: a demoted domain needs several
-- consecutive successes (not one) before it is trusted again.
alter table status_pages add column if not exists custom_domain_successes integer not null default 0;

--bun:split

-- When the domain last entered `grace`. Makes "has stayed unreachable well past
-- the grace window" a readable fact instead of an inference from a counter and
-- a job interval.
alter table status_pages add column if not exists custom_domain_grace_since timestamptz;

--bun:split

-- Human-readable diagnostic from the last re-check: which mode was used, what
-- target was expected, what DNS actually returned, and the lookup error if any.
-- Without it, "verification fails but dig says the CNAME is right" can only be
-- investigated by correlating pod logs with manual dig runs.
alter table status_pages add column if not exists custom_domain_last_check text;

--bun:split

-- Backfill the state of existing rows from the columns that already encode it,
-- so an upgraded installation does not start every configured domain at 'none'.
update status_pages
   set custom_domain_state = case
         when custom_domain is null then 'none'
         when custom_domain_verified_at is not null then 'active'
         when custom_domain_checked_at is not null then 'demoted'
         else 'pending'
       end
 where custom_domain_state = 'none';

--bun:split

comment on column status_pages.custom_domain_state is
  'Custom-domain lifecycle: none | pending | active | grace | demoted. grace keeps serving; only demoted clears custom_domain_verified_at.';

-- ==========================================================================
-- SECTION: must-change-password
-- ==========================================================================

-- The seeded bootstrap admin ships with credentials published in a public
-- repository (admin@solidping.io / solidpass). Rather than make the seed
-- mode-dependent — a security posture that can be mis-detected — the account is
-- seeded exactly as before and marked here: a session authenticated as a flagged
-- user can reach the password-rotation endpoint and nothing else.
--
-- Deliberately NOT a column about the seeded admin. It is a general user-level
-- capability, reused for operator-initiated resets, invited users, and
-- compromised-credential response. Every consumer reads the column; nothing
-- keys on "is this the seeded admin".
--
-- Default false, so existing rows — including OAuth/SSO/LDAP users who have no
-- password at all and could not satisfy a rotation — are untouched.
alter table users add column if not exists must_change_password boolean not null default false;

--bun:split

comment on column users.must_change_password is
  'When true, an authenticated session for this user may reach only POST /auth/change-password, GET /auth/me and POST /auth/logout until the password is rotated.';
