-- v0.30.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.30.0 release. 021_v0_28_0 shipped in v0.28.0, so this is the next free
-- number and every schema change of this cycle is appended here as a new
-- SECTION, per wiki/conventions/database.md.
--
--   SECTION: user-contact-team-id   user_contacts.team_id, the workspace a
--                                   slack_user contact belongs to
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
