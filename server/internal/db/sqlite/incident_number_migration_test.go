package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// incidentNumberBackfillSQL is the backfill block of
// 012_incident_number.up.sql. The `alter table` that precedes it in the
// migration is NOT re-runnable (SQLite has no ADD COLUMN IF NOT EXISTS), so
// this test inlines just the backfill — exactly like
// owner_backfill_migration_test.go does for 011.
// Keep in sync with migrations/012_incident_number.up.sql.
const incidentNumberBackfillSQL = `
update incidents
set number = (
  select count(*)
  from incidents i2
  where i2.organization_uid = incidents.organization_uid
    and (i2.started_at < incidents.started_at
         or (i2.started_at = incidents.started_at and i2.uid <= incidents.uid))
)
where number = 0;
`

// TestIncidentNumberBackfillOrdersByStartedAt pins the migration's backfill:
// incidents that predate the numbers are numbered per organization, oldest
// first. Ordering matters — `#1` must be the org's FIRST incident, or every
// historical reference in a runbook or a Slack scrollback points somewhere else.
func TestIncidentNumberBackfillOrdersByStartedAt(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	seed := func(slug string, offsets ...int) (string, []string) {
		org := models.NewOrganization(slug, slug)
		r.NoError(svc.CreateOrganization(ctx, org))

		check := models.NewCheck(org.UID, slug+"-check", "http")
		r.NoError(svc.CreateCheck(ctx, check))

		uids := make([]string, 0, len(offsets))

		for _, offset := range offsets {
			incident := models.NewIncident(org.UID, check.UID, base.Add(time.Duration(offset)*time.Hour), slug)
			r.NoError(svc.CreateIncident(ctx, incident))
			uids = append(uids, incident.UID)
		}

		return org.UID, uids
	}

	// Deliberately inserted OUT of chronological order, so a backfill that
	// simply followed insertion order would produce a different answer.
	orgA, uidsA := seed("bf-a", 5, 1, 3)
	orgB, uidsB := seed("bf-b", 2, 0)

	// Rewind to the pre-migration state: the unique index has to go first,
	// because zeroing every number would otherwise collide on it.
	_, err = svc.db.ExecContext(ctx, `drop index if exists incidents_organization_number_idx`)
	r.NoError(err)
	_, err = svc.db.ExecContext(ctx, `update incidents set number = 0`)
	r.NoError(err)

	_, err = svc.db.ExecContext(ctx, incidentNumberBackfillSQL)
	r.NoError(err)

	// The index is recreated last: if the backfill had handed out a duplicate
	// within an org, this is where it would fail.
	_, err = svc.db.ExecContext(ctx,
		`create unique index incidents_organization_number_idx on incidents (organization_uid, number)`)
	r.NoError(err)

	numberOf := func(orgUID, uid string) int64 {
		incident, getErr := svc.GetIncident(ctx, orgUID, uid)
		r.NoError(getErr)

		return incident.Number
	}

	// uidsA is in insertion order (offsets 5, 1, 3) → chronological rank 3, 1, 2.
	r.EqualValues(3, numberOf(orgA, uidsA[0]))
	r.EqualValues(1, numberOf(orgA, uidsA[1]))
	r.EqualValues(2, numberOf(orgA, uidsA[2]))

	// Numbering restarts per organization.
	r.EqualValues(2, numberOf(orgB, uidsB[0]))
	r.EqualValues(1, numberOf(orgB, uidsB[1]))
}

// TestIncidentNumberSkipsSoftDeletedRows proves a soft-deleted incident KEEPS
// its number and the next incident does not inherit it. `#42` has to mean one
// incident forever: a reused number turns a `/ack #42` typed from an old alert
// into an ack of somebody else's outage.
func TestIncidentNumberSkipsSoftDeletedRows(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	org := models.NewOrganization("inc-num-del", "inc-num-del")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "inc-num-del-check", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	first := models.NewIncident(org.UID, check.UID, time.Now(), "one")
	r.NoError(svc.CreateIncident(ctx, first))

	second := models.NewIncident(org.UID, check.UID, time.Now(), "two")
	r.NoError(svc.CreateIncident(ctx, second))
	r.EqualValues(2, second.Number)

	// There is no soft-delete API for incidents, so the column is set directly —
	// which is exactly the state a future retention sweep would leave behind.
	_, err = svc.db.ExecContext(ctx,
		`update incidents set deleted_at = datetime('now') where uid = ?`, second.UID)
	r.NoError(err)

	third := models.NewIncident(org.UID, check.UID, time.Now(), "three")
	r.NoError(svc.CreateIncident(ctx, third))
	r.EqualValues(3, third.Number, "#2 stays taken by the deleted incident")

	// And the deleted incident is no longer reachable by its number.
	_, err = svc.GetIncidentByNumber(ctx, org.UID, 2)
	r.Error(err)
}
