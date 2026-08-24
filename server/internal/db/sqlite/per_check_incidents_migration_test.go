package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// execMigrationSection replays one migration section statement by statement,
// honoring bun's `--bun:split` separator. The section is pure UPDATEs, so
// replaying it against an already-initialized database is legal — which is the
// only reason this test can seed rows the migration then has to act on.
func execMigrationSection(ctx context.Context, t *testing.T, svc *Service, section string) {
	t.Helper()

	for _, stmt := range bunSplitRE.Split(section, -1) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}

		hasSQL := false

		for _, line := range strings.Split(stmt, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
				hasSQL = true

				break
			}
		}

		if !hasSQL {
			continue
		}

		_, err := svc.DB().ExecContext(ctx, stmt)
		require.NoError(t, err, "statement failed:\n%s", stmt)
	}
}

// TestPerCheckIncidentsMigrationClosesActiveGroupIncidents executes the
// `per-check-incidents` section of the v0.18.0 migration (spec 2026-08-24-14).
//
// The group WRITE path is deleted from the binary, so an active group incident
// would have no code left to close it — it would sit on the dashboard as a
// permanent outage nobody can resolve. The migration closes those, and ONLY
// those: an already-resolved group incident is history and must not be
// rewritten, and a per-check incident is the new normal and must not be
// touched at all.
//
// Both negative controls are asserted, because an `update ... where` that lost
// its predicate would still satisfy the positive assertion perfectly.
func TestPerCheckIncidentsMigrationClosesActiveGroupIncidents(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	org := models.NewOrganization("acme", "Acme")
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

	// 1. Negative control: an already-RESOLVED group incident, member row still
	//    flagged failing. History — the migration must leave every byte of it
	//    alone, member flag included.
	historical := models.NewIncident(org.UID, trigger.UID, base.Add(-72*time.Hour), "RabbitMQ — 1/6 checks down")
	historical.CheckGroupUID = &group.UID
	historicalDescription := "an outage from last week"
	historical.Description = &historicalDescription
	resolvedState := models.IncidentStateResolved
	resolvedAt := base.Add(-71 * time.Hour)
	manual := "manual"
	r.NoError(svc.CreateIncident(ctx, historical))
	r.NoError(svc.UpdateIncident(ctx, historical.UID, &models.IncidentUpdate{
		State: &resolvedState, ResolvedAt: &resolvedAt, ResolutionType: &manual,
	}))
	r.NoError(svc.UpsertIncidentMemberCheck(ctx, &models.IncidentMemberCheck{
		IncidentUID: historical.UID, CheckUID: member.UID,
		JoinedAt: base.Add(-72 * time.Hour), FirstFailureAt: base.Add(-72 * time.Hour),
		LastFailureAt: base.Add(-72 * time.Hour), FailureCount: 1, CurrentlyFailing: true,
	}))

	// 2. The row this migration exists for: an ACTIVE group incident with a
	//    member still flagged as failing. Seeded AFTER the resolved one because
	//    uq_active_group_incident allows a single active incident per group.
	active := models.NewIncident(org.UID, trigger.UID, base, "RabbitMQ — 2/6 checks down")
	active.CheckGroupUID = &group.UID
	activeDescription := "opened by the group state machine"
	active.Description = &activeDescription
	r.NoError(svc.CreateIncident(ctx, active))

	r.NoError(svc.UpsertIncidentMemberCheck(ctx, &models.IncidentMemberCheck{
		IncidentUID: active.UID, CheckUID: member.UID,
		JoinedAt: base, FirstFailureAt: base, LastFailureAt: base,
		FailureCount: 1, CurrentlyFailing: true,
	}))

	// 3. Negative control: an ordinary ACTIVE per-check incident. This is what
	//    every incident looks like from now on, and closing one would take a
	//    live outage off the dashboard.
	perCheck := models.NewIncident(org.UID, lonely.UID, base, "payments-api is down")
	r.NoError(svc.CreateIncident(ctx, perCheck))

	execMigrationSection(ctx, t, svc, migrationSection(t, "per-check-incidents"))

	// The active group incident is closed, with an explanation on the record.
	closed, err := svc.GetIncident(ctx, org.UID, active.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, closed.State, "an active group incident must be closed")
	r.NotNil(closed.ResolvedAt)
	r.NotNil(closed.ResolutionType)
	r.Equal("auto", *closed.ResolutionType)
	r.NotNil(closed.Description)
	r.Contains(*closed.Description, "opened by the group state machine",
		"the original description survives — the note is appended, not substituted")
	r.Contains(*closed.Description, "per-check incidents")

	// Its member row no longer points at a dead incident.
	activeMembers, err := svc.ListIncidentMemberChecks(ctx, active.UID)
	r.NoError(err)
	r.Len(activeMembers, 1)
	r.False(activeMembers[0].CurrentlyFailing,
		"no member check may be left pointing at a closed incident")

	// The resolved group incident is untouched, member flag included.
	untouched, err := svc.GetIncident(ctx, org.UID, historical.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, untouched.State)
	r.Equal(resolvedAt.UTC(), untouched.ResolvedAt.UTC(), "a resolved incident keeps its own timestamp")
	r.Equal("manual", *untouched.ResolutionType, "a resolved incident keeps its own resolution type")
	r.Equal(historicalDescription, *untouched.Description, "history is not rewritten")

	historicalMembers, err := svc.ListIncidentMemberChecks(ctx, historical.UID)
	r.NoError(err)
	r.Len(historicalMembers, 1)
	r.True(historicalMembers[0].CurrentlyFailing,
		"the member-flag clear is scoped to the incidents being closed, not applied fleet-wide")

	// The per-check incident is still live.
	stillActive, err := svc.GetIncident(ctx, org.UID, perCheck.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateActive, stillActive.State,
		"a per-check incident must survive the migration untouched")
	r.Nil(stillActive.ResolvedAt)
}

