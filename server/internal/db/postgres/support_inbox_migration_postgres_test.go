package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSupportInboxMigrationPostgresParity is the Postgres half of the
// support-inbox parity check (spec 2026-08-22-02). Its SQLite twin lives in
// internal/db/sqlite/support_inbox_migration_test.go.
//
// The spec is explicit that the two dialects must agree on BEHAVIOR, not merely
// both apply. The predicates asserted here are the same ones asserted there —
// if either file drops one, the two sides stop meaning the same thing and one
// deployment silently gains a bug the other does not have.
//
// Pure text assertions on the shipped file: no database needed, so this runs
// everywhere rather than only where testcontainers can start.
func TestSupportInboxMigrationPostgresParity(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	up := migrationSection(t, "support-inbox")
	r.Contains(up, "create table if not exists support_threads")
	r.Contains(up, "create table if not exists support_messages")

	// One LIVE thread per (channel, identity).
	r.Contains(up, "uq_support_threads_live_identity")
	r.Contains(up, "where status <> 'closed' and deleted_at is null")

	// Idempotency against provider webhook retries.
	r.Contains(up, "uq_support_messages_external")
	r.Contains(up, "on support_messages (channel, external_id)")
	r.Contains(up, "where external_id is not null")

	// Attribution is NULLABLE by design: most senders are a bare phone number
	// with no organization to attribute at all, and a message from a stranger
	// must not be dropped for lack of one.
	r.Contains(up, "organization_uid  uuid references organizations(uid)")
	r.Contains(up, "user_uid          uuid references users(uid)")
	r.NotContains(up, "organization_uid  uuid not null")

	down := findMigrationSection(t, "down", "support-inbox")
	r.Contains(down, "drop table if exists support_messages")
	r.Contains(down, "drop table if exists support_threads")

	// Consolidated into 015 (2026-08-24): v0.18.0 is unreleased, so it carries
	// exactly ONE migration file per dialect, per wiki/conventions/database.md.
	// The support section therefore lives in 015 and 016 must not exist — a
	// reappearing 016 means someone added a second file for this release again.
	body, err := migrationsFS.ReadFile("migrations/015_v0_18_0.up.sql")
	r.NoError(err)
	r.Contains(string(body), "-- SECTION: support-inbox\n")

	_, err = migrationsFS.ReadFile("migrations/016_v0_18_0.up.sql")
	r.Error(err, "v0.18.0 must remain a single consolidated migration per dialect")
}
