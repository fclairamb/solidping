package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// portDanglingNotificationRoutes is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portDanglingNotificationRoutes = 15483

// TestDanglingNotificationRoutesMigration_Postgres is the Postgres twin of the
// SQLite migration test: spec 2026-08-20-02's data migration removes the
// notification routes left behind by historical SOFT contact deletions, and
// leaves a route backed by a live contact alone.
//
// Worth running on both dialects even though the intent is identical: the two
// files are not the same SQL. Postgres aliases the DELETE target (`... r`) and
// SQLite cannot, so each dialect's correlated subquery is written differently
// and each one has to be proven separately.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestDanglingNotificationRoutesMigration_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portDanglingNotificationRoutes, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	// Only the dangling-notification-routes section is replayed: the rest of
	// the consolidated v0.17.0 migration is not re-runnable, while this section
	// (one guarded DELETE) is.
	migration := migrationSection(t, "dangling-notification-routes")

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

	org := "11111111-1111-1111-1111-111111111111"
	user := "22222222-2222-2222-2222-222222222222"
	contactLive := "33333333-3333-3333-3333-333333333333"
	contactDeleted := "44444444-4444-4444-4444-444444444444"
	contactVanished := "77777777-7777-7777-7777-777777777777"
	routeLive := "55555555-5555-5555-5555-555555555555"
	routeGhost := "66666666-6666-6666-6666-666666666666"
	routeOrphan := "88888888-8888-8888-8888-888888888888"

	exec(`insert into organizations (uid, slug, name) values (?, 'acme', 'Acme')`, org)
	exec(`insert into users (uid, email) values (?, 'alice@acme.com')`, user)

	// A live contact and its route — the bystander that must survive.
	exec(`insert into user_contacts (uid, user_uid, organization_uid, type, value, label)
	      values (?, ?, ?, 'email', 'alice@acme.com', '')`, contactLive, user, org)
	exec(`insert into user_notification_routes (uid, user_uid, org_uid, contact_uid)
	      values (?, ?, ?, ?)`, routeLive, user, org, contactLive)

	// A soft-deleted contact whose route was left behind — the ghost row. This
	// is what EVERY contact deletion produced before the fix: the `on delete
	// cascade` FK only fires on a hard delete.
	exec(`insert into user_contacts (uid, user_uid, organization_uid, type, value, label, deleted_at)
	      values (?, ?, ?, 'phone', '+15550001111', '', now())`, contactDeleted, user, org)
	exec(`insert into user_notification_routes (uid, user_uid, org_uid, contact_uid)
	      values (?, ?, ?, ?)`, routeGhost, user, org, contactDeleted)

	// A route whose contact row does not exist AT ALL. The FK should make this
	// unreachable, so the fixture has to drop the constraint to build it — but
	// the cleanup must collect it anyway: `not exists` is deliberately written
	// to cover "no row" as well as "soft-deleted row", and a migration that
	// only handled the shape the FK already protects would be untested on the
	// shape it does not.
	//
	// The constraint name is looked up rather than hardcoded: 001_v0_1_0 uses
	// an inline `references`, so Postgres named it, and betting on
	// `user_notification_routes_contact_uid_fkey` would make this test a
	// tripwire for a rename it does not care about.
	var fkName string

	r.NoError(svc.DB().QueryRowContext(ctx, `
		select c.conname
		from pg_constraint c
		join pg_class t on t.oid = c.conrelid
		where t.relname = 'user_notification_routes'
		  and c.contype = 'f'
		  and pg_get_constraintdef(c.oid) ilike '%user_contacts%'
		limit 1`).Scan(&fkName))
	r.NotEmpty(fkName, "the contact_uid foreign key must exist to be dropped")

	exec(`alter table user_notification_routes drop constraint ` + fkName)
	exec(`insert into user_notification_routes (uid, user_uid, org_uid, contact_uid)
	      values (?, ?, ?, ?)`, routeOrphan, user, org, contactVanished)

	r.Equal(3, count(`select count(*) from user_notification_routes`),
		"the fixture must actually have created all three routes")

	// The migration itself.
	_, err = svc.DB().ExecContext(ctx, migration)
	r.NoError(err)

	r.Equal(0, count(`select count(*) from user_notification_routes where uid = ?`, routeGhost),
		"the route whose contact is soft-deleted must be removed")
	r.Equal(0, count(`select count(*) from user_notification_routes where uid = ?`, routeOrphan),
		"the route whose contact row is gone must be removed")
	r.Equal(1, count(`select count(*) from user_notification_routes where uid = ?`, routeLive),
		"the route backed by a live contact must survive")

	// Restoring the FK is itself an assertion: it can only succeed if the
	// cleanup left NO route pointing at a nonexistent contact.
	exec(`alter table user_notification_routes
	      add constraint ` + fkName + ` foreign key (contact_uid) references user_contacts(uid) on delete cascade`)

	// The contacts themselves are untouched — this cleans up join rows, it does
	// not hard-delete anybody's soft-deleted contact history.
	r.Equal(1, count(`select count(*) from user_contacts where uid = ?`, contactDeleted),
		"the soft-deleted contact row itself must be left alone")
	r.Equal(1, count(`select count(*) from user_contacts where uid = ?`, contactLive),
		"the live contact must be left alone")

	// Idempotent — re-running against an already-clean database is a no-op.
	_, err = svc.DB().ExecContext(ctx, migration)
	r.NoError(err)
	r.Equal(1, count(`select count(*) from user_notification_routes`),
		"a second run must not delete the surviving route")
}