// TestPerCheckIncidentsMigrationLeavesNoActiveGroupIncident is the "no row is
// permanently stuck" assertion, stated as the property rather than as a list
// of the rows this test happened to seed: after the migration, the query the
// dashboard runs for live group outages must come back empty.
func TestPerCheckIncidentsMigrationLeavesNoActiveGroupIncident(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	org := models.NewOrganization("acme", "Acme")
	r.NoError(svc.CreateOrganization(ctx, org))

	base := time.Date(2026, 8, 23, 23, 0, 0, 0, time.UTC)

	// One active group incident per group — uq_active_group_incident never
	// allowed more than that, which is also why "stuck" is a per-group problem.
	for idx := range 5 {
		suffix := string(rune('a' + idx))

		group := models.NewCheckGroup(org.UID, "Cluster "+suffix, "cluster-"+suffix)
		r.NoError(svc.CreateCheckGroup(ctx, group))

		check := models.NewCheck(org.UID, "rabbitmq-"+suffix, "tcp")
		check.CheckGroupUID = &group.UID
		r.NoError(svc.CreateCheck(ctx, check))

		incident := models.NewIncident(org.UID, check.UID, base.Add(time.Duration(idx)*time.Minute), "RabbitMQ down")
		incident.CheckGroupUID = &group.UID
		r.NoError(svc.CreateIncident(ctx, incident))

		r.NoError(svc.UpsertIncidentMemberCheck(ctx, &models.IncidentMemberCheck{
			IncidentUID: incident.UID, CheckUID: check.UID,
			JoinedAt: base, FirstFailureAt: base, LastFailureAt: base,
			FailureCount: 1, CurrentlyFailing: true,
		}))
	}

	execMigrationSection(ctx, t, svc, migrationSection(t, "per-check-incidents"))

	var stuck int

	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from incidents where check_group_uid is not null and state = 1 and deleted_at is null`,
	).Scan(&stuck))
	r.Zero(stuck, "no group incident may be left active with nothing able to close it")

	var flagged int

	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from incident_member_checks m
		 join incidents i on i.uid = m.incident_uid
		 where m.currently_failing = 1 and i.state = 2`,
	).Scan(&flagged))
	r.Zero(flagged, "no member row may point at a closed incident as still failing")
}
