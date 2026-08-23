package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSupportInboxMigrationParity pins the two halves of the support-inbox
// section (spec 2026-08-22-02) that a functional test cannot reach: that the
// section exists in both directions, that its teardown really unwinds
// everything, and — the load-bearing one — that the SQLite mirror carries the
// SAME partial predicates as Postgres.
//
// The spec explicitly warns that the two dialects must agree on BEHAVIOR, not
// merely both apply. SQLite has supported partial indexes since 3.8.0, so the
// predicates are a genuine mirror and not a weaker fallback; without them a
// closed thread would still occupy the identity and a person could never come
// back a second time.
func TestSupportInboxMigrationParity(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	up := migrationSection(t, "support-inbox")
	r.Contains(up, "create table if not exists support_threads")
	r.Contains(up, "create table if not exists support_messages")

	// One LIVE thread per (channel, identity) — the predicate is what lets a
	// closed conversation be reopened as a fresh one.
	r.Contains(up, "uq_support_threads_live_identity")
	r.Contains(up, "where status <> 'closed' and deleted_at is null")

	// Idempotency against provider webhook retries, partial so outbound rows
	// with no provider id do not collide on NULL.
	r.Contains(up, "uq_support_messages_external")
	r.Contains(up, "on support_messages (channel, external_id)")
	r.Contains(up, "where external_id is not null")

	// The channel vocabulary reserves 'email' even though nothing writes it in
	// v1 — inbound email capture is a later spec, and reserving the value now
	// means it needs no migration.
	r.Contains(up, "'email'")

	down := downMigrationSection(t, "support-inbox")
	r.Contains(down, "drop table if exists support_messages")
	r.Contains(down, "drop table if exists support_threads")

	// It is a SECOND file for the same unreleased release, never an append to
	// 015. Bun keys applied migrations on the numeric prefix alone, so an
	// append is silently skipped by any database that already recorded 015 and
	// then fails at runtime on a missing table.
	for _, file := range []string{
		"migrations/014_v0_17_0.up.sql",
		"migrations/015_v0_18_0.up.sql",
		"migrations/014_v0_17_0.down.sql",
		"migrations/015_v0_18_0.down.sql",
	} {
		body, err := migrationsFS.ReadFile(file)
		r.NoError(err)
		r.NotContains(string(body), "-- SECTION: support-inbox\n",
			"%s already exists on developer databases; the support section must not be appended to it", file)
	}

	// Positive control for the loop above: the banner IS present in 016, in
	// both directions, so those NotContains assertions test a real string.
	for _, file := range []string{
		"migrations/016_v0_18_0.up.sql",
		"migrations/016_v0_18_0.down.sql",
	} {
		body, err := migrationsFS.ReadFile(file)
		r.NoError(err)
		r.Contains(string(body), "-- SECTION: support-inbox\n")
	}
}

// TestSupportThreadsLiveIdentityIndexIsEnforced proves the partial unique index
// is real in SQLite, not just spelled in the file.
//
// Two live threads on the same (channel, identity) must be REJECTED BY THE
// DATABASE — the application-level lookup is an optimization, not the
// guarantee, and two replicas racing on the same inbound message will both miss
// it. Closing one must then free the identity.
func TestSupportThreadsLiveIdentityIndexIsEnforced(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))

	t.Cleanup(func() { _ = svc.Close() })

	insert := func(uid, status string) error {
		_, execErr := svc.DB().ExecContext(ctx,
			`insert into support_threads (uid, channel, channel_identity, status, last_message_at)
			 values (?, 'whatsapp', '+33600000000', ?, datetime('now'))`, uid, status)

		return execErr
	}

	r.NoError(insert("t-1", "open"))
	r.Error(insert("t-2", "open"), "a second LIVE thread on the same identity must be rejected")
	r.Error(insert("t-3", "pending"), "pending is live too")

	// Close the first: the identity is free again, which is what lets somebody
	// come back a second time.
	_, err = svc.DB().ExecContext(ctx, `update support_threads set status = 'closed' where uid = 't-1'`)
	r.NoError(err)

	r.NoError(insert("t-4", "open"), "a message after closure must be able to open a fresh thread")

	// And a closed one alongside a live one is fine — the predicate excludes
	// closed rows from the constraint entirely.
	r.NoError(insert("t-5", "closed"))
}

// TestSupportMessagesExternalIDIsUnique proves the idempotency index. Meta and
// Twilio both retry on any non-2xx, so a replayed webhook is guaranteed, and
// the database is what has to stop it double-inserting when two replicas race.
func TestSupportMessagesExternalIDIsUnique(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))

	t.Cleanup(func() { _ = svc.Close() })

	_, err = svc.DB().ExecContext(ctx,
		`insert into support_threads (uid, channel, channel_identity, status, last_message_at)
		 values ('t-1', 'whatsapp', '+33600000000', 'open', datetime('now'))`)
	r.NoError(err)

	insert := func(uid string, externalID any) error {
		_, execErr := svc.DB().ExecContext(ctx,
			`insert into support_messages (uid, thread_uid, channel, direction, body, external_id)
			 values (?, 't-1', 'whatsapp', 'inbound', 'hello', ?)`, uid, externalID)

		return execErr
	}

	r.NoError(insert("m-1", "wamid.AAA"))
	r.Error(insert("m-2", "wamid.AAA"), "a retried provider message id must not double-insert")

	// A different channel may reuse the id: provider ids are only unique within
	// a provider.
	_, err = svc.DB().ExecContext(ctx,
		`insert into support_threads (uid, channel, channel_identity, status, last_message_at)
		 values ('t-2', 'telegram', '42', 'open', datetime('now'))`)
	r.NoError(err)

	_, err = svc.DB().ExecContext(ctx,
		`insert into support_messages (uid, thread_uid, channel, direction, body, external_id)
		 values ('m-3', 't-2', 'telegram', 'inbound', 'hello', 'wamid.AAA')`)
	r.NoError(err)

	// NULL external ids never collide — outbound replies frequently have none.
	r.NoError(insert("m-4", nil))
	r.NoError(insert("m-5", nil))
}
