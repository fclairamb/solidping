package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portPerCheckIncidentsMigration is distinct from every other
// _postgres_test.go file's port so the embedded servers never collide.
const portPerCheckIncidentsMigration = 15491

// TestPerCheckIncidentsMigrationParity_Postgres is the dialect-parity half of
// the group-incident retirement (spec 2026-08-24-14). The behavioral twin runs
// against SQLite in internal/db/sqlite/per_check_incidents_migration_test.go.
//
// The two dialects must agree on BEHAVIOR, not merely both apply — a migration
// that closed the wrong set of rows on one engine would be a silently
// divergent deployment. Pure text assertions on the shipped files, so this
// runs everywhere rather than only where an embedded Postgres can start.
func TestPerCheckIncidentsMigrationParity_Postgres(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	up := migrationSection(t, "per-check-incidents")

	// Member rows are cleared FIRST, scoped by subquery to the incidents about
	// to be closed. Order is load-bearing: run the other way round, the
	// subquery would match nothing and every member row would stay flagged.
	clearAt := strings.Index(up, "update incident_member_checks")
	closeAt := strings.Index(up, "update incidents")
	r.GreaterOrEqual(clearAt, 0, "the member-row clear must be in the section")
	r.Greater(closeAt, clearAt, "member rows must be cleared while their parent is still active")

	r.Contains(up, "set currently_failing = false")
	r.Contains(up, "and state = 1")
	r.Contains(up, "check_group_uid is not null")
	r.Contains(up, "deleted_at is null")

	// The close itself: resolved state, timestamps, an explanation on the row.
	r.Contains(up, "set state           = 2")
	r.Contains(up, "resolved_at     = coalesce(resolved_at, now())")
	r.Contains(up, "resolution_type = coalesce(resolution_type, 'auto')")
	r.Contains(up, "description")

	// Not scoped to a single org, and never touching per-check incidents.
	r.NotContains(up, "organization_uid =")

	// The teardown is a deliberate no-op — the up half changed data, not
	// schema, and there is no record of which rows to reopen.
	down := findMigrationSection(t, "down", "per-check-incidents")
	r.Contains(down, "NO-OP")

	// v0.18.0 is unreleased, so it stays ONE consolidated migration per
	// dialect (wiki/conventions/database.md): the section belongs in 015.
	body, err := migrationsFS.ReadFile("migrations/015_v0_18_0.up.sql")
	r.NoError(err)
	r.Contains(string(body), "-- SECTION: per-check-incidents\n")
}

// TestPerCheckIncidentsMigrationClosesActiveGroupIncidents_Postgres EXECUTES
// the section against a real Postgres, so the parity test above is not the
// only thing standing between a typo and a group incident stuck active
// forever.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestPerCheckIncidentsMigrationClosesActiveGroupIncidents_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portPerCheckIncidentsMigration, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("acmepci", "Acme PCI")
	r.NoError(svc.CreateOrganization(ctx, org))

	group := models.NewCheckGroup(org.UID, "RabbitMQ", "rabbitmq")
	r.NoError(svc.CreateCheckGroup(ctx, group))

	trigger := models.NewCheck(org.UID, "rabbitmq-nonprod", "tcp")
	trigger.CheckGroupUID = &group.UID
	r.NoError(svc.CreateCheck(ctx, trigger))

	member := models.NewCheck(org.UID, "rabbitmq-prod", "tcp")
	member.CheckGroupUID = &group.UID
	r.NoError(svc.CreateCheck(ctx, member))

	lonely := models.NewCheck(org.UID, "payments-api", "http")
	r.NoError(svc.CreateCheck(ctx, lonely))

	base := time.Date(2026, 8, 23, 23, 23, 30, 0, time.UTC)

	active := models.NewIncident(org.UID, trigger.UID, base, "RabbitMQ — 2/6 checks down")
	active.CheckGroupUID = &group.UID
	description := "opened by the group state machine"
	active.Description = &description
	r.NoError(svc.CreateIncident(ctx, active))

	r.NoError(svc.UpsertIncidentMemberCheck(ctx, &models.IncidentMemberCheck{
		IncidentUID: active.UID, CheckUID: member.UID,
		JoinedAt: base, FirstFailureAt: base, LastFailureAt: base,
		FailureCount: 1, CurrentlyFailing: true,
	}))

	// Negative control: an ordinary per-check incident must survive.
	perCheck := models.NewIncident(org.UID, lonely.UID, base, "payments-api is down")
	r.NoError(svc.CreateIncident(ctx, perCheck))

	for _, stmt := range pgBunSplitRE.Split(migrationSection(t, "per-check-incidents"), -1) {
		if !hasSQL(stmt) {
			continue
		}

		_, execErr := svc.DB().ExecContext(ctx, stmt)
		r.NoError(execErr, "statement failed:\n%s", stmt)
	}

	closed, err := svc.GetIncident(ctx, org.UID, active.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, closed.State)
	r.NotNil(closed.ResolvedAt)
	r.Equal("auto", *closed.ResolutionType)
	r.Contains(*closed.Description, description, "the note is appended, not substituted")
	r.Contains(*closed.Description, "per-check incidents")

	members, err := svc.ListIncidentMemberChecks(ctx, active.UID)
	r.NoError(err)
	r.Len(members, 1)
	r.False(members[0].CurrentlyFailing, "no member check may be left pointing at a closed incident")

	stillActive, err := svc.GetIncident(ctx, org.UID, perCheck.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateActive, stillActive.State,
		"a per-check incident must survive the migration untouched")
}
