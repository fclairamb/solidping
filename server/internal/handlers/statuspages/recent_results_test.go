package statuspages

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// --- Spec 2026-08-22-05: the response-time fetch is per check and per tier ---
//
// These tests pin WHAT comes back. The query PLAN — the actual defect — is
// pinned separately and in both dialects
// (db/postgres/recent_results_plan_postgres_test.go,
// db/sqlite/recent_results_plan_test.go), because no assertion on returned rows
// can tell an index seek from a sequential scan.

// seedRawSeries writes `count` raw rows for a check at the given cadence,
// newest at `now`, in one region.
func seedRawSeries(
	ctx context.Context, t *testing.T, svc *Service, orgUID, checkUID, region string,
	now time.Time, cadence time.Duration, count int,
) {
	t.Helper()

	for i := range count {
		row := models.NewResult(orgUID, checkUID, models.ResultStatusUp, float32(40+i%9))
		row.PeriodStart = now.Add(-time.Duration(i) * cadence)
		row.Region = &region
		require.NoError(t, svc.db.CreateResult(ctx, row))
	}
}

// seedHourRollups writes `count` hour rollups for a check, newest at the hour
// containing `now`, in one region. duration_p95 is what the response-time chart
// reads off an aggregated row.
func seedHourRollups(
	ctx context.Context, t *testing.T, svc *Service, orgUID, checkUID, region string,
	now time.Time, count int,
) {
	t.Helper()

	for i := range count {
		row := models.NewResult(orgUID, checkUID, models.ResultStatusUp, 0)
		row.PeriodType = models.PeriodTypeHour
		row.PeriodStart = now.Truncate(time.Hour).Add(-time.Duration(i) * time.Hour)
		row.Region = &region

		total, ok := 60, 60
		p95 := float32(50 + i%9)
		row.TotalChecks = &total
		row.SuccessfulChecks = &ok
		row.DurationP95 = &p95
		row.Duration = nil

		require.NoError(t, svc.db.CreateResult(ctx, row))
	}
}

// pageWithChecks builds a public page with one single-check resource per check.
func pageWithChecks(
	ctx context.Context, t *testing.T, svc *Service, org *models.Organization, checks []*models.Check,
) StatusPageResponse {
	t.Helper()

	r := require.New(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, HistoryPeriod: strPtr("7d"),
	})
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	for _, check := range checks {
		_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID,
			CreateResourceRequest{CheckUID: check.UID})
		r.NoError(err)
	}

	return page
}

// legacyFetchRecentResults reproduces EXACTLY what fetchRecentResults did
// before this spec: every requested check, any period type, no time bound, one
// GLOBAL row limit of responseTimeLimit x regionFanoutCap x len(checkUIDs),
// then keep the first responseTimeLimit rows per (check, region) in arrival
// order.
//
// It exists so the parity test can compare against the real previous behaviour
// rather than against a hand-written expectation of it.
func legacyFetchRecentResults(
	ctx context.Context, t *testing.T, svc *Service, orgUID string, checkUIDs []string,
) map[string]map[string][]*models.Result {
	t.Helper()

	resp, err := svc.db.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       checkUIDs,
		Limit:           responseTimeLimit * regionFanoutCap * len(checkUIDs),
		SkipBlobs:       true,
	})
	require.NoError(t, err)

	recentByCheck := make(map[string]map[string][]*models.Result)

	for _, result := range resp.Results {
		regionKey := ""
		if result.Region != nil {
			regionKey = *result.Region
		}

		byRegion := recentByCheck[result.CheckUID]
		if byRegion == nil {
			byRegion = make(map[string][]*models.Result)
			recentByCheck[result.CheckUID] = byRegion
		}

		if len(byRegion[regionKey]) < responseTimeLimit {
			byRegion[regionKey] = append(byRegion[regionKey], result)
		}
	}

	return recentByCheck
}

// uidsAt renders a (check, region) series as its ordered uid sequence, which is
// what "same points, same order" means concretely.
func uidsAt(byRegion map[string][]*models.Result, region string) []string {
	rows := byRegion[region]
	uids := make([]string, len(rows))

	for i, row := range rows {
		uids[i] = row.UID
	}

	return uids
}

