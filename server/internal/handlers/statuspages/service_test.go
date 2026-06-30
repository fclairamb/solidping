package statuspages

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

const testPublicSlug = "public"

func setupStatusPagesTest(t *testing.T) (context.Context, *Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	return ctx, NewService(dbService), org
}

func ptrFloat64(v float64) *float64 { return &v }
func ptrFloat32(v float32) *float32 { return &v }
func ptrInt(v int) *int             { return &v }

// findByDate finds a result with the given UTC date in the slice, or returns nil.
func findByDate(results []*models.Result, dateStr string) *models.Result {
	for _, r := range results {
		if r.PeriodStart.UTC().Format("2006-01-02") == dateStr {
			return r
		}
	}

	return nil
}

const historyDays = 90

func TestSynthesizeMissingDailyBuckets_ReplacesStoredTodayRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	todayStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	checkUID := "check-1"

	staleAvail := 100.0
	staleToday := &models.Result{
		CheckUID:        checkUID,
		PeriodStart:     todayStart,
		AvailabilityPct: &staleAvail,
		TotalChecks:     ptrInt(60),
	}
	yesterdayAvail := 95.0
	yesterday := &models.Result{
		CheckUID:        checkUID,
		PeriodStart:     yesterdayStart,
		AvailabilityPct: &yesterdayAvail,
	}

	resultsByCheck := map[string][]*models.Result{
		checkUID: {staleToday, yesterday},
	}

	hourlyByCheck := map[string][]*models.Result{
		checkUID: {
			{
				CheckUID:        checkUID,
				PeriodStart:     todayStart,
				AvailabilityPct: ptrFloat64(100),
				Duration:        ptrFloat32(50),
				DurationP95:     ptrFloat32(60),
			},
			{
				CheckUID:        checkUID,
				PeriodStart:     todayStart.Add(time.Hour),
				AvailabilityPct: ptrFloat64(50),
				Duration:        ptrFloat32(70),
				DurationP95:     ptrFloat32(80),
			},
		},
	}

	synthesizeMissingDailyBuckets(resultsByCheck, hourlyByCheck, []string{checkUID}, todayStart, historyDays)

	results := resultsByCheck[checkUID]
	r.Len(results, 2, "expected yesterday + synthesized today")

	todayRow := findByDate(results, "2026-05-11")
	r.NotNil(todayRow, "today row should exist")
	r.NotSame(staleToday, todayRow, "today row should be the synthesized one")
	r.NotNil(todayRow.AvailabilityPct)
	r.InDelta(75.0, *todayRow.AvailabilityPct, 0.01, "synthesized avail should average hourly rows")
}

func TestSynthesizeMissingDailyBuckets_FillsMissingPastDayFromHourly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	todayStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	dayMinus2 := todayStart.AddDate(0, 0, -2)
	checkUID := "check-1"

	// No stored daily rows at all (fresh install with no rollups yet).
	resultsByCheck := map[string][]*models.Result{
		checkUID: {},
	}

	// Two hourly rows in the dayMinus2 UTC bucket: 100% and 80% → avg 90%.
	hourlyByCheck := map[string][]*models.Result{
		checkUID: {
			{CheckUID: checkUID, PeriodStart: dayMinus2, AvailabilityPct: ptrFloat64(100)},
			{CheckUID: checkUID, PeriodStart: dayMinus2.Add(time.Hour), AvailabilityPct: ptrFloat64(80)},
		},
	}

	synthesizeMissingDailyBuckets(resultsByCheck, hourlyByCheck, []string{checkUID}, todayStart, historyDays)

	results := resultsByCheck[checkUID]
	r.Len(results, 1, "exactly one synthetic daily row should be created")

	row := findByDate(results, "2026-05-09")
	r.NotNil(row, "row for dayMinus2 should exist")
	r.NotNil(row.AvailabilityPct)
	r.InDelta(90.0, *row.AvailabilityPct, 0.01)
	r.True(row.PeriodStart.Equal(dayMinus2), "PeriodStart should be the UTC day midnight")
}

