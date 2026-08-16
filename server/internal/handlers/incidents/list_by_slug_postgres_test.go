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

// portListBySlugPG is distinct from every other embedded-Postgres port
// claimed in the repo (5435, 15434, 15436-15448, 15451, 15452) to avoid
// colliding if the full (non -short) suite ever runs them concurrently.
const portListBySlugPG = 15453

// TestListIncidents_ByCheckSlug_Postgres is the true regression test for
// issue #127 / spec 2026-07-14-03: the incidents table's `check_uid` column
// is a real Postgres `uuid` (server/internal/db/postgres/migrations,
// `incidents.check_uid uuid not null`), so on SQLite (TEXT-affinity, no type
// enforcement) the pre-fix code never actually 500s — only a real Postgres
// backend reproduces "invalid input syntax for type uuid" when a slug like
// "http-api-acme-io-datalake" is handed straight to a `check_uid IN (?)`
// filter. This test proves the fix (resolving checkUid through
// GetCheckByUidOrSlug before building the filter, mirroring
// results.Service.resolveCheckIdentifiers) against that real column type.
//
// Self-skips under `-short` (the default `make test` / CI mode) and on any
// embedded-startup error, mirroring
// results.TestGetResultNeighbors_Postgres.
func TestListIncidents_ByCheckSlug_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portListBySlugPG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("list-by-slug-pg-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "http-api-acme-io-datalake", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-5*time.Minute), "datalake is down")
	r.NoError(dbSvc.CreateIncident(ctx, inc))

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	// Pre-fix, this call fails with:
	//   failed to list incidents: ERROR: invalid input syntax for type uuid:
	//   "http-api-acme-io-datalake" (SQLSTATE=22P02)
	// because *check.Slug was forwarded straight into `check_uid IN (?)`.
	resp, err := svc.ListIncidents(ctx, org.Slug, &incidents.ListIncidentsOptions{
		CheckUIDs: []string{*check.Slug},
		Size:      100,
	})
	r.NoError(err, "checkUid=<slug> must resolve, not hit the DB as a raw uuid literal")
	r.Len(resp.Data, 1)
	r.Equal(inc.UID, resp.Data[0].UID)
	r.Equal(check.UID, resp.Data[0].CheckUID)
}
