package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portLastResult is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portLastResult = 15446

// seedRawResults inserts n raw results for checkUID under orgUID, spaced one
// minute apart, starting one hour in the future relative to check creation
// (CreateCheck itself inserts an initial "created" raw result at time.Now(),
// so seeded rows must postdate that to unambiguously be the newest) and
// increasing in PeriodStart. Returns the newest one — the one every
// regression test expects GetLastResultForChecks to return.
func seedRawResults(t *testing.T, s *Service, orgUID, checkUID string, n int) *models.Result {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	base := time.Now().Add(time.Hour)

	var newest *models.Result
	for i := 0; i < n; i++ {
		result := models.NewResult(orgUID, checkUID, models.ResultStatusUp, float32(100+i))
		result.PeriodStart = base.Add(time.Duration(i) * time.Minute)
		r.NoError(s.CreateResult(ctx, result))
		newest = result
	}

	return newest
}

// TestGetLastResultForChecks_ReturnsOneRowPerCheck_Postgres is the Postgres
// counterpart to the SQLite regression test for spec 2026-07-06-01 Part A:
// the previous query had no DISTINCT ON despite the comment claiming
// otherwise, and fetched every raw row (O(retention)) before discarding
// extras in a Go loop. Self-skips under -short / on embedded-startup error,
// like every other embedded-postgres test in this package.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its sibling below
func TestGetLastResultForChecks_ReturnsOneRowPerCheck_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded:    true,
		EmbeddedDir: t.TempDir(),
		Port:        portLastResult,
		RunMode:     runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("last-result-pg-org", "Last Result PG Org")
	r.NoError(s.CreateOrganization(ctx, org))

	checkA := models.NewCheck(org.UID, "check-a", "http")
	checkB := models.NewCheck(org.UID, "check-b", "http")
	r.NoError(s.CreateCheck(ctx, checkA))
	r.NoError(s.CreateCheck(ctx, checkB))

	// Many raw rows for A (simulating a high-frequency check with long
	// retention), fewer for B — proves the query returns O(1) rows per
	// check regardless of how many raw rows exist.
	newestA := seedRawResults(t, s, org.UID, checkA.UID, 50)
	newestB := seedRawResults(t, s, org.UID, checkB.UID, 3)

	results, err := s.GetLastResultForChecks(ctx, org.UID, []string{checkA.UID, checkB.UID})
	r.NoError(err)
	r.Len(results, 2, "exactly one row per requested check")

	r.Contains(results, checkA.UID)
	r.Equal(newestA.UID, results[checkA.UID].UID, "must be the newest row for check A")

	r.Contains(results, checkB.UID)
	r.Equal(newestB.UID, results[checkB.UID].UID, "must be the newest row for check B")
}

// TestGetLastResultForChecks_FiltersByOrganization_Postgres confirms the
// organization_uid predicate is actually applied — querying with the wrong
// org UID must return nothing, and the covering index
// (organization_uid, check_uid, period_start desc) requires the predicate
// to be present to be ridden at all.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its sibling above
func TestGetLastResultForChecks_FiltersByOrganization_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded:    true,
		EmbeddedDir: t.TempDir(),
		Port:        portLastResult + 1,
		RunMode:     runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	orgA := models.NewOrganization("org-a", "Org A")
	orgB := models.NewOrganization("org-b", "Org B")
	r.NoError(s.CreateOrganization(ctx, orgA))
	r.NoError(s.CreateOrganization(ctx, orgB))

	checkA := models.NewCheck(orgA.UID, "check-a", "http")
	r.NoError(s.CreateCheck(ctx, checkA))

	newestA := seedRawResults(t, s, orgA.UID, checkA.UID, 5)

	// Querying with orgB's UID for orgA's check must return nothing.
	results, err := s.GetLastResultForChecks(ctx, orgB.UID, []string{checkA.UID})
	r.NoError(err)
	r.Empty(results, "wrong org UID must not see another org's results")

	// Querying with the correct org UID returns it.
	results, err = s.GetLastResultForChecks(ctx, orgA.UID, []string{checkA.UID})
	r.NoError(err)
	r.Contains(results, checkA.UID)
	r.Equal(newestA.UID, results[checkA.UID].UID)
}