func TestSynthesizeMissingDailyBuckets_NoHourlyKeepsStored(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	todayStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	checkUID := "check-1"

	staleAvail := 100.0
	storedToday := &models.Result{
		CheckUID:        checkUID,
		PeriodStart:     todayStart,
		AvailabilityPct: &staleAvail,
	}

	resultsByCheck := map[string][]*models.Result{
		checkUID: {storedToday},
	}
	hourlyByCheck := map[string][]*models.Result{}

	synthesizeMissingDailyBuckets(resultsByCheck, hourlyByCheck, []string{checkUID}, todayStart, historyDays)

	r.Len(resultsByCheck[checkUID], 1)
	r.Same(storedToday, resultsByCheck[checkUID][0], "stored today row should be preserved when no hourly synth")
}

func TestSynthesizeMissingDailyBuckets_PastDailyTakesPrecedenceOverHourly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	todayStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	dayMinus3 := todayStart.AddDate(0, 0, -3)
	checkUID := "check-1"

	storedAvail := 88.0
	stored := &models.Result{
		CheckUID:        checkUID,
		PeriodStart:     dayMinus3,
		AvailabilityPct: &storedAvail,
	}

	resultsByCheck := map[string][]*models.Result{
		checkUID: {stored},
	}
	// Hourly data for the same past day — must NOT override the rolled-up daily.
	hourlyByCheck := map[string][]*models.Result{
		checkUID: {
			{CheckUID: checkUID, PeriodStart: dayMinus3, AvailabilityPct: ptrFloat64(50)},
		},
	}

	synthesizeMissingDailyBuckets(resultsByCheck, hourlyByCheck, []string{checkUID}, todayStart, historyDays)

	row := findByDate(resultsByCheck[checkUID], "2026-05-08")
	r.NotNil(row)
	r.Same(stored, row, "stored daily for past day should win over hourly synth")
}

func TestAggregateHourlyToDaily_AveragesHourlyAvail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	periodStart := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)

	hourly := []*models.Result{
		{AvailabilityPct: ptrFloat64(100), Duration: ptrFloat32(10), DurationP95: ptrFloat32(20)},
		{AvailabilityPct: ptrFloat64(80), Duration: ptrFloat32(30), DurationP95: ptrFloat32(40)},
	}

	synth := aggregateHourlyToDaily("check-1", hourly, periodStart)

	r.NotNil(synth)
	r.Equal("check-1", synth.CheckUID)
	r.True(synth.PeriodStart.Equal(periodStart))
	r.NotNil(synth.AvailabilityPct)
	r.InDelta(90.0, *synth.AvailabilityPct, 0.01)
	r.NotNil(synth.Duration)
	r.InDelta(float32(20), *synth.Duration, 0.01)
	r.NotNil(synth.DurationP95)
	r.InDelta(float32(30), *synth.DurationP95, 0.01)
	r.NotNil(synth.TotalChecks)
	r.Equal(2, *synth.TotalChecks)
}

func TestAggregateHourlyToDaily_ReturnsNilOnEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	periodStart := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)

	r.Nil(aggregateHourlyToDaily("check-1", nil, periodStart))
	r.Nil(aggregateHourlyToDaily("check-1", []*models.Result{}, periodStart))
	r.Nil(aggregateHourlyToDaily("check-1", []*models.Result{{}}, periodStart), "no avail in any row → nil")
}

func TestAggregateRawToDaily_DerivesAvailFromStatus(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	periodStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	upStatus := int(models.ResultStatusUp)
	downStatus := int(models.ResultStatusDown)

	// 3 up, 1 down → 75% availability.
	raw := []*models.Result{
		{Status: &upStatus, Duration: ptrFloat32(40)},
		{Status: &upStatus, Duration: ptrFloat32(60)},
		{Status: &upStatus, Duration: ptrFloat32(50)},
		{Status: &downStatus, Duration: ptrFloat32(9999)},
	}

	synth := aggregateRawToDaily("check-1", raw, periodStart)

	r.NotNil(synth)
	r.NotNil(synth.AvailabilityPct)
	r.InDelta(75.0, *synth.AvailabilityPct, 0.01)
	r.NotNil(synth.TotalChecks)
	r.Equal(4, *synth.TotalChecks)
	// Duration averages successful rows only: (40+60+50)/3 = 50.
	r.NotNil(synth.Duration)
	r.InDelta(float32(50), *synth.Duration, 0.01)
}

