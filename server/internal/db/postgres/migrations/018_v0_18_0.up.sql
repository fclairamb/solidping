-- v0.18.0 (fourth file) — forced password rotation as a user-level capability
-- (spec 2026-08-23-04).
--
-- A FOURTH file for the same unreleased release. 015-017 are already applied on
-- developer databases and Bun keys applied migrations on the NUMERIC PREFIX
-- only, so appending to any of them would be silently skipped by every database
-- that recorded it — and `internal/db/migrationguard` would fail the boot on the
-- changed checksum anyway.
--
--   SECTION: must-change-password   users.must_change_password

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
