-- v0.18.0 (second file) — support inbox: threads and messages.
--
-- SQLite mirror of postgres/migrations/016_v0_18_0.up.sql. See the Postgres
-- file for the rationale of every column, of the partial unique index, and of
-- why this is a SECOND file rather than an append to 015_v0_18_0 (Bun keys
-- applied migrations on the numeric prefix only, so an append is silently
-- skipped by any database that already recorded 015).
--
--   SECTION: support-inbox   support_threads + support_messages
--
-- Dialect differences: no gen_random_uuid() (the Go layer supplies the UID),
-- uuid -> text, timestamptz -> text, jsonb -> text, boolean -> integer.
-- SQLite HAS supported partial indexes since 3.8.0, so the "live thread"
-- predicate below is a genuine mirror of the Postgres one rather than a
-- weaker fallback — the two dialects agree on BEHAVIOUR, not merely both apply.

-- ==========================================================================
-- SECTION: support-inbox
-- Instance-level capture of human messages our bots cannot parse
-- (spec 2026-08-22-02).
-- ==========================================================================

create table if not exists support_threads (
  uid               text primary key,
  channel           text not null,
  channel_identity  text not null,
  channel_context   text,
  subject           text not null default '',
  status            text not null default 'open',
  organization_uid  text references organizations(uid),
  user_uid          text references users(uid),
  last_message_at   text not null default (datetime('now')),
  last_inbound_at   text,
  unread_count      integer not null default 0,
  last_mirror_at    text,
  pending_mirrors   integer not null default 0,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text,
  constraint support_threads_channel_check
    check (channel in ('whatsapp', 'telegram', 'sms', 'slack', 'discord', 'email')),
  constraint support_threads_status_check
    check (status in ('open', 'pending', 'closed'))
);

--bun:split

create unique index if not exists uq_support_threads_live_identity
  on support_threads (channel, channel_identity)
  where status <> 'closed' and deleted_at is null;

--bun:split

create index if not exists idx_support_threads_last_message
  on support_threads (last_message_at desc)
  where deleted_at is null;

--bun:split

create index if not exists idx_support_threads_status_updated
  on support_threads (status, updated_at)
  where deleted_at is null;

--bun:split

create table if not exists support_messages (
  uid          text primary key,
  thread_uid   text not null references support_threads(uid) on delete cascade,
  channel      text not null,
  direction    text not null,
  body         text not null,
  truncated    integer not null default 0,
  raw_type     text not null default 'text',
  external_id  text,
  author_uid   text references users(uid),
  delivery     text,
  created_at   text not null default (datetime('now')),
  updated_at   text not null default (datetime('now')),
  constraint support_messages_direction_check
    check (direction in ('inbound', 'outbound'))
);

--bun:split

create index if not exists idx_support_messages_thread_created
  on support_messages (thread_uid, created_at);

--bun:split

create unique index if not exists uq_support_messages_external
  on support_messages (channel, external_id)
  where external_id is not null;
