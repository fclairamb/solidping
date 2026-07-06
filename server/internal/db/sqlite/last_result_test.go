package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// seedRawResults inserts n raw results for checkUID under orgUID, spaced one
// minute apart, starting one hour in the future relative to check creation
// (CreateCheck itself inserts an initial "created" raw result at time.Now(),
// so seeded rows must postdate that to unambiguously be the newest) and
// increasing in PeriodStart. Returns the newest one — the one every
// regression test expects GetLastResultForChecks to return.
func seedRawResults(
	t *testing.T, s *Service, orgUID, checkUID string, n int,
) *models.Result {
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

// TestGetLastResultForChecks_ReturnsOneRowPerCheck seeds several raw rows
// across two checks and asserts the map returns exactly the newest row per
// check — regression test for spec 2026-07-06-01 Part A: the previous query
// fetched every raw row (O(retention)) and only discarded extras in Go.
func TestGetLastResultForChecks_ReturnsOneRowPerCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("last-result-org", "Last Result Org")
	r.NoError(s.CreateOrganization(ctx, org))

	checkA := models.NewCheck(org.UID, "check-a", "http")
	checkB := models.NewCheck(org.UID, "check-b", "http")
	r.NoError(s.CreateCheck(ctx, checkA))
	r.NoError(s.CreateCheck(ctx, checkB))

	// Many raw rows for A (simulating a high-frequency check with long
	// retention), fewer for B.
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

// TestGetLastResultForChecks_FiltersByOrganization seeds a check in another
// organization with a newer raw row sharing no check_uid overlap, then
// confirms the organization_uid predicate is applied — the query must not
// accidentally return cross-org rows even if check_uid matching alone would
// (it can't, check_uid is unique, but this also exercises the predicate is
// present at all and doesn't silently match everything).
func TestGetLastResultForChecks_FiltersByOrganization(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

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
