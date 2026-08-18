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
		Embedded: true,
		Port:     portLastResult,
		RunMode:  runModeTest,
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

// TestGetLastResultForChecks_Parity_Postgres is the one-for-one twin of the
// SQLite parity test: the CROSS JOIN LATERAL rewrite (spec 2026-08-09-07) must
// return exactly what the former DISTINCT ON returned, on both dialects.
// Aggregated rollup rows are never returned even when newer than every raw
// row, checks with no raw history are absent from the map, a repeated uid
// yields one entry, and an unknown uid is simply absent.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestGetLastResultForChecks_Parity_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portLastResult + 2,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("parity-pg-org", "Parity PG Org")
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

// TestGetLastResultForChecks_ExcludesCreatedMarkerOnly_Postgres is the
// Postgres twin of the SQLite regression test for spec 2026-08-18-03
// (Proposal Part 2): a raw row still in ResultStatusCreated must never win
// "last checked" over an older terminal row, and a check whose only raw row
// IS such a marker must read as having no last result at all.
// ResultStatusRunning is a different story — heartbeat checks use it as a
// legitimate long-lived status the API must still surface.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestGetLastResultForChecks_ExcludesCreatedMarkerOnly_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portLastResult + 3,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("marker-pg-org", "Lifecycle Marker PG Org")
	r.NoError(s.CreateOrganization(ctx, org))

	stale := models.NewCheck(org.UID, "stale-marker-only", "http")
	withHistory := models.NewCheck(org.UID, "with-history-then-marker", "http")
	r.NoError(s.CreateCheck(ctx, stale))
	r.NoError(s.CreateCheck(ctx, withHistory))

	// `stale` keeps only CreateCheck's own "Check created" marker.
	results, err := s.GetLastResultForChecks(ctx, org.UID, []string{stale.UID})
	r.NoError(err)
	r.NotContains(results, stale.UID,
		"a check whose only raw row is the created marker must read as having no last result")

	terminal := models.NewResult(org.UID, withHistory.UID, models.ResultStatusUp, 42)
	terminal.PeriodStart = time.Now().Add(time.Hour)
	r.NoError(s.CreateResult(ctx, terminal))

	newerCreated := models.NewResult(org.UID, withHistory.UID, models.ResultStatusCreated, 0)
	newerCreated.PeriodStart = terminal.PeriodStart.Add(time.Minute)
	r.NoError(s.CreateResult(ctx, newerCreated))

	results, err = s.GetLastResultForChecks(ctx, org.UID, []string{withHistory.UID})
	r.NoError(err)
	r.Contains(results, withHistory.UID)
	r.Equal(terminal.UID, results[withHistory.UID].UID,
		"a newer created row must never win over an older terminal row")

	// A newer RUNNING row, in contrast, must win: heartbeat checks rely on
	// this to surface "the monitored job is currently running" as the check's
	// last result.
	newerRunning := models.NewResult(org.UID, withHistory.UID, models.ResultStatusRunning, 0)
	newerRunning.PeriodStart = newerCreated.PeriodStart.Add(time.Minute)
	r.NoError(s.CreateResult(ctx, newerRunning))

	results, err = s.GetLastResultForChecks(ctx, org.UID, []string{withHistory.UID})
	r.NoError(err)
	r.Contains(results, withHistory.UID)
	r.Equal(newerRunning.UID, results[withHistory.UID].UID,
		"a running row is a legitimate heartbeat report and must win as the newest row")

	// A reaped/abandoned row is deliberately NOT excluded: once terminal it is
	// a legitimate last-checked entry.
	abandoned := models.NewResult(org.UID, withHistory.UID, models.ResultStatusError, 0)
	abandoned.PeriodStart = newerRunning.PeriodStart.Add(time.Minute)
	abandoned.Abandoned = true
	r.NoError(s.CreateResult(ctx, abandoned))

	results, err = s.GetLastResultForChecks(ctx, org.UID, []string{withHistory.UID})
	r.NoError(err)
	r.Contains(results, withHistory.UID)
	r.Equal(abandoned.UID, results[withHistory.UID].UID,
		"a reaped/abandoned row is terminal and must still be eligible as the last result")
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
		Embedded: true,
		Port:     portLastResult + 1,
		RunMode:  runModeTest,
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