func TestAggregateRawToDaily_AllDownStillReturnsRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	periodStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	downStatus := int(models.ResultStatusDown)

	raw := []*models.Result{
		{Status: &downStatus},
		{Status: &downStatus},
	}

	synth := aggregateRawToDaily("check-1", raw, periodStart)
	r.NotNil(synth, "an all-down day should still produce a synth (signals an outage, not no-data)")
	r.NotNil(synth.AvailabilityPct)
	r.InDelta(0.0, *synth.AvailabilityPct, 0.01)
	r.Nil(synth.Duration, "no successful rows → no duration")
}

func TestAggregateRawToDaily_ReturnsNilOnEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	periodStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)

	r.Nil(aggregateRawToDaily("check-1", nil, periodStart))
	r.Nil(aggregateRawToDaily("check-1", []*models.Result{}, periodStart))
	r.Nil(aggregateRawToDaily("check-1", []*models.Result{{Status: nil}}, periodStart),
		"rows with nil status are ignored — if all are nil, return nil")
}

// TestAggregateRawToDaily_ExcludesLifecycleRows is the core regression guard for
// spec 2026-06-30-02: lifecycle rows (created/running) are in-flight or
// just-scheduled markers, not measurements, and must be excluded from the
// availability denominator so a healthy check reads 100% — matching the badges
// endpoint and the stored daily rows written by the aggregation job.
func TestAggregateRawToDaily_ExcludesLifecycleRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	periodStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	upStatus := int(models.ResultStatusUp)
	createdStatus := int(models.ResultStatusCreated)
	runningStatus := int(models.ResultStatusRunning)

	// 18 up + 2 created + 1 running → lifecycle rows excluded → 100% over 18.
	raw := make([]*models.Result, 0, 21)
	for i := 0; i < 18; i++ {
		s := upStatus
		raw = append(raw, &models.Result{Status: &s, Duration: ptrFloat32(50)})
	}
	for i := 0; i < 2; i++ {
		s := createdStatus
		raw = append(raw, &models.Result{Status: &s})
	}
	raw = append(raw, &models.Result{Status: &runningStatus})

	synth := aggregateRawToDaily("check-1", raw, periodStart)

	r.NotNil(synth)
	r.NotNil(synth.AvailabilityPct)
	r.InDelta(100.0, *synth.AvailabilityPct, 0.001,
		"a healthy check with only lifecycle rows besides up must read 100%")
	r.NotNil(synth.TotalChecks)
	r.Equal(18, *synth.TotalChecks, "lifecycle rows excluded from the denominator")
	r.NotNil(synth.SuccessfulChecks)
	r.Equal(18, *synth.SuccessfulChecks)
}

// TestAggregateRawToDaily_WarningCountsAsUp pins that a warning row counts as up
// (target reachable, just something to report) — consistent with the aggregation
// job's Decision A — and that its duration is included in the average.
func TestAggregateRawToDaily_WarningCountsAsUp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	periodStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	upStatus := int(models.ResultStatusUp)
	warningStatus := int(models.ResultStatusWarning)

	// 2 up + 1 warning → still 100%; duration averages over all three.
	raw := []*models.Result{
		{Status: &upStatus, Duration: ptrFloat32(40)},
		{Status: &warningStatus, Duration: ptrFloat32(70)},
		{Status: &upStatus, Duration: ptrFloat32(60)},
	}

	synth := aggregateRawToDaily("check-1", raw, periodStart)

	r.NotNil(synth)
	r.NotNil(synth.AvailabilityPct)
	r.InDelta(100.0, *synth.AvailabilityPct, 0.001, "warning counts as up → 100%")
	r.NotNil(synth.TotalChecks)
	r.Equal(3, *synth.TotalChecks)
	// Duration averages up+warning rows: (40+70+60)/3 ≈ 56.67.
	r.NotNil(synth.Duration)
	r.InDelta(float32(56.6667), *synth.Duration, 0.01)
}

