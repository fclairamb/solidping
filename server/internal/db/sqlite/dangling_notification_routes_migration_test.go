package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// danglingRoutesMigrationSQL is the dangling-notification-routes SECTION of the
// consolidated v0.17.0 migration, read straight out of the embedded FS so this
// test can never drift from the SQL that actually ships. Only that section is
// replayed: the rest of the consolidated file is not re-runnable. The section
// itself is idempotent (a DELETE guarded by NOT EXISTS is a no-op once there is
// nothing left to delete), so replaying it after Initialize() already applied it
// to an empty database is safe — the same pattern as this package's other
// migration tests.
func danglingRoutesMigrationSQL(t *testing.T) string {
	t.Helper()

	return migrationSection(t, "dangling-notification-routes")
}

// TestDanglingNotificationRoutesMigration pins spec 2026-08-20-02's data
// migration: the routes left behind by historical SOFT contact deletions are
// removed, and a route backed by a live contact is untouched.
//
// The two dangling shapes are covered separately because they arrive by
// different paths and the `on delete cascade` FK only ever handled one of them:
//
//   - contact soft-deleted (deleted_at set) — what EVERY deletion in this
//     system produced before the fix: the dashboard's "remove method", the
//     webpush prune on a 410 Gone, the Telegram /stop webhook. The cascade
//     never fires for these, so the route survived its contact.
//   - contact row gone outright — belt and braces. The FK should make this
//     unreachable, but the cleanup costs nothing extra and a route pointing at
//     nothing is garbage either way.
func TestDanglingNotificationRoutesMigration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	exec := func(query string, args ...any) {
		t.Helper()

		_, execErr := svc.DB().ExecContext(ctx, query, args...)
		r.NoError(execErr)
	}

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(svc.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	exec(`insert into organizations (uid, slug, name) values ('org-1', 'acme', 'Acme')`)
	exec(`insert into users (uid, email) values ('user-1', 'alice@acme.com')`)

	// A live contact and its route — the bystander that must survive.
	exec(`insert into user_contacts (uid, user_uid, organization_uid, type, value, label)
	      values ('contact-live', 'user-1', 'org-1', 'email', 'alice@acme.com', '')`)
	exec(`insert into user_notification_routes (uid, user_uid, org_uid, contact_uid)
	      values ('route-live', 'user-1', 'org-1', 'contact-live')`)

	// A soft-deleted contact whose route was left behind — the ghost row.
	exec(`insert into user_contacts (uid, user_uid, organization_uid, type, value, label, deleted_at)
	      values ('contact-soft-deleted', 'user-1', 'org-1', 'phone', '+15550001111', '', datetime('now'))`)
	exec(`insert into user_notification_routes (uid, user_uid, org_uid, contact_uid)
	      values ('route-ghost', 'user-1', 'org-1', 'contact-soft-deleted')`)

	// A route whose contact row does not exist at all. The FK is disabled for
	// this one insert so the row can be created; the migration must still
	// collect it.
	exec(`pragma foreign_keys = off`)
	exec(`insert into user_notification_routes (uid, user_uid, org_uid, contact_uid)
	      values ('route-orphan', 'user-1', 'org-1', 'contact-vanished')`)
	exec(`pragma foreign_keys = on`)

	r.Equal(3, count(`select count(*) from user_notification_routes`),
		"the fixture must actually have created all three routes")

	migration := danglingRoutesMigrationSQL(t)

	_, err = svc.DB().ExecContext(ctx, migration)
	r.NoError(err)

	r.Equal(0, count(`select count(*) from user_notification_routes where uid = 'route-ghost'`),
		"the route whose contact is soft-deleted must be removed")
	r.Equal(0, count(`select count(*) from user_notification_routes where uid = 'route-orphan'`),
		"the route whose contact row is gone must be removed")
	r.Equal(1, count(`select count(*) from user_notification_routes where uid = 'route-live'`),
		"the route backed by a live contact must survive")

	// The contacts themselves are untouched — this cleans up join rows, it does
	// not hard-delete anybody's soft-deleted contact history.
	r.Equal(1, count(`select count(*) from user_contacts where uid = 'contact-soft-deleted'`),
		"the soft-deleted contact row itself must be left alone")
	r.Equal(1, count(`select count(*) from user_contacts where uid = 'contact-live'`),
		"the live contact must be left alone")

	// Idempotent — re-running against an already-clean database is a no-op.
	_, err = svc.DB().ExecContext(ctx, migration)
	r.NoError(err)
	r.Equal(1, count(`select count(*) from user_notification_routes`),
		"a second run must not delete the surviving route")
}
