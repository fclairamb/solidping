package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// eventsColumns returns the column set of the `events` table.
func eventsColumns(ctx context.Context, t *testing.T, svc *Service) map[string]bool {
	t.Helper()

	rows, err := svc.DB().QueryContext(ctx, `select name from pragma_table_info('events')`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	out := map[string]bool{}

	for rows.Next() {
		var name string

		require.NoError(t, rows.Scan(&name))

		out[name] = true
	}

	require.NoError(t, rows.Err())
	require.NotEmpty(t, out, "pragma_table_info must have enumerated the events table")

	return out
}

// eventsIndexExists reports whether an index on `events` is present.
func eventsIndexExists(ctx context.Context, t *testing.T, svc *Service, name string) bool {
	t.Helper()

	var count int

	require.NoError(t, svc.DB().QueryRowContext(ctx,
		`select count(*) from sqlite_master where type = 'index' and name = ?`, name,
	).Scan(&count))

	return count == 1
}

// seedAuditOrg inserts a minimal organization and returns its UID.
func seedAuditOrg(ctx context.Context, t *testing.T, svc *Service) string {
	t.Helper()

	orgUID := "44444444-4444-4444-4444-444444444444"

	_, err := svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acme', 'Acme')`, orgUID)
	require.NoError(t, err)

	return orgUID
}

// insertEvent inserts one raw events row, returning the driver error (if any)
// so a test can assert on a CHECK-constraint rejection.
func insertEvent(ctx context.Context, svc *Service, uid, orgUID, eventType, actorType string) error {
	_, err := svc.DB().ExecContext(ctx,
		`insert into events (uid, organization_uid, event_type, actor_type) values (?, ?, ?, ?)`,
		uid, orgUID, eventType, actorType)

	return err
}

// TestAuditActorMetadataSectionShipsBothDialectHalves pins the four halves of
// the audit-actor-metadata section (spec 2026-08-21-09) that a functional test
// cannot reach: the SECTION exists in both directions, and the down half
// unwinds what the up half created.
func TestAuditActorMetadataSectionShipsBothDialectHalves(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	up := migrationSection(t, "audit-actor-metadata")
	r.Contains(up, "create table events_new")
	r.Contains(up, "source_ip")
	r.Contains(up, "user_agent")
	r.Contains(up, "'api_token'")
	r.Contains(up, "'service'")
	r.Contains(up, "idx_events_org_type_created")
	r.Contains(up, "idx_events_created")

	// Every index the rebuild dropped with the old table must be recreated, or
	// the swap silently costs the audit UI its query plans.
	for _, index := range []string{
		"idx_events_org_created",
		"idx_events_org_incident_created",
		"idx_events_check_created",
		"idx_events_type_created",
		"idx_events_actor",
	} {
		r.Contains(up, index)
	}

	down := downMigrationSection(t, "audit-actor-metadata")
	r.Contains(down, "create table events_old")
	r.Contains(down, "delete from events where actor_type in ('api_token', 'service')")
	r.Contains(down, "check (actor_type in ('system', 'user'))")
}

// TestAuditActorMetadataColumnsPresent proves the section actually ran on a
// fresh database.
func TestAuditActorMetadataColumnsPresent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))

	t.Cleanup(func() { _ = svc.Close() })

	columns := eventsColumns(ctx, t, svc)
	r.True(columns["source_ip"], "events.source_ip must exist after migration")
	r.True(columns["user_agent"], "events.user_agent must exist after migration")
	// Positive control: the pragma really enumerated the table, and the
	// rebuild preserved the pre-existing columns rather than replacing them.
	r.True(columns["uid"])
	r.True(columns["actor_uid"])
	r.True(columns["payload"])

	r.True(eventsIndexExists(ctx, t, svc, "idx_events_org_type_created"))
	r.True(eventsIndexExists(ctx, t, svc, "idx_events_created"))
	// The rebuild must not have lost the original indexes.
	r.True(eventsIndexExists(ctx, t, svc, "idx_events_org_created"))
	r.True(eventsIndexExists(ctx, t, svc, "idx_events_actor"))
}

// TestAuditActorTypeConstraintAdmitsTheNewKindsAndOnlyThose is the load-bearing
// schema guard. It asserts the widened CHECK in BOTH directions:
//
//   - the two new actor kinds are accepted (without which every token- and
//     service-attributed audit write would fail at runtime), and
//   - an arbitrary value is still REJECTED.
//
// The second half is what stops the "fix" of dropping the constraint entirely
// from passing this test.
func TestAuditActorTypeConstraintAdmitsTheNewKindsAndOnlyThose(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))

	t.Cleanup(func() { _ = svc.Close() })

	orgUID := seedAuditOrg(ctx, t, svc)

	// Positive control: the two kinds that always worked still do.
	r.NoError(insertEvent(ctx, svc, "e-system", orgUID, "check.created", "system"))
	r.NoError(insertEvent(ctx, svc, "e-user", orgUID, "check.updated", "user"))

	// The point of the section.
	r.NoError(insertEvent(ctx, svc, "e-token", orgUID, "auth.token_created", "api_token"))
	r.NoError(insertEvent(ctx, svc, "e-service", orgUID, "config.applied", "service"))

	// Negative control: the constraint is widened, not removed.
	r.Error(insertEvent(ctx, svc, "e-bogus", orgUID, "auth.logout", "wizard"))
}

// TestAuditActorMetadataRollback EXECUTES the teardown half against a migrated
// database holding rows of every actor kind.
//
// The load-bearing assertions are the last two: `events` must survive the
// rebuild with its pre-existing rows and columns intact (a teardown that took
// the table with it would satisfy "the columns are gone" just as well), and the
// narrowed constraint must be back in force.
func TestAuditActorMetadataRollback(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))

	t.Cleanup(func() { _ = svc.Close() })

	orgUID := seedAuditOrg(ctx, t, svc)

	_, err = svc.DB().ExecContext(ctx,
		`insert into events (uid, organization_uid, event_type, actor_type, source_ip, user_agent)
		 values ('keep-me', ?, 'check.created', 'user', '203.0.113.7', 'curl/8')`, orgUID)
	r.NoError(err)
	r.NoError(insertEvent(ctx, svc, "drop-me", orgUID, "auth.token_created", "api_token"))

	execMigrationStatements(ctx, t, svc, downMigrationSection(t, "audit-actor-metadata"))

	after := eventsColumns(ctx, t, svc)
	r.False(after["source_ip"], "source_ip must be gone after rollback")
	r.False(after["user_agent"], "user_agent must be gone after rollback")

	// The table and its ordinary rows survive.
	r.True(after["uid"])
	r.True(after["actor_uid"])
	r.True(after["payload"])

	var surviving int
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from events where uid = 'keep-me'`).Scan(&surviving))
	r.Equal(1, surviving, "a system/user-attributed event must survive the rollback")

	var dropped int
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from events where uid = 'drop-me'`).Scan(&dropped))
	r.Equal(0, dropped, "an api_token-attributed event is documented as lossy on the way down")

	// The narrowed constraint is genuinely back.
	r.Error(insertEvent(ctx, svc, "e-token-2", orgUID, "auth.token_created", "api_token"))
	r.NoError(insertEvent(ctx, svc, "e-user-2", orgUID, "check.deleted", "user"))
}