// TestAggregateRawToDaily_GenuineDownReflectsMiss confirms that excluding
// lifecycle rows does not mask real downtime: a genuine down still lowers the
// percentage over the countable (non-lifecycle) denominator.
func TestAggregateRawToDaily_GenuineDownReflectsMiss(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	periodStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	upStatus := int(models.ResultStatusUp)
	downStatus := int(models.ResultStatusDown)
	createdStatus := int(models.ResultStatusCreated)

	// 3 up + 1 down + 1 created → created excluded → 3/4 = 75%.
	raw := []*models.Result{
		{Status: &upStatus, Duration: ptrFloat32(50)},
		{Status: &upStatus, Duration: ptrFloat32(50)},
		{Status: &upStatus, Duration: ptrFloat32(50)},
		{Status: &downStatus},
		{Status: &createdStatus},
	}

	synth := aggregateRawToDaily("check-1", raw, periodStart)

	r.NotNil(synth)
	r.NotNil(synth.AvailabilityPct)
	r.InDelta(75.0, *synth.AvailabilityPct, 0.001, "real down still counts against availability")
	r.NotNil(synth.TotalChecks)
	r.Equal(4, *synth.TotalChecks, "lifecycle excluded, down included")
}

// TestBuildAvailabilityData_UTCBucketing pins that daily rows are looked up by
// their UTC date, not the server's local date. Pre-fix the loop iterated days
// in time.Local, so for servers west of UTC the most-recent stored daily row
// missed and rendered as noData. Forcing time.Local to a non-UTC zone for
// this test would race with other parallel tests, so we instead verify the
// equivalent invariant: a daily row stamped at midnight UTC must be findable
// by its UTC date string regardless of how it would render in local time.
func TestBuildAvailabilityData_UTCBucketing(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A daily row at midnight UTC for "yesterday" UTC.
	now := time.Now().UTC()
	todayUTC := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterdayUTC := todayUTC.AddDate(0, 0, -1)
	yesterdayAvail := 99.95

	daily := []*models.Result{
		{
			CheckUID:        "check-1",
			PeriodStart:     yesterdayUTC,
			AvailabilityPct: &yesterdayAvail,
			TotalChecks:     ptrInt(1440),
		},
	}

	out := buildAvailabilityData(daily, nil, 7, true, false)
	r.NotNil(out)
	r.Len(out.DailyAvailability, 7)

	yesterdayStr := yesterdayUTC.Format("2006-01-02")
	var matched *AvailabilityPoint
	for i := range out.DailyAvailability {
		if out.DailyAvailability[i].Date == yesterdayStr {
			matched = &out.DailyAvailability[i]
		}
	}
	r.NotNil(matched, "yesterday UTC bucket should be present in the bar")
	r.Equal("up", matched.Status, "stored daily row should render as up, not noData")
	r.InDelta(99.95, matched.AvailabilityPct, 0.01)
}

// TestCreateResource_AssignsSequentialPositions pins the contract that new
// resources land at max+1, never sharing position=0. The dashboard's swap-based
// reorder UI relies on positions being distinct.
func TestCreateResource_AssignsSequentialPositions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	pageReq := &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug}
	page, err := svc.CreateStatusPage(ctx, org.Slug, pageReq)
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	checks := make([]*models.Check, 3)
	for i := range checks {
		c := models.NewCheck(org.UID, "", "http")
		r.NoError(svc.db.CreateCheck(ctx, c))
		checks[i] = c
	}

	resources := make([]StatusPageResourceResponse, 3)
	for i, c := range checks {
		res, errCreate := svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: c.UID})
		r.NoError(errCreate)
		resources[i] = res
	}

	r.Equal(1, resources[0].Position, "first resource should be position 1")
	r.Equal(2, resources[1].Position, "second resource should be position 2")
	r.Equal(3, resources[2].Position, "third resource should be position 3")
}

// TestCreateResource_SwapPositionsReordersList exercises the exact two-PATCH
// swap the dashboard performs when the user clicks the move-up/down chevrons.
// Pre-fix every resource shared position=0 so the swap was a no-op; this test
// catches a regression to that bug.
func TestCreateResource_SwapPositionsReordersList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	pageReq := &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug}
	page, err := svc.CreateStatusPage(ctx, org.Slug, pageReq)
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	checks := make([]*models.Check, 3)
	resources := make([]StatusPageResourceResponse, 3)
	for i := range checks {
		c := models.NewCheck(org.UID, "", "http")
		r.NoError(svc.db.CreateCheck(ctx, c))
		checks[i] = c

		res, errCreate := svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: c.UID})
		r.NoError(errCreate)
		resources[i] = res
	}

	swapPosA := resources[1].Position
	swapPosB := resources[0].Position
	updateA := UpdateResourceRequest{Position: &swapPosA}
	updateB := UpdateResourceRequest{Position: &swapPosB}
	_, err = svc.UpdateResource(ctx, org.Slug, page.UID, section.UID, resources[0].UID, updateA)
	r.NoError(err)
	_, err = svc.UpdateResource(ctx, org.Slug, page.UID, section.UID, resources[1].UID, updateB)
	r.NoError(err)

	listed, err := svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Len(listed, 3)
	r.Equal(resources[1].UID, listed[0].UID, "swapped second resource should now be first")
	r.Equal(resources[0].UID, listed[1].UID, "swapped first resource should now be second")
	r.Equal(resources[2].UID, listed[2].UID, "untouched third resource stays last")
}

