package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

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

// TestGetLastResultForChecks_UsesTheRawIndex asserts the SQLite mirror really
// descends results_raw_idx per check instead of silently regressing to a scan
// of `results` — the whole point of the rewrite (spec 2026-08-09-07), and the
// one thing a row-equality test cannot see. EXPLAIN QUERY PLAN is run on the
// exact SQL production executes (same package constant).
func TestGetLastResultForChecks_UsesTheRawIndex(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("plan-org", "Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "planned", "http")
	r.NoError(s.CreateCheck(ctx, check))
	seedRawResults(t, s, org.UID, check.UID, 40)

	_, err = s.DB().ExecContext(ctx, "ANALYZE")
	r.NoError(err)

	// EXPLAIN QUERY PLAN emits (id, parent, notused, detail) — every column
	// has to be in the scan target for bun to accept the row.
	var plan []struct {
		ID      int    `bun:"id"`
		Parent  int    `bun:"parent"`
		NotUsed int    `bun:"notused"`
		Detail  string `bun:"detail"`
	}
	r.NoError(s.DB().NewRaw(
		"EXPLAIN QUERY PLAN "+lastResultForChecksQuery, org.UID, bun.List([]string{check.UID}),
	).Scan(ctx, &plan))

	var steps string
	for i := range plan {
		steps += plan[i].Detail + "\n"
	}

	r.Contains(steps, "results_raw_idx",
		"the per-check lookup must ride the partial raw index:\n%s", steps)
	r.Contains(steps, "CORRELATED SCALAR SUBQUERY",
		"the winner per check must come from a correlated lookup, not a ranking:\n%s", steps)
	// Every step must be a SEARCH (an index descent). A SCAN step means
	// SQLite materialized and walked a row set — which is exactly what the
	// former ROW_NUMBER() form did ("CO-ROUTINE ranked" + "SCAN ranked"),
	// i.e. reading every raw row of the organization before discarding all
	// but one per check.
	r.NotContains(steps, "SCAN",
		"no step may scan a row set; every step must be an index search:\n%s", steps)
}

// TestGetLastResultForChecks_Parity pins the exact contract the per-check
// index descent (spec 2026-08-09-07) must preserve, field by field, and is
// mirrored one-for-one by its Postgres twin so the two backends cannot drift:
// aggregated rollup rows are never returned even when newer than every raw
// row, checks with no raw history are absent from the map rather than present
// with a nil value, a repeated uid yields one entry, and an unknown uid is
// simply absent.
func TestGetLastResultForChecks_Parity(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("parity-org", "Parity Org")
	r.NoError(s.CreateOrganization(ctx, org))

	withHistory := models.NewCheck(org.UID, "with-history", "http")
	noHistory := models.NewCheck(org.UID, "no-history", "http")
	r.NoError(s.CreateCheck(ctx, withHistory))
	r.NoError(s.CreateCheck(ctx, noHistory))

	newest := seedRawResults(t, s, org.UID, withHistory.UID, 5)

	// A rollup row NEWER than every raw row must still never win.
	rollup := models.NewResult(org.UID, withHistory.UID, models.ResultStatusUp, 999)
	rollup.PeriodType = models.PeriodTypeHour
	rollup.PeriodStart = newest.PeriodStart.Add(time.Hour)
	r.NoError(s.CreateResult(ctx, rollup))

	// noHistory keeps only CreateCheck's initial "created" marker row; drop
	// it so the check genuinely has no raw history.
	_, err = s.DB().NewRaw("DELETE FROM results WHERE check_uid = ?", noHistory.UID).Exec(ctx)
	r.NoError(err)

	results, err := s.GetLastResultForChecks(ctx, org.UID, []string{
		withHistory.UID, noHistory.UID, withHistory.UID, models.NewCheck(org.UID, "x", "http").UID,
	})
	r.NoError(err)
	r.Len(results, 1, "one entry: only the check that has raw history")

	got := results[withHistory.UID]
	r.NotNil(got)
	r.Equal(newest.UID, got.UID)
	r.Equal(withHistory.UID, got.CheckUID)
	r.Equal(models.PeriodTypeRaw, got.PeriodType, "rollup rows must never be returned")
	r.WithinDuration(newest.PeriodStart, got.PeriodStart, time.Second)
	r.NotNil(got.Status)
	r.Equal(int(models.ResultStatusUp), *got.Status)
	r.NotNil(got.Duration)
	r.InDelta(*newest.Duration, *got.Duration, 0.001)

	r.NotContains(results, noHistory.UID, "a check with no raw row is absent, not nil-valued")
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
