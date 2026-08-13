package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portIncidentNumberMigration is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portIncidentNumberMigration = 15471

// TestIncidentNumberBackfillOrdersByStartedAt_Postgres is the Postgres twin of
// the SQLite backfill test. The two dialects express the same rewrite
// completely differently — a window function plus UPDATE…FROM here, a
// correlated COUNT there — so each needs its own proof; and Postgres actually
// enforces the unique index the backfill must not violate.
//
// Self-skips under `-short` (the default `make test` / CI mode), mirroring the
// other embedded-Postgres migration tests.
func TestIncidentNumberBackfillOrdersByStartedAt_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{
		Embedded: true,
		Port:     portIncidentNumberMigration,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	// The backfill block is read straight out of the shipped migration rather
	// than retyped, so the test cannot drift from the SQL that actually runs.
	migration, err := migrationsFS.ReadFile("migrations/012_incident_number.up.sql")
	r.NoError(err)

	backfill := backfillBlock(t, string(migration))

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

	// Deliberately inserted OUT of chronological order.
	orgA, uidsA := seed("bfpg-a", 5, 1, 3)
	orgB, uidsB := seed("bfpg-b", 2, 0)

	// Rewind to the pre-migration state. The unique index goes first: zeroing
	// every number would otherwise collide on it.
	_, err = svc.db.ExecContext(ctx, `drop index if exists incidents_organization_number_idx`)
	r.NoError(err)
	_, err = svc.db.ExecContext(ctx, `update incidents set number = 0`)
	r.NoError(err)

	_, err = svc.db.ExecContext(ctx, backfill)
	r.NoError(err)

	// Recreated last: a duplicate handed out by the backfill fails LOUDLY here.
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

	r.EqualValues(2, numberOf(orgB, uidsB[0]))
	r.EqualValues(1, numberOf(orgB, uidsB[1]))
}

// backfillBlock extracts the middle `--bun:split` section of the migration —
// the backfill. The ALTER TABLE before it and the CREATE INDEX after it have
// both already been applied by Initialize.
func backfillBlock(t *testing.T, migration string) string {
	t.Helper()

	parts := strings.Split(migration, "--bun:split")
	require.Len(t, parts, 3, "012_incident_number.up.sql must stay three --bun:split blocks")

	return parts[1]
}