// TestReorderResources_RewritesPositionsByUIDOrder pins the contract of the
// drag-and-drop reorder endpoint: orderedUIDs[i] gets position i+1.
func TestReorderResources_RewritesPositionsByUIDOrder(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	pageReq := &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug}
	page, err := svc.CreateStatusPage(ctx, org.Slug, pageReq)
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	resources := make([]StatusPageResourceResponse, 4)
	for i := range resources {
		c := models.NewCheck(org.UID, "", "http")
		r.NoError(svc.db.CreateCheck(ctx, c))

		res, errCreate := svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: c.UID})
		r.NoError(errCreate)
		resources[i] = res
	}

	newOrder := []string{resources[3].UID, resources[1].UID, resources[0].UID, resources[2].UID}
	r.NoError(svc.ReorderResources(ctx, org.Slug, page.UID, section.UID, newOrder))

	listed, err := svc.db.ListStatusPageResources(ctx, section.UID)
	r.NoError(err)
	r.Len(listed, 4)
	for i, uid := range newOrder {
		r.Equal(uid, listed[i].UID, "position %d should be %s", i+1, uid)
		r.Equal(i+1, listed[i].Position, "position field should be %d", i+1)
	}
}

// TestReorderResources_RejectsMismatchedUIDList ensures the endpoint can't be
// used to drop or duplicate resources via a partial / extra UID list.
func TestReorderResources_RejectsMismatchedUIDList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	pageReq := &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug}
	page, err := svc.CreateStatusPage(ctx, org.Slug, pageReq)
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	resources := make([]StatusPageResourceResponse, 2)
	for i := range resources {
		c := models.NewCheck(org.UID, "", "http")
		r.NoError(svc.db.CreateCheck(ctx, c))
		res, errCreate := svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: c.UID})
		r.NoError(errCreate)
		resources[i] = res
	}

	missing := []string{resources[0].UID}
	r.ErrorIs(svc.ReorderResources(ctx, org.Slug, page.UID, section.UID, missing), ErrReorderUIDsMismatch)

	duplicate := []string{resources[0].UID, resources[0].UID}
	r.ErrorIs(svc.ReorderResources(ctx, org.Slug, page.UID, section.UID, duplicate), ErrReorderUIDsMismatch)

	stranger := []string{resources[0].UID, "00000000-0000-0000-0000-000000000000"}
	r.ErrorIs(svc.ReorderResources(ctx, org.Slug, page.UID, section.UID, stranger), ErrReorderUIDsMismatch)
}

// TestReorderSections_RewritesPositionsByUIDOrder pins the contract of the
// section reorder endpoint: orderedUIDs[i] gets position i+1.
func TestReorderSections_RewritesPositionsByUIDOrder(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	pageReq := &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug}
	page, err := svc.CreateStatusPage(ctx, org.Slug, pageReq)
	r.NoError(err)

	sections := make([]StatusPageSectionResponse, 4)
	for i := range sections {
		sec, errCreate := svc.CreateSection(
			ctx, org.Slug, page.UID,
			CreateSectionRequest{Name: fmt.Sprintf("Section %d", i), Slug: fmt.Sprintf("sec-%d", i)},
		)
		r.NoError(errCreate)
		sections[i] = sec
	}

	newOrder := []string{sections[3].UID, sections[1].UID, sections[0].UID, sections[2].UID}
	r.NoError(svc.ReorderSections(ctx, org.Slug, page.UID, newOrder))

	listed, err := svc.db.ListStatusPageSections(ctx, page.UID)
	r.NoError(err)
	r.Len(listed, 4)
	for i, uid := range newOrder {
		r.Equal(uid, listed[i].UID, "position %d should be %s", i+1, uid)
		r.Equal(i+1, listed[i].Position, "position field should be %d", i+1)
	}
}

