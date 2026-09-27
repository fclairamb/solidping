package incidents_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// portListByKindPG is distinct from every other embedded-Postgres port
// claimed in the repo.
const portListByKindPG = 15475

// TestListIncidents_KindFilter_Postgres is the Postgres half of
// TestListIncidents_KindFilter: it proves ListIncidentsOptions.Kinds reaches
// the real `kind IN (?)` filter and narrows the list. The unfiltered call
// returning all three incidents is the positive control. Self-skips under
// `-short`.
func TestListIncidents_KindFilter_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portListByKindPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	f := seedIncidentsOfEveryKind(t, dbSvc, "list-by-kind-pg-org")

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	list := func(kinds ...string) []string {
		resp, listErr := svc.ListIncidents(ctx, f.org.Slug, &incidents.ListIncidentsOptions{Size: 100, Kinds: kinds})
		r.NoError(listErr)

		uids := make([]string, 0, len(resp.Data))
		for _, inc := range resp.Data {
			uids = append(uids, inc.UID)
		}
		sort.Strings(uids)

		return uids
	}

	r.Equal(sortedUIDs(
		f.byKind[models.IncidentKindCheck],
		f.byKind[models.IncidentKindDegraded],
		f.byKind[models.IncidentKindSLOBurn],
	), list(), "positive control: no kind filter returns every kind")
	r.Equal([]string{f.byKind[models.IncidentKindDegraded]}, list(models.IncidentKindDegraded))
	r.Equal(
		sortedUIDs(f.byKind[models.IncidentKindCheck], f.byKind[models.IncidentKindSLOBurn]),
		list(models.IncidentKindCheck, models.IncidentKindSLOBurn),
	)
}