// TestFetchRecentResults_ParityWithGlobalLimitFetch is the point-parity gate:
// on a page mixing a 1-minute check (raw only) and a 1-hour check (whose 100
// points reach past raw retention and exist only as hour rollups), the new
// per-check, per-tier fetch must return the SAME regions, the SAME point
// counts and the SAME ordering as the global-limit fetch it replaces.
//
// The 1-hour check is the load-bearing half of the fixture: it is the case that
// silently empties if the rollup branch is dropped, and the reason the pre-fix
// query filtered no period type at all.
func TestFetchRecentResults_ParityWithGlobalLimitFetch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	now := time.Now().UTC().Truncate(time.Minute)

	fast := models.NewCheck(org.UID, "fast", "http")
	fast.Regions = []string{"eu2"}
	r.NoError(svc.db.CreateCheck(ctx, fast))

	slow := models.NewCheck(org.UID, "slow", "http")
	slow.Regions = []string{"eu2"}
	r.NoError(svc.db.CreateCheck(ctx, slow))

	// The 1-minute check: 150 raw rows, well inside the 24 h raw retention.
	seedRawSeries(ctx, t, svc, org.UID, fast.UID, "eu2", now, time.Minute, 150)

	// The 1-hour check: only the last 20 h exist as raw (retention), the rest —
	// 130 more hours — only as hour rollups. Exactly the shape the spec
	// describes.
	seedRawSeries(ctx, t, svc, org.UID, slow.UID, "eu2", now, time.Hour, 20)
	seedHourRollups(ctx, t, svc, org.UID, slow.UID, "eu2", now.Add(-20*time.Hour), 130)

	checkUIDs := []string{fast.UID, slow.UID}

	want := legacyFetchRecentResults(ctx, t, svc, org.UID, checkUIDs)
	got := svc.fetchRecentResults(ctx, org.UID, checkUIDs, true, uptimebar.Hints{})

	r.Len(got, len(want), "same set of checks")

	for _, checkUID := range checkUIDs {
		r.ElementsMatch(regionKeys(want[checkUID]), regionKeys(got[checkUID]),
			"same regions for check %s", checkUID)

		for region := range want[checkUID] {
			r.Equal(uidsAt(want[checkUID], region), uidsAt(got[checkUID], region),
				"check %s region %q must return the same points in the same order", checkUID, region)
		}
	}

	// The fixture must actually exercise the rollup branch, or this proves
	// nothing about the 1-hour check.
	r.Equal(responseTimeLimit, len(got[slow.UID]["eu2"]),
		"the 1-hour check must fill its full budget")

	rollups := 0

	for _, row := range got[slow.UID]["eu2"] {
		if row.PeriodType != models.PeriodTypeRaw {
			rollups++
		}
	}

	r.Positive(rollups, "the 1-hour check's tail must come from hour rollups, not raw")
	r.Equal(models.PeriodTypeRaw, got[slow.UID]["eu2"][0].PeriodType,
		"raw wins the recent end; rollups fill the older tail")
}

func regionKeys(byRegion map[string][]*models.Result) []string {
	keys := make([]string, 0, len(byRegion))
	for key := range byRegion {
		keys = append(keys, key)
	}

	return keys
}

// TestFetchRecentResults_DenseCheckCannotStarveSparseOne pins per-check
// isolation. Under the old GLOBAL limit, one very dense check could consume the
// whole row budget ordered by period_start and leave a sparser neighbour with
// nothing — the reason the old limit had to be so absurdly wide in the first
// place. The per-check budget makes that structurally impossible: the test
// drives the budget down to a value the old shape provably fails at, and shows
// the new shape still fills both checks.
func TestFetchRecentResults_DenseCheckCannotStarveSparseOne(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	now := time.Now().UTC().Truncate(time.Second)

	dense := models.NewCheck(org.UID, "dense", "http")
	r.NoError(svc.db.CreateCheck(ctx, dense))

	sparse := models.NewCheck(org.UID, "sparse", "http")
	r.NoError(svc.db.CreateCheck(ctx, sparse))

	// The dense check probes every 10 s and covers the last ~5 h; the sparse
	// one probes every 10 min and its newest row is already 6 h old. Ordered
	// globally by period_start, every one of the dense check's rows sorts ahead
	// of every one of the sparse check's.
	seedRawSeries(ctx, t, svc, org.UID, dense.UID, "eu2", now, 10*time.Second, 1_800)
	seedRawSeries(ctx, t, svc, org.UID, sparse.UID, "eu2", now.Add(-6*time.Hour), 10*time.Minute, 120)

	checkUIDs := []string{dense.UID, sparse.UID}

	got := svc.fetchRecentResults(ctx, org.UID, checkUIDs, true, uptimebar.Hints{})

	r.Equal(responseTimeLimit, len(got[dense.UID]["eu2"]), "the dense check fills its budget")
	r.Equal(responseTimeLimit, len(got[sparse.UID]["eu2"]),
		"the sparse check must ALSO fill its budget — a per-check budget cannot be starved")

	// Positive control: the same data through a global limit sized to the same
	// total row count the per-check shape reads. The sparse check gets nothing.
	starved, err := svc.db.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       checkUIDs,
		Limit:           2 * responseTimeLimit,
		SkipBlobs:       true,
	})
	r.NoError(err)

	sparseRows := 0

	for _, row := range starved.Results {
		// The check-creation lifecycle marker is written at creation time, so
		// it sorts with the dense check's newest rows and survives any global
		// limit. It carries no duration and renders no point; only real probes
		// count as "not starved".
		if row.CheckUID == sparse.UID && row.Status != nil &&
			models.ResultStatus(*row.Status) != models.ResultStatusCreated {
			sparseRows++
		}
	}

	r.Zero(sparseRows,
		"a GLOBAL limit of the same size returns not one real probe for the sparse check — "+
			"that is the starvation the per-check budget removes")
}

