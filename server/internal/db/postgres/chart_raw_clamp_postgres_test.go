package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// Port distinct from every other _postgres_test.go file's embedded-Postgres
// port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const (
	portRawClampPlan  = 15496
	portRawClampParam = 15497
)

// rawClampMarginHours mirrors uptimebar's unexported rawClampMargin — the lag
// pad the shared raw bound adds past the configured retention. Transcribed
// rather than exported: the tests below assert the bound lands within a
// tolerance of `retention + margin`, so an intentional change to the pad shows
// up here as a failure to reconsider, not as a silently-tracking constant.
const rawClampMarginHours = 2

// seedRawSpanning inserts `count` raw rows for a check at the given cadence,
// walking backwards from now — so the fixture deliberately contains raw OLDER
// than the retention band. Production never does (the aggregation job deletes a
// bucket's raw rows in the same transaction that writes the bucket), which is
// precisely why the fixture must: a clamp that excluded nothing would make
// every "clamped window returns the same rows" assertion below vacuous.
func seedRawSpanning(
	ctx context.Context, t *testing.T, s *Service, orgUID, checkUID string, count int, cadence time.Duration,
) {
	t.Helper()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO results (organization_uid, check_uid, period_type, period_start, region, status, duration)
		 SELECT ?, ?, 'raw', now() - (i * (? * interval '1 second')), 'eu', 3, 42
		 FROM generate_series(1, ?) AS i`,
		orgUID, checkUID, cadence.Seconds(), count)
	require.NoError(t, err)
}

// countRawBefore reports how many raw rows for a check sit strictly before a
// timestamp — the "the clamp actually excluded something" control.
func countRawBefore(ctx context.Context, t *testing.T, s *Service, checkUID string, before time.Time) int {
	t.Helper()

	var n int

	row := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM results WHERE check_uid = ? AND period_type = 'raw' AND period_start < ?`,
		checkUID, before)
	require.NoError(t, row.Scan(&n))

	return n
}

func resultUIDs(resp *results.ListResultsResponse) []string {
	uids := make([]string, 0, len(resp.Data))
	for _, r := range resp.Data {
		uids = append(uids, r.UID)
	}

	return uids
}

// TestRawTierRequestIsClampedToRetention_Postgres is the server-side half of
// spec 2026-08-22-07 §4. `/results` is public API — dash0, the MCP tools and
// third-party scripts all reach it — so a `periodType=raw` request spanning a
// year is otherwise planned and executed against the largest table in the
// system with nothing to bound it. The endpoint must hold that floor itself,
// report what it bounded, and leave every non-raw request alone.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestRawTierRequestIsClampedToRetention_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portRawClampPlan)

	org := models.NewOrganization("raw-clamp-org", "Raw Clamp Org")
	r.NoError(s.CreateOrganization(ctx, org))

	target := models.NewCheck(org.UID, "raw-clamp-target", "http")
	r.NoError(s.CreateCheck(ctx, target))

	// Five days of raw at a 1-minute cadence — four of them beyond the default
	// 24 h band — plus 45 days of rollups.
	seedRawSpanning(ctx, t, s, org.UID, target.UID, 7_200, time.Minute)
	seedPlanRollups(ctx, t, s, org.UID, target.UID, 45)

	// Noise, for the same reason as the sibling plan test: on a single-check
	// table any plan looks fine.
	for i := range 9 {
		noise := models.NewCheck(org.UID, fmt.Sprintf("raw-clamp-noise-%d", i), "http")
		r.NoError(s.CreateCheck(ctx, noise))
		seedRawSpanning(ctx, t, s, org.UID, noise.UID, 7_200, time.Minute)
		seedPlanRollups(ctx, t, s, org.UID, noise.UID, 45)
	}

	_, err := s.db.ExecContext(ctx, "ANALYZE results")
	r.NoError(err)

	svc := results.NewService(s, nil)
	yearBack := time.Now().UTC().AddDate(-1, 0, 0)
	const pageSize = 1000

	rawOpts := func(startAfter time.Time) *results.ListResultsOptions {
		return &results.ListResultsOptions{
			Checks:           []string{target.UID},
			PeriodTypes:      []string{models.PeriodTypeRaw},
			PeriodStartAfter: &startAfter,
			Size:             pageSize,
			With:             chartWithFields(),
		}
	}

	// --- A year-back raw request is clamped, and answers identically to one
	// the caller clamped itself. ---
	wide, err := svc.ListResults(ctx, org.Slug, rawOpts(yearBack))
	r.NoError(err)
	r.True(wide.Window.Clamped, "a year-back raw request must report that the server bounded it")
	r.NotNil(wide.Window.PeriodStartAfter, "a clamped window must report its effective start")

	bound := *wide.Window.PeriodStartAfter
	expected := time.Now().UTC().Add(-(systemconfig.DefaultRetentionRawHours + rawClampMarginHours) * time.Hour)
	r.WithinDuration(expected, bound, 5*time.Minute,
		"the clamp must land on the raw-retention band, not on an arbitrary window")

	// Positive control #1: rows ARE returned. Every assertion about which rows
	// a clamp keeps is satisfied by an empty answer otherwise.
	r.Len(wide.Data, pageSize, "the clamped window must still return a full page of raw")

	// Positive control #2: the fixture really does hold raw the clamp excluded,
	// so "same rows as an explicitly clamped request" is not trivially true.
	r.Positive(countRawBefore(ctx, t, s, target.UID, bound),
		"the fixture must contain raw older than the clamp, or this test proves nothing")

	// A minute inside the boundary, so the comparison request is stably
	// unclamped — the band's lower edge slides forward with wall-clock time, so
	// re-asking for exactly `bound` would be clamped again a second later.
	narrow, err := svc.ListResults(ctx, org.Slug, rawOpts(bound.Add(time.Minute)))
	r.NoError(err)
	r.False(narrow.Window.Clamped, "a request already inside the band must report clamped:false")
	r.Len(narrow.Data, pageSize)
	r.Equal(resultUIDs(narrow), resultUIDs(wide),
		"clamping must remove work, not results: both windows must answer identically")

	// The clamped statement — rebuilt through the production filter path — must
	// still ride the partial raw index.
	filter := captureChartFilter(ctx, t, s, org.Slug, rawOpts(yearBack))
	r.NotNil(filter.PeriodStartAfter, "the clamp must reach the DB layer")
	r.WithinDuration(bound, *filter.PeriodStartAfter, time.Minute,
		"the window reported to the client must be the window that ran")

	plan := explainListResults(ctx, t, s, filter)
	r.NotContains(plan, "Seq Scan on results",
		"a clamped raw request must not sequentially scan results (plan:\n%s)", plan)
	r.Contains(plan, "results_raw_idx",
		"a clamped raw request must ride the partial raw index (plan:\n%s)", plan)

	// Plan control: the pre-split mixed predicate on the SAME fixture. Without
	// it, a fixture too small to bother the planner would make the assertion
	// above pass vacuously.
	mixedFilter := captureChartFilter(ctx, t, s, org.Slug, &results.ListResultsOptions{
		Checks:           []string{target.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw, models.PeriodTypeHour},
		PeriodStartAfter: &yearBack,
		Size:             pageSize,
		With:             chartWithFields(),
	})
	mixedPlan := explainListResults(ctx, t, s, mixedFilter)
	r.Contains(mixedPlan, "Seq Scan on results",
		"the mixed predicate MUST still seq-scan, or the assertion above proves nothing (plan:\n%s)", mixedPlan)

	// --- The clamp is raw-only. ---
	rollup, err := svc.ListResults(ctx, org.Slug, &results.ListResultsOptions{
		Checks:           []string{target.UID},
		PeriodTypes:      []string{models.PeriodTypeHour, models.PeriodTypeDay},
		PeriodStartAfter: &yearBack,
		Size:             pageSize,
		With:             chartWithFields(),
	})
	r.NoError(err)
	r.False(rollup.Window.Clamped,
		"rollup retention is months, not hours — clamping a rollup request to the raw band would DELETE results")
	r.WithinDuration(yearBack, *rollup.Window.PeriodStartAfter, time.Second,
		"a rollup request's window must be exactly what was asked for")

	r.False(mixedFilter.PeriodStartAfter.After(yearBack),
		"a mixed request also selects rollup rows, so it must not be clamped to the raw band either")
}

