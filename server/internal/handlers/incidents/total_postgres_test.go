package incidents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// portIncidentsTotalPG is distinct from every other embedded-Postgres port
// claimed in the repo (5435, 15434, 15436-15448, 15451-15459, 15461, 15463,
// 15465-15467) to avoid colliding if the full (non -short) suite ever runs
// them concurrently.
const portIncidentsTotalPG = 15460

// TestListIncidents_Total_Postgres is the Postgres-backed sibling of
// TestListIncidents_Total_ProvesPageSizeTruncation / _ReflectsFilteredSet
// (spec 2026-08-02-10), mirroring how list_by_slug_test.go /
// list_by_slug_postgres_test.go split SQLite vs real-Postgres coverage. The
// COUNT query built by applyIncidentsFilter uses the exact same bun query
// builder calls on both backends, but running it against real Postgres
// pins the contract against the actual production database engine rather
// than only SQLite's more lenient type/query handling.
//
// Self-skips under `-short` (the default `make test` / CI mode) and on any
// embedded-startup error, mirroring TestListIncidents_ByCheckSlug_Postgres.
func TestListIncidents_Total_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portIncidentsTotalPG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("incidents-total-pg", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	// Seed 7 active incidents on 7 distinct checks, plus 1 resolved incident
	// that must not be counted.
	const activeCount = 7
	for i := range activeCount {
		check := models.NewCheck(org.UID, "pg-check-active-"+string(rune('a'+i)), "http")
		r.NoError(dbSvc.CreateCheck(ctx, check))
		inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-5*time.Minute), "down")
		r.NoError(dbSvc.CreateIncident(ctx, inc))
	}

	resolvedCheck := models.NewCheck(org.UID, "pg-check-resolved", "http")
	r.NoError(dbSvc.CreateCheck(ctx, resolvedCheck))
	resolvedInc := models.NewIncident(org.UID, resolvedCheck.UID, time.Now().Add(-time.Hour), "was down")
	r.NoError(dbSvc.CreateIncident(ctx, resolvedInc))
	resolvedState := models.IncidentStateResolved
	resolvedAt := time.Now()
	r.NoError(dbSvc.UpdateIncident(ctx, resolvedInc.UID, &models.IncidentUpdate{
		State:      &resolvedState,
		ResolvedAt: &resolvedAt,
	}))

	// Proving assertion: page caps at 5, total reflects all 7 active
	// incidents — not the returned page, and not the org-wide 8.
	resp, err := svc.ListIncidents(ctx, org.Slug, &incidents.ListIncidentsOptions{
		States: []string{"active"},
		Size:   5,
	})
	r.NoError(err)
	r.Len(resp.Data, 5)
	r.EqualValues(activeCount, resp.Pagination.Total,
		"total must reflect the filtered active set on Postgres too, not the returned page or org-wide count")
}
