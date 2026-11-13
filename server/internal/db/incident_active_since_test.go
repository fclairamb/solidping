package db_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestListIncidentsActiveSince_SQLite(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "sqlite-active-since-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	svc, err := sqlite.New(t.Context(), sqlite.Config{DataDir: tempDir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(t.Context()))

	testListIncidentsActiveSince(t, svc)
}

func TestListIncidentsActiveSince_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: testsupport.FreePort(t), RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	testListIncidentsActiveSince(t, svc)
}

// testListIncidentsActiveSince pins ActiveSince: open incidents and incidents
// resolved at/after the bound are kept, earlier-resolved ones are not.
func testListIncidentsActiveSince(t *testing.T, svc db.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	org := models.NewOrganization("active-since-org", "")
	r.NoError(svc.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "active-since-check", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	now := time.Now().UTC()
	bound := now.Add(-24 * time.Hour)

	mk := func(started time.Time, resolved *time.Time) string {
		inc := models.NewIncident(org.UID, check.UID, started, "x")
		inc.ResolvedAt = resolved
		r.NoError(svc.CreateIncident(ctx, inc))

		return inc.UID
	}

	before := bound.Add(-time.Hour)
	inside := bound.Add(time.Hour)
	stillOpen := mk(bound.Add(-48*time.Hour), nil)
	resolvedInside := mk(bound.Add(-48*time.Hour), &inside)
	resolvedBefore := mk(bound.Add(-48*time.Hour), &before)

	list, _, err := svc.ListIncidents(ctx, &models.ListIncidentsFilter{
		OrganizationUID: org.UID, ActiveSince: &bound, Limit: 10,
	})
	r.NoError(err)

	got := map[string]bool{}
	for _, inc := range list {
		got[inc.UID] = true
	}

	r.True(got[stillOpen], "open incident is active")
	r.True(got[resolvedInside], "incident resolved after the bound is active")
	r.False(got[resolvedBefore], "incident resolved before the bound is not")
}
