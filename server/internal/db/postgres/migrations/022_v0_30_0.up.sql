-- v0.30.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.30.0 release. 021_v0_28_0 shipped in v0.28.0, so this is the next free
-- number and every schema change of this cycle is appended here as a new
-- SECTION, per wiki/conventions/database.md.
--
--   SECTION: user-contact-team-id   user_contacts.team_id, the workspace a
--                                   slack_user contact belongs to
--   SECTION: user-contact-dm-channel-id
--                                   user_contacts.dm_channel_id, the cached
--                                   Discord DM channel a `discord` contact is
--                                   paged through
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 022 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or drop and recreate the database.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything.

-- ==========================================================================
-- SECTION: user-contact-team-id  (spec 2026-09-19-02)
--
-- A `slack_user` contact stores a Slack user id and nothing else, so there was
-- no way to tell WHICH workspace that id belongs to. The on-call mention
-- resolver may only reuse a member's self-declared Slack handle when it is
-- certain the handle lives in the same workspace as the integration doing the
-- posting — a Slack user id from another workspace addresses a different
-- person entirely.
--
-- Deliberately NULLABLE with no backfill: every contact created before this
-- column existed genuinely has an unknown workspace, and inventing one would
-- be exactly the cross-workspace guess this column exists to prevent. The
-- resolver treats NULL as "unknown" and uses such a contact only when the org
-- has exactly one Slack integration, where there is nothing to cross.
--
-- Generic name rather than `slack_team_id`: the column answers "which provider
-- workspace/tenant is this contact's value scoped to", which is the same
-- question a future Discord-guild or Teams-tenant contact would ask.
-- ==========================================================================

alter table user_contacts add column if not exists team_id text;

comment on column user_contacts.team_id is
  'Provider workspace the contact value is scoped to (Slack team id). NULL = unknown workspace.';

-- ==========================================================================
-- SECTION: user-contact-dm-channel-id  (spec 2026-09-19-05)
--
-- A `discord` contact stores the member's Discord user id (a snowflake). Discord
-- will not accept a message addressed to a user: the bot must first call
-- `POST /users/@me/channels` to open a 1:1 DM channel and then post into that
-- channel id. That call is idempotent — Discord returns the SAME channel for the
-- same recipient forever — so re-opening it before every notice would be one
-- wasted, rate-limited round trip per page, on the exact path that is latency
-- sensitive.
--
-- Deliberately NULLABLE with no backfill: "not opened yet" is the honest state
-- for a contact the bot has never messaged, and there is no value that could be
-- computed offline — only Discord can mint a DM channel id. A NULL simply means
-- the next send opens the DM and caches what it gets back.
--
-- Deliberately NOT unique and not a foreign key: it is an opaque id owned by
-- Discord, it can be revoked on their side (a stale one answers 404, and the
-- sender then re-opens and re-caches), and the same channel legitimately appears
-- on two contacts when one human is a member of two organizations.
--
-- Generic name rather than `discord_dm_channel_id`: the column answers "which
-- provider-side conversation is this contact's value reachable through", the
-- same question a future Teams-chat or Matrix-room contact would ask.
-- ==========================================================================

alter table user_contacts add column if not exists dm_channel_id text;

comment on column user_contacts.dm_channel_id is
  'Cached provider-side DM conversation id for the contact (Discord DM channel). NULL = never opened.';
