package incidents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// newTotalTestService spins up an in-memory SQLite db.Service plus the
// incidents.Service wired the same way newListBySlugRouter's underlying
// service is, without the HTTP router layer (these tests call svc.ListIncidents
// directly, mirroring TestListIncidents_ByCheckSlug_Postgres's pattern).
func newTotalTestService(t *testing.T) (*sqlite.Service, *incidents.Service, *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("incidents-total-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	return dbSvc, svc, org
}

// newActiveIncidentOnNewCheck creates a fresh check plus an active incident
// on it. Incidents don't share a DB-level "one active incident per check"
// constraint outside application logic, but using a dedicated check per
// incident keeps the fixture obviously realistic and avoids relying on that.
func newActiveIncidentOnNewCheck(
	t *testing.T, dbSvc *sqlite.Service, org *models.Organization, slug string,
) *models.Incident {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	check := models.NewCheck(org.UID, slug, "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-5*time.Minute), slug+" is down")
	r.NoError(dbSvc.CreateIncident(ctx, inc))

	return inc
}

// TestListIncidents_Total_ProvesPageSizeTruncation is the proving test for
// spec 2026-08-02-10: seed more active incidents than the requested page
// size and assert both the truncated page AND the untruncated total. A test
// with fewer incidents than the page size would pass both before and after
// the fix, so it wouldn't actually prove anything — this one seeds 7 active
// incidents with limit=5, which only passes once `pagination.total` is wired
// up from the DB-layer COUNT (previously always zero-value).
func TestListIncidents_Total_ProvesPageSizeTruncation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dbSvc, svc, org := newTotalTestService(t)

	const activeCount = 7
	for i := range activeCount {
		newActiveIncidentOnNewCheck(t, dbSvc, org, "check-"+string(rune('a'+i)))
	}

	resp, err := svc.ListIncidents(t.Context(), org.Slug, &incidents.ListIncidentsOptions{
		States: []string{"active"},
		Size:   5,
	})
	r.NoError(err)
	r.Len(resp.Data, 5, "page must still be capped at the requested size")
	r.EqualValues(activeCount, resp.Pagination.Total,
		"total must reflect every matching incident, not just the returned page")
}

// TestListIncidents_Total_ReflectsFilteredSet is the positive control on
// filtering: total must reflect the *filtered* set (active, not suppressed),
// not the org-wide incident count. A COUNT that ignored the filter would
// otherwise silently pass the truncation test above too.
func TestListIncidents_Total_ReflectsFilteredSet(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, svc, org := newTotalTestService(t)

	// 2 active, unsuppressed incidents — these are what the filtered query
	// below should count.
	newActiveIncidentOnNewCheck(t, dbSvc, org, "active-visible-1")
	newActiveIncidentOnNewCheck(t, dbSvc, org, "active-visible-2")

	// 1 active but paging-suppressed incident — excluded by hideSuppressed.
	suppressed := newActiveIncidentOnNewCheck(t, dbSvc, org, "active-suppressed")
	suppressedTrue := true
	r.NoError(dbSvc.UpdateIncident(ctx, suppressed.UID, &models.IncidentUpdate{
		PagingSuppressed: &suppressedTrue,
	}))

	// 2 resolved incidents — excluded by state=active.
	for _, slug := range []string{"resolved-1", "resolved-2"} {
		resolvedCheck := models.NewCheck(org.UID, slug, "http")
		r.NoError(dbSvc.CreateCheck(ctx, resolvedCheck))
		inc := models.NewIncident(org.UID, resolvedCheck.UID, time.Now().Add(-time.Hour), slug)
		r.NoError(dbSvc.CreateIncident(ctx, inc))
		resolvedState := models.IncidentStateResolved
		resolvedAt := time.Now()
		r.NoError(dbSvc.UpdateIncident(ctx, inc.UID, &models.IncidentUpdate{
			State:      &resolvedState,
			ResolvedAt: &resolvedAt,
		}))
	}

	// Org-wide total is 5 (2 + 1 + 2); the filtered total must be 2.
	resp, err := svc.ListIncidents(ctx, org.Slug, &incidents.ListIncidentsOptions{
		States:         []string{"active"},
		HideSuppressed: true,
		Size:           100,
	})
	r.NoError(err)
	r.Len(resp.Data, 2)
	r.EqualValues(2, resp.Pagination.Total,
		"total must reflect the filtered (active, non-suppressed) set, not the org-wide incident count")
}
