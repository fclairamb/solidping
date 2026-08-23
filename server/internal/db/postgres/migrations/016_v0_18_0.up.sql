-- v0.18.0 (second file) — support inbox: threads and messages.
--
-- A SECOND file for the same unreleased release. 015_v0_18_0 already exists and
-- has been applied on developer databases; Bun keys applied migrations on the
-- NUMERIC PREFIX only, so appending to 015 would be silently skipped by every
-- database that already recorded it and then fail at runtime on a missing
-- table. This repository has hit exactly that during a pre-release
-- consolidation. Two files sharing a version label is fine; a renumber or an
-- in-place edit is not.
--
--   SECTION: support-inbox   support_threads + support_messages
--
-- ORDER IS LOAD-BEARING. The .down.sql unwinds in the exact reverse order.

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