// TestReorderSections_RejectsMismatchedUIDList ensures the endpoint can't be
// used to drop or duplicate sections via a partial / extra UID list.
func TestReorderSections_RejectsMismatchedUIDList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	pageReq := &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug}
	page, err := svc.CreateStatusPage(ctx, org.Slug, pageReq)
	r.NoError(err)

	sections := make([]StatusPageSectionResponse, 2)
	for i := range sections {
		sec, errCreate := svc.CreateSection(
			ctx, org.Slug, page.UID,
			CreateSectionRequest{Name: fmt.Sprintf("Section %d", i), Slug: fmt.Sprintf("sec-%d", i)},
		)
		r.NoError(errCreate)
		sections[i] = sec
	}

	missing := []string{sections[0].UID}
	r.ErrorIs(svc.ReorderSections(ctx, org.Slug, page.UID, missing), ErrReorderUIDsMismatch)

	duplicate := []string{sections[0].UID, sections[0].UID}
	r.ErrorIs(svc.ReorderSections(ctx, org.Slug, page.UID, duplicate), ErrReorderUIDsMismatch)

	stranger := []string{sections[0].UID, "00000000-0000-0000-0000-000000000000"}
	r.ErrorIs(svc.ReorderSections(ctx, org.Slug, page.UID, stranger), ErrReorderUIDsMismatch)
}

// TestCreateSection_AssignsSequentialPositions mirrors the resource test for
// the section level, where the same legacy bug existed.
func TestCreateSection_AssignsSequentialPositions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	pageReq := &CreateStatusPageRequest{Name: "Public", Slug: testPublicSlug}
	page, err := svc.CreateStatusPage(ctx, org.Slug, pageReq)
	r.NoError(err)

	first, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)
	r.Equal(1, first.Position)

	second, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Edge", Slug: "edge"})
	r.NoError(err)
	r.Equal(2, second.Position)

	third, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Internal", Slug: "internal"})
	r.NoError(err)
	r.Equal(3, third.Position)
}

// TestGetCheckInfo_InMaintenanceFlag verifies that getCheckInfo sets the public
// payload's InMaintenance flag only when the check sits inside an active
// maintenance window at request time.
func TestGetCheckInfo_InMaintenanceFlag(t *testing.T) {
	t.Parallel()

	now := time.Now()

	testCases := []struct {
		name string
		// window returns the (start, end) of a one-off window to attach to the
		// check, or ok=false to attach no window at all.
		window func() (start, end time.Time, ok bool)
		want   bool
	}{
		{
			name:   "no window",
			window: func() (time.Time, time.Time, bool) { return time.Time{}, time.Time{}, false },
			want:   false,
		},
		{
			name: "active window now",
			window: func() (time.Time, time.Time, bool) {
				return now.Add(-1 * time.Hour), now.Add(1 * time.Hour), true
			},
			want: true,
		},
		{
			name: "past window only",
			window: func() (time.Time, time.Time, bool) {
				return now.Add(-48 * time.Hour), now.Add(-24 * time.Hour), true
			},
			want: false,
		},
		{
			name: "upcoming window only",
			window: func() (time.Time, time.Time, bool) {
				return now.Add(24 * time.Hour), now.Add(48 * time.Hour), true
			},
			want: false,
		},
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, svc, org := setupStatusPagesTest(t)

			check := models.NewCheck(org.UID, fmt.Sprintf("chk-%d", i), "http")
			r.NoError(svc.db.CreateCheck(ctx, check))

			if start, end, ok := tc.window(); ok {
				win := models.NewMaintenanceWindow(org.UID, "Planned", start, end)
				r.NoError(svc.db.CreateMaintenanceWindow(ctx, win))
				r.NoError(svc.db.SetMaintenanceWindowChecks(ctx, win.UID, []string{check.UID}, nil))
			}

			info, err := svc.getCheckInfo(ctx, org.UID, check.UID)
			r.NoError(err)
			r.Equal(tc.want, info.InMaintenance)
		})
	}
}