// TestRawClampFollowsRetentionParameter_Postgres pins the resolution path, not
// just the arithmetic: the clamp must read the LIVE
// performance.aggregation_retention_raw_hours parameter (the one the server's
// Aggregation settings tab writes), never cfg.Aggregation.RetentionRaw. A
// reader clamping to 24 h while the job keeps 168 h silently drops six days of
// raw that no rollup covers — data loss that looks like an empty chart.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestRawClampFollowsRetentionParameter_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portRawClampParam)

	org := models.NewOrganization("raw-param-org", "Raw Param Org")
	r.NoError(s.CreateOrganization(ctx, org))

	target := models.NewCheck(org.UID, "raw-param-target", "http")
	r.NoError(s.CreateCheck(ctx, target))
	seedRawSpanning(ctx, t, s, org.UID, target.UID, 1_440, 10*time.Minute) // 10 days

	svc := results.NewService(s, nil)
	yearBack := time.Now().UTC().AddDate(-1, 0, 0)

	askRaw := func(startAfter time.Time) *results.ListResultsResponse {
		resp, listErr := svc.ListResults(ctx, org.Slug, &results.ListResultsOptions{
			Checks:           []string{target.UID},
			PeriodTypes:      []string{models.PeriodTypeRaw},
			PeriodStartAfter: &startAfter,
			Size:             10,
			With:             chartWithFields(),
		})
		r.NoError(listErr)

		return resp
	}

	// Baseline: nothing configured, so the documented default applies.
	def := askRaw(yearBack)
	r.True(def.Window.Clamped)
	defaultBound := *def.Window.PeriodStartAfter

	// Now configure a non-default retention through the very parameter the
	// settings tab writes.
	const configuredHours = 72

	r.NoError(s.SetSystemParameter(ctx, string(systemconfig.KeyPerfAggRetentionRawHours), configuredHours, false))

	configured := askRaw(yearBack)
	r.True(configured.Window.Clamped)

	configuredBound := *configured.Window.PeriodStartAfter
	r.WithinDuration(
		time.Now().UTC().Add(-(configuredHours+rawClampMarginHours)*time.Hour), configuredBound, 5*time.Minute,
		"the clamp must follow performance.aggregation_retention_raw_hours")
	r.True(configuredBound.Before(defaultBound.Add(-time.Hour)),
		"the configured bound must actually differ from the default one, or the parameter was never read")

	// Positive control: a request INSIDE the boundary is untouched and says so.
	inside := time.Now().UTC().Add(-time.Hour)
	near := askRaw(inside)
	r.False(near.Window.Clamped, "a request inside the retention band must report clamped:false")
	r.WithinDuration(inside, *near.Window.PeriodStartAfter, time.Second,
		"an unclamped window must echo the requested start unchanged")
	r.NotEmpty(near.Data, "the control must actually return rows, or clamped:false proves nothing")
}