// TestFetchRecentResults_RawBoundFollowsLiveRetention pins spec 2026-08-22-05
// §3: the raw branch's lower bound comes from the LIVE retention the caller
// resolved through systemconfig, not from cfg.Aggregation.RetentionRaw.
//
// The Aggregation settings tab writes performance.* DB parameters that never
// reach the koanf struct. A reader clamping to the documented 24 h default
// while the job is actually configured to keep 168 h would silently drop six
// days of raw rows that no rollup covers yet — invisible, because the chart
// would simply be shorter.
func TestFetchRecentResults_RawBoundFollowsLiveRetention(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	now := time.Now().UTC().Truncate(time.Minute)

	check := models.NewCheck(org.UID, "slow-raw", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))

	// Raw at a 1-hour cadence reaching ~100 h back — far past the 24 h default,
	// well inside a 168 h setting, and with NO rollup covering it. The extra
	// 30 min offset keeps every row a clear half hour away from the 26 h clamp,
	// so the expected count below does not depend on how long the test took to
	// get here.
	seedRawSeries(ctx, t, svc, org.UID, check.UID, "eu2",
		now.Add(-30*time.Minute), time.Hour, 100)

	checkUIDs := []string{check.UID}

	// Control: with the documented default the clamp cuts the series at
	// 24 h + the 2 h aggregation-lag margin.
	atDefault := svc.fetchRecentResults(ctx, org.UID, checkUIDs, true,
		svc.uptimebarHints(ctx, org.UID))
	r.Len(atDefault[check.UID]["eu2"], 26,
		"the default 24 h retention (+2 h margin) must clamp the raw branch: rows at "+
			"0.5 h, 1.5 h ... 25.5 h survive, everything older is cut")

	// Now configure the live retention the way the settings tab would.
	r.NoError(svc.db.SetSystemParameter(ctx,
		string(systemconfig.KeyPerfAggRetentionRawHours), 168, false))

	atLive := svc.fetchRecentResults(ctx, org.UID, checkUIDs, true,
		svc.uptimebarHints(ctx, org.UID))
	r.Len(atLive[check.UID]["eu2"], responseTimeLimit,
		"with performance.aggregation_retention_raw_hours=168 the whole series must survive — "+
			"a koanf-only reader would still be clamping at 24 h")
}

// TestFetchRecentResults_DisabledChartIssuesNothing pins the early return: a
// page with the response-time chart switched off must not pay for the fetch at
// all.
func TestFetchRecentResults_DisabledChartIssuesNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))
	seedRawSeries(ctx, t, svc, org.UID, check.UID, "eu2", time.Now().UTC(), time.Minute, 10)

	r.Empty(svc.fetchRecentResults(ctx, org.UID, []string{check.UID}, false, uptimebar.Hints{}))
	r.Empty(svc.fetchRecentResults(ctx, org.UID, nil, true, uptimebar.Hints{}))
}

// TestResourceRecentResults_GroupsGetNoSeries pins the rule the rewrite feeds:
// a group resource, and any resource resolving to more than one member, gets NO
// response-time series. Interleaving several members' p95 into one chart is
// meaningless, and it would publish per-member latency — the one thing a group
// component exists to hide (spec 2026-08-01-03).
func TestResourceRecentResults_GroupsGetNoSeries(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	recentByCheck := map[string]map[string][]*models.Result{
		"member-1": {"eu2": {{UID: "row-1", CheckUID: "member-1"}}},
		"member-2": {"eu2": {{UID: "row-2", CheckUID: "member-2"}}},
	}

	groupUID := "group-1"

	r.Nil(resourceRecentResults(recentByCheck,
		&StatusPageResourceResponse{CheckGroupUID: &groupUID}, []string{"member-1"}),
		"a group resource never gets a series, even with a single member")

	r.Nil(resourceRecentResults(recentByCheck,
		&StatusPageResourceResponse{}, []string{"member-1", "member-2"}),
		"a multi-member resource never gets a series")

	r.NotNil(resourceRecentResults(recentByCheck,
		&StatusPageResourceResponse{}, []string{"member-1"}),
		"positive control: a single-check resource DOES get its series")
}
