package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// portAuditActorMetadata / portAuditActorMetadataRollback are distinct from
// every other _postgres_test.go embedded port in the repo (see the
// port-numbering note in postgres_headroom_postgres_test.go). The rollback test
// destroys schema, so it must not share a database with the apply test.
const (
	portAuditActorMetadata         = 15490
	portAuditActorMetadataRollback = 15491
)

// TestAuditActorMetadataMigration_Postgres proves the audit-actor-metadata
// section (spec 2026-08-21-09) lands on Postgres.
//
// Worth a dedicated Postgres test even though SQLite is covered: the two
// migrations are NOT the same SQL. Postgres widens the actor_type domain with
// DROP CONSTRAINT / ADD CONSTRAINT; SQLite cannot alter a CHECK at all and has
// to rebuild the table. Two different mechanisms for one contract is exactly
// the shape of change that works on one engine and silently not the other.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestAuditActorMetadataMigration_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portAuditActorMetadata, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(svc.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	eventsColumn := func(name string) int {
		t.Helper()

		return count(`select count(*) from information_schema.columns
		              where table_name = 'events' and column_name = ?`, name)
	}

	r.Equal(1, eventsColumn("source_ip"))
	r.Equal(1, eventsColumn("user_agent"))
	// Positive control: the query really inspects this table, and the
	// pre-existing columns are untouched.
	r.Equal(1, eventsColumn("actor_uid"))
	r.Equal(1, eventsColumn("payload"))

	r.Equal(1, count(`select count(*) from pg_indexes where indexname = 'idx_events_org_type_created'`))
	r.Equal(1, count(`select count(*) from pg_indexes where indexname = 'idx_events_created'`))

	org := "44444444-4444-4444-4444-444444444444"
	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acmeaudit', 'Acme Audit')`, org)
	r.NoError(err)

	insert := func(uid, eventType, actorType string) error {
		_, execErr := svc.DB().ExecContext(ctx,
			`insert into events (uid, organization_uid, event_type, actor_type)
			 values (?, ?, ?, ?)`,
			uid, org, eventType, actorType)

		return execErr
	}

	// Positive control: the kinds that always worked still do.
	r.NoError(insert("11111111-0000-0000-0000-000000000001", "check.created", "system"))
	r.NoError(insert("11111111-0000-0000-0000-000000000002", "check.updated", "user"))

	// The point of the section.
	r.NoError(insert("11111111-0000-0000-0000-000000000003", "auth.token_created", "api_token"))
	r.NoError(insert("11111111-0000-0000-0000-000000000004", "config.applied", "service"))

	// Negative control: widened, not removed. Without this a "fix" that simply
	// dropped the constraint would pass everything above.
	r.Error(insert("11111111-0000-0000-0000-000000000005", "auth.logout", "wizard"))

	// source_ip / user_agent really store what they claim to.
	_, err = svc.DB().ExecContext(ctx,
		`insert into events (uid, organization_uid, event_type, actor_type, source_ip, user_agent)
		 values ('11111111-0000-0000-0000-000000000006', ?, 'auth.login_succeeded', 'user',
		         '2001:db8::1', 'Mozilla/5.0')`, org)
	r.NoError(err)

	var ip, agent string
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select source_ip, user_agent from events where uid = '11111111-0000-0000-0000-000000000006'`,
	).Scan(&ip, &agent))
	// An IPv6 literal is 39 characters; varchar(45) must not be truncating it.
	r.Equal("2001:db8::1", ip)
	r.Equal("Mozilla/5.0", agent)
}

// TestAuditActorMetadataRollback_Postgres EXECUTES the teardown half against a
// migrated, populated database.
//
// The load-bearing assertions are the last ones: `events` survives with its
// ordinary rows and columns, and the NARROWED constraint is genuinely back in
// force. A teardown that dropped the constraint instead of restoring the old
// one would satisfy "the columns are gone" just as well.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestAuditActorMetadataRollback_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portAuditActorMetadataRollback, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(svc.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	eventsColumn := func(name string) int {
		t.Helper()

		return count(`select count(*) from information_schema.columns
		              where table_name = 'events' and column_name = ?`, name)
	}

	// Applied.
	r.Equal(1, eventsColumn("source_ip"))
	r.Equal(1, eventsColumn("user_agent"))

	org := "44444444-4444-4444-4444-444444444444"
	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acmeauditroll', 'Acme Audit Rollback')`, org)
	r.NoError(err)

	keep := "22222222-0000-0000-0000-000000000001"
	drop := "22222222-0000-0000-0000-000000000002"

	_, err = svc.DB().ExecContext(ctx,
		`insert into events (uid, organization_uid, event_type, actor_type, source_ip, user_agent)
		 values (?, ?, 'check.created', 'user', '203.0.113.7', 'curl/8')`, keep, org)
	r.NoError(err)
	_, err = svc.DB().ExecContext(ctx,
		`insert into events (uid, organization_uid, event_type, actor_type)
		 values (?, ?, 'auth.token_created', 'api_token')`, drop, org)
	r.NoError(err)

	// Rolled back, statement by statement, exactly as bun would run it.
	for _, stmt := range pgBunSplitRE.Split(downMigrationSection(t, "audit-actor-metadata"), -1) {
		if !hasSQL(stmt) {
			continue
		}

		_, execErr := svc.DB().ExecContext(ctx, stmt)
		r.NoError(execErr, "statement failed:\n%s", stmt)
	}

	r.Zero(eventsColumn("source_ip"), "source_ip must be gone after rollback")
	r.Zero(eventsColumn("user_agent"), "user_agent must be gone after rollback")
	r.Zero(count(`select count(*) from pg_indexes where indexname = 'idx_events_org_type_created'`))
	r.Zero(count(`select count(*) from pg_indexes where indexname = 'idx_events_created'`))

	for _, column := range []string{
		"uid", "organization_uid", "incident_uid", "check_uid", "job_uid",
		"event_type", "actor_type", "actor_uid", "payload", "created_at",
	} {
		r.Equal(1, eventsColumn(column), "events.%s must survive the rollback", column)
	}

	r.Equal(1, count(`select count(*) from events where uid = ?`, keep),
		"a user-attributed event must survive the rollback")
	r.Zero(count(`select count(*) from events where uid = ?`, drop),
		"an api_token-attributed event is documented as lossy on the way down")

	// The narrowed constraint is genuinely back — not merely absent.
	_, err = svc.DB().ExecContext(ctx,
		`insert into events (uid, organization_uid, event_type, actor_type)
		 values ('22222222-0000-0000-0000-000000000003', ?, 'auth.token_created', 'api_token')`, org)
	r.Error(err)

	_, err = svc.DB().ExecContext(ctx,
		`insert into events (uid, organization_uid, event_type, actor_type)
		 values ('22222222-0000-0000-0000-000000000004', ?, 'check.deleted', 'user')`, org)
	r.NoError(err)
}