// --- 24h hourly history period (spec 2026-06-30-03) ---

// TestStatusPagePeriodInfo pins the period→(bucketType, count, duration) mapping,
// which must match the badges endpoint's uptimeBarPeriodInfo so the two surfaces
// bucket identically.
func TestStatusPagePeriodInfo(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cases := []struct {
		period   models.StatusPagePeriod
		wantType string
		wantN    int
		wantDur  time.Duration
	}{
		{models.StatusPagePeriod24h, models.PeriodTypeHour, 24, time.Hour},
		{models.StatusPagePeriod7d, models.PeriodTypeDay, 7, 24 * time.Hour},
		{models.StatusPagePeriod30d, models.PeriodTypeDay, 30, 24 * time.Hour},
		{models.StatusPagePeriod90d, models.PeriodTypeDay, 90, 24 * time.Hour},
		{models.StatusPagePeriod("bogus"), models.PeriodTypeDay, 90, 24 * time.Hour},
	}

	for _, tc := range cases {
		pt, n, dur := statusPagePeriodInfo(tc.period)
		r.Equal(tc.wantType, pt, "period %q type", tc.period)
		r.Equal(tc.wantN, n, "period %q count", tc.period)
		r.Equal(tc.wantDur, dur, "period %q duration", tc.period)
	}
}

// TestPagePeriod_ResolvesEnumThenDays verifies the effective period prefers the
// enum and falls back to mapping history_days for legacy rows with an empty or
// invalid history_period.
func TestPagePeriod_ResolvesEnumThenDays(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(models.StatusPagePeriod24h, pagePeriod(&models.StatusPage{HistoryPeriod: "24h"}))
	r.Equal(models.StatusPagePeriod7d, pagePeriod(&models.StatusPage{HistoryPeriod: "7d"}))
	// Empty enum → fall back to history_days mapping.
	r.Equal(models.StatusPagePeriod30d, pagePeriod(&models.StatusPage{HistoryPeriod: "", HistoryDays: 30}))
	r.Equal(models.StatusPagePeriod7d, pagePeriod(&models.StatusPage{HistoryPeriod: "", HistoryDays: 7}))
	r.Equal(models.StatusPagePeriod90d, pagePeriod(&models.StatusPage{HistoryPeriod: "", HistoryDays: 365}))
	// Invalid enum → fall back too.
	r.Equal(models.StatusPagePeriod90d, pagePeriod(&models.StatusPage{HistoryPeriod: "nonsense", HistoryDays: 999}))
}

// TestValidateHistoryPeriod accepts the four enum values and a nil (omitted)
// pointer, and rejects anything else with ErrInvalidHistoryPeriod.
func TestValidateHistoryPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.NoError(validateHistoryPeriod(nil))
	for _, ok := range []string{"24h", "7d", "30d", "90d"} {
		v := ok
		r.NoError(validateHistoryPeriod(&v), "%q should be valid", ok)
	}
	for _, bad := range []string{"1d", "24", "h24", "", "7D"} {
		v := bad
		r.ErrorIs(validateHistoryPeriod(&v), ErrInvalidHistoryPeriod, "%q should be invalid", bad)
	}
}

// TestAggregateRawToHour_ParityWithDaily is the key regression guard: the hourly
// current-bucket synthesis must apply the exact same availability rule as the
// daily synthesis (and therefore the badges period=24h computation). Only the
// period boundary differs.
func TestAggregateRawToHour_ParityWithDaily(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	hourStart := time.Date(2026, 5, 11, 14, 0, 0, 0, time.UTC)
	dayStart := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	upStatus := int(models.ResultStatusUp)
	downStatus := int(models.ResultStatusDown)
	createdStatus := int(models.ResultStatusCreated)
	warningStatus := int(models.ResultStatusWarning)

	raw := []*models.Result{
		{Status: &upStatus, Duration: ptrFloat32(40)},
		{Status: &warningStatus, Duration: ptrFloat32(70)},
		{Status: &downStatus, Duration: ptrFloat32(9999)},
		{Status: &createdStatus}, // lifecycle → excluded
		{Status: &upStatus, Duration: ptrFloat32(60)},
	}

	hourly := aggregateRawToHour("check-1", raw, hourStart)
	daily := aggregateRawToDaily("check-1", raw, dayStart)

	r.NotNil(hourly)
	r.NotNil(daily)
	// Same availability rule: up+warning success over non-lifecycle denominator.
	r.NotNil(hourly.AvailabilityPct)
	r.NotNil(daily.AvailabilityPct)
	r.InDelta(*daily.AvailabilityPct, *hourly.AvailabilityPct, 0.0001, "hourly must match daily availability")
	r.InDelta(75.0, *hourly.AvailabilityPct, 0.01, "3 up/warning of 4 countable = 75%")
	r.Equal(*daily.TotalChecks, *hourly.TotalChecks)
	r.Equal(4, *hourly.TotalChecks, "the created lifecycle row is excluded")
	r.Equal(*daily.SuccessfulChecks, *hourly.SuccessfulChecks)
	// Only the period boundary differs.
	r.Equal(hourStart, hourly.PeriodStart)
	r.Equal(dayStart, daily.PeriodStart)
}

// TestBuildHourlyAvailabilityData_Builds24Buckets verifies the 24h view renders
// exactly 24 hourly buckets, oldest→newest, with the newest anchored on the
// current hour and correct hour boundaries.
func TestBuildHourlyAvailabilityData_Builds24Buckets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	avail := 100.0
	stored := []*models.Result{
		{CheckUID: "c1", PeriodStart: currentHour, AvailabilityPct: &avail, TotalChecks: ptrInt(60)},
	}

	out := buildHourlyAvailabilityData(stored, nil, bucketStart, true, false)
	r.NotNil(out)
	r.Len(out.DailyAvailability, 24, "24h renders 24 hourly buckets")

	// Oldest → newest, exactly one hour apart.
	for i := range out.DailyAvailability {
		want := bucketStart.Add(time.Duration(i) * time.Hour)
		got, err := time.Parse(time.RFC3339, out.DailyAvailability[i].Time)
		r.NoError(err)
		r.True(want.Equal(got.UTC()), "bucket %d should be at %s, got %s", i, want, got)
	}

	// The last bucket is the current hour and carries the stored row.
	last := out.DailyAvailability[23]
	gotLast, _ := time.Parse(time.RFC3339, last.Time)
	r.True(currentHour.Equal(gotLast.UTC()), "newest bucket is the current hour")
	r.Equal("up", last.Status)
	r.InDelta(100.0, last.AvailabilityPct, 0.001)

	// Buckets with no stored row read noData (grey), never red.
	r.Equal("noData", out.DailyAvailability[0].Status)
}

// TestEnrichHourly_HealthyCheckReads100 exercises the full 24h path through the
// public view: a healthy check whose only signal for the current hour is raw
// rows (no stored hourly rollup yet) must render 24 buckets and read 100% for
// the current hour — synthesized from raw via the shared availability rule.
func TestEnrichHourly_HealthyCheckReads100(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	// A status page configured for the 24h hourly period.
	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name:          "Public",
		Slug:          testPublicSlug,
		HistoryPeriod: strPtr("24h"),
	})
	r.NoError(err)
	r.Equal("24h", page.HistoryPeriod)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	check := models.NewCheck(org.UID, "API", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	// Insert raw 'up' results inside the current hour (no hourly rollup exists yet).
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		res := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
		res.PeriodStart = now.Add(-time.Duration(i) * time.Minute)
		r.NoError(svc.db.CreateResult(ctx, res))
	}

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)
	r.Equal("24h", view.HistoryPeriod)
	r.Len(view.Sections, 1)
	r.Len(view.Sections[0].Resources, 1)

	avail := view.Sections[0].Resources[0].Availability
	r.NotNil(avail)
	r.Equal(models.PeriodTypeHour, avail.BucketUnit, "24h view buckets by hour")
	r.Equal("24h", avail.Period)
	r.Len(avail.DailyAvailability, 24, "24h renders 24 hourly buckets")

	// The current (last) hourly bucket is synthesized from raw and reads 100%.
	last := avail.DailyAvailability[23]
	r.Equal("up", last.Status, "a healthy current hour must read up, not noData")
	r.InDelta(100.0, last.AvailabilityPct, 0.001)
	r.NotNil(avail.OverallAvailabilityPct)
	r.InDelta(100.0, *avail.OverallAvailabilityPct, 0.001)
}

func strPtr(s string) *string { return &s }
