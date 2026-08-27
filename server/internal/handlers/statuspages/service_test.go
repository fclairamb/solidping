package statuspages

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/badges"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
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

	return ctx, NewService(dbService, &config.Config{}, nil), org
}

// TestBuildAvailabilityData_UTCBucketing pins that daily buckets are keyed by
// their UTC-midnight start, not the server's local date. The per-bucket stats
// map is keyed by UTC day; a stat at yesterday's UTC midnight must render as a
// real status, not noData, regardless of the server's local zone.
func TestBuildAvailabilityData_UTCBucketing(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	todayUTC := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterdayUTC := todayUTC.AddDate(0, 0, -1)

	// 1440 checks, 1439 up → 99.93% (≥99.9 → up).
	byBucket := map[time.Time]uptimebar.BucketStats{
		yesterdayUTC: {Up: 1440, Total: 1440},
	}

	out := buildAvailabilityData(byBucket, nil, todayUTC, 7, true, false,
		models.DefaultAvailabilityThresholdUp, models.DefaultAvailabilityThresholdDegraded)
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
	r.Equal("up", matched.Status, "a filled bucket should render as up, not noData")
	r.InDelta(100.0, matched.AvailabilityPct, 0.01)
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

			info, _, err := svc.getCheckInfo(ctx, org.UID, check.UID)
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

// TestBuildHourlyAvailabilityData_Builds24Buckets verifies the 24h view renders
// exactly 24 hourly buckets, oldest→newest, with the newest anchored on the
// current hour and correct hour boundaries, from per-bucket stats.
func TestBuildHourlyAvailabilityData_Builds24Buckets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	byBucket := map[time.Time]uptimebar.BucketStats{
		currentHour: {Up: 60, Total: 60},
	}

	out := buildHourlyAvailabilityData(byBucket, nil, bucketStart, true, false,
		models.DefaultAvailabilityThresholdUp, models.DefaultAvailabilityThresholdDegraded)
	r.NotNil(out)
	r.Len(out.DailyAvailability, 24, "24h renders 24 hourly buckets")

	// Oldest → newest, exactly one hour apart.
	for i := range out.DailyAvailability {
		want := bucketStart.Add(time.Duration(i) * time.Hour)
		got, err := time.Parse(time.RFC3339, out.DailyAvailability[i].Time)
		r.NoError(err)
		r.True(want.Equal(got.UTC()), "bucket %d should be at %s, got %s", i, want, got)
	}

	// The last bucket is the current hour and carries the stats.
	last := out.DailyAvailability[23]
	gotLast, _ := time.Parse(time.RFC3339, last.Time)
	r.True(currentHour.Equal(gotLast.UTC()), "newest bucket is the current hour")
	r.Equal("up", last.Status)
	r.InDelta(100.0, last.AvailabilityPct, 0.001)

	// Buckets with no stats read noData (grey), never red.
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

// TestEnrichHourly_PreviousHourFilledFromRaw is the direct regression for the
// "No data where the badge shows data" bug: a check with only raw rows in the
// PREVIOUS hour (no stored hourly rollup yet — the raw→hour rollup lags) must
// render that past bucket with its real status, not noData.
func TestEnrichHourly_PreviousHourFilledFromRaw(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, HistoryPeriod: strPtr("24h"),
	})
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	check := models.NewCheck(org.UID, "API", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	// Raw rows in the PREVIOUS hour only (10 up + 1 down → 90.9% over the prev hour),
	// plus one up in the current hour. No stored hourly rollups at all.
	now := time.Now().UTC()
	prevHour := now.Truncate(time.Hour).Add(-time.Hour)
	for i := 0; i < 10; i++ {
		res := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
		res.PeriodStart = prevHour.Add(time.Duration(i) * time.Minute)
		r.NoError(svc.db.CreateResult(ctx, res))
	}
	down := models.NewResult(org.UID, check.UID, models.ResultStatusDown, 0)
	down.PeriodStart = prevHour.Add(11 * time.Minute)
	r.NoError(svc.db.CreateResult(ctx, down))

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)

	avail := view.Sections[0].Resources[0].Availability
	r.NotNil(avail)
	r.Len(avail.DailyAvailability, 24)

	// The previous hour is the 23rd bucket (index 22): bucketStart is currentHour-23h,
	// so currentHour-1h is at index 22.
	prev := avail.DailyAvailability[22]
	r.NotEqual("noData", prev.Status, "the previous hour has raw data and must NOT read noData")
	r.InDelta(100.0*10.0/11.0, prev.AvailabilityPct, 0.01, "10 up of 11 countable")
}

// TestEnrichDaily_RecentDayFilledFromRaw is the daily analog of the bug: a recent
// past day with only raw rows (before the raw→hour rollup ran) must fill its
// daily bucket rather than reading noData.
func TestEnrichDaily_RecentDayFilledFromRaw(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, HistoryPeriod: strPtr("7d"),
	})
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	check := models.NewCheck(org.UID, "API", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	// Raw rows older than the raw-retention window cannot exist in production
	// (the aggregation job rolls them up and deletes them in one transaction),
	// and uptimebar clamps its raw-tier query accordingly. This fixture's raw
	// rows are up to two days old, so the page must be configured to keep raw
	// that long for them to be a realistic dataset.
	svc.cfg.Aggregation.RetentionRaw = 7 * 24

	// Raw rows on YESTERDAY (UTC) only — no daily or hourly rollups exist.
	now := time.Now().UTC()
	yesterday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	for i := 0; i < 8; i++ {
		res := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
		res.PeriodStart = yesterday.Add(time.Duration(i) * time.Hour)
		r.NoError(svc.db.CreateResult(ctx, res))
	}

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)

	avail := view.Sections[0].Resources[0].Availability
	r.NotNil(avail)
	r.Equal(models.PeriodTypeDay, avail.BucketUnit)
	r.Len(avail.DailyAvailability, 7)

	yesterdayStr := yesterday.Format("2006-01-02")

	var matched *AvailabilityPoint

	for i := range avail.DailyAvailability {
		if avail.DailyAvailability[i].Date == yesterdayStr {
			matched = &avail.DailyAvailability[i]
		}
	}

	r.NotNil(matched, "yesterday bucket present")
	r.Equal("up", matched.Status, "yesterday has raw-only data and must read up, not noData")
	r.InDelta(100.0, matched.AvailabilityPct, 0.001)
}

// TestBadgeStatusPageParity_SameBucketsForSameData is the cross-surface parity
// guard: the same synthetic results driven through the BADGE bucketing and the
// STATUS PAGE bucketing yield identical per-bucket availability for the 24h
// window. Both surfaces delegate to uptimebar.BucketAvailability with the same
// anchor, so they cannot diverge — this test pins that contract structurally by
// comparing the badge's public bucketing against the status page's view buckets.
func TestBadgeStatusPageParity_SameBucketsForSameData(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, HistoryPeriod: strPtr("24h"),
	})
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	check := models.NewCheck(org.UID, "API", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	// A mix that exercises the success rule: up, warning (counts as up), down,
	// created (lifecycle, excluded), spread across the current and previous hour.
	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	prevHour := currentHour.Add(-time.Hour)

	seed := func(st models.ResultStatus, at time.Time) {
		res := models.NewResult(org.UID, check.UID, st, 50)
		res.PeriodStart = at
		r.NoError(svc.db.CreateResult(ctx, res))
	}

	seed(models.ResultStatusUp, currentHour.Add(1*time.Minute))
	seed(models.ResultStatusWarning, currentHour.Add(2*time.Minute))
	seed(models.ResultStatusDown, currentHour.Add(3*time.Minute))
	seed(models.ResultStatusCreated, currentHour.Add(4*time.Minute))
	seed(models.ResultStatusUp, prevHour.Add(1*time.Minute))
	seed(models.ResultStatusDown, prevHour.Add(2*time.Minute))

	bucketStart := currentHour.Add(-23 * time.Hour)

	// Badge path: GenerateBadge with the uptime-bar component drives the badge's
	// fetchBucketData → uptimebar.BucketAvailability over the same 24h window.
	// We read the badge's per-bucket source via its public bucketing seam below.
	badgeSvc := badges.NewService(svc.db, svc.cfg)
	badgeBuckets, err := badges.BucketAvailabilityForPeriod(ctx, badgeSvc, org.UID, check.UID, "24h")
	r.NoError(err)

	// Status page path: the public view's hourly availability points.
	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)
	statusPoints := view.Sections[0].Resources[0].Availability.DailyAvailability
	r.Len(statusPoints, hourlyBucketCount)

	// Both must agree on every bucket in the window.
	for i := range hourlyBucketCount {
		bucket := bucketStart.Add(time.Duration(i) * time.Hour)

		badgePct, badgeHas := badgeBuckets[bucket].AvailabilityPct()

		statusPoint := statusPoints[i]
		gotTime, perr := time.Parse(time.RFC3339, statusPoint.Time)
		r.NoError(perr)
		r.True(bucket.Equal(gotTime.UTC()), "bucket %d time mismatch", i)

		statusHas := statusPoint.Status != statusNoData
		r.Equal(badgeHas, statusHas, "bucket %s presence must match across surfaces", bucket)

		if badgeHas {
			r.InDelta(badgePct, statusPoint.AvailabilityPct, 0.0001,
				"bucket %s pct must match across surfaces", bucket)
		}
	}

	// Spot-check actual values: current hour = 2 up/warning of 3 = 66.67%,
	// previous hour = 1 up of 2 = 50%.
	curPct, _ := badgeBuckets[currentHour].AvailabilityPct()
	r.InDelta(66.6667, curPct, 0.01)
	prevPct, _ := badgeBuckets[prevHour].AvailabilityPct()
	r.InDelta(50.0, prevPct, 0.01)
}

// TestBuildResponseTimeSeries_GroupsAndSortsByRegion pins buildResponseTimeSeries'
// contract (spec 2026-08-14-04): one ResponseTimeSeries per region key, sorted
// so output order is stable, NULL/legacy region ("" key) sorting first with no
// special-casing needed, and each series' points still oldest→newest (delegated
// to the unchanged buildResponseTimeData).
func TestBuildResponseTimeSeries_GroupsAndSortsByRegion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	up := int(models.ResultStatusUp)
	mkResult := func(region *string, minutesAgo int) *models.Result {
		duration := float32(42)
		return &models.Result{
			UID:         fmt.Sprintf("r-%v-%d", region, minutesAgo),
			CheckUID:    "check-1",
			PeriodStart: time.Now().UTC().Add(-time.Duration(minutesAgo) * time.Minute),
			Region:      region,
			Status:      &up,
			Duration:    &duration,
		}
	}

	byRegion := map[string][]*models.Result{
		"us1": {mkResult(strPtr("us1"), 1), mkResult(strPtr("us1"), 2)},
		"eu2": {mkResult(strPtr("eu2"), 1)},
		"":    {mkResult(nil, 1)},
	}

	series := buildResponseTimeSeries(
		byRegion, models.DefaultAvailabilityThresholdUp, models.DefaultAvailabilityThresholdDegraded)
	r.Len(series, 3, "one series per region key")

	// Sorted order: "" < "eu2" < "us1".
	r.Nil(series[0].Region, "NULL/legacy region sorts first and has no Region label")
	r.Len(series[0].Points, 1)

	r.NotNil(series[1].Region)
	r.Equal("eu2", *series[1].Region)
	r.Len(series[1].Points, 1)

	r.NotNil(series[2].Region)
	r.Equal("us1", *series[2].Region)
	r.Len(series[2].Points, 2)

	// Empty input yields no series at all (not a slice of zero-length groups).
	r.Nil(buildResponseTimeSeries(
		nil, models.DefaultAvailabilityThresholdUp, models.DefaultAvailabilityThresholdDegraded))
	r.Nil(buildResponseTimeSeries(
		map[string][]*models.Result{},
		models.DefaultAvailabilityThresholdUp, models.DefaultAvailabilityThresholdDegraded))

	// A region group with no real duration signal (e.g. the check-creation
	// "created" lifecycle marker on its own) is dropped entirely — it must
	// never manufacture a phantom "unknown region" legend entry.
	created := int(models.ResultStatusCreated)
	markerOnly := map[string][]*models.Result{
		"": {{UID: "marker", CheckUID: "check-1", PeriodStart: time.Now(), Status: &created}},
	}
	r.Nil(buildResponseTimeSeries(
		markerOnly,
		models.DefaultAvailabilityThresholdUp, models.DefaultAvailabilityThresholdDegraded),
		"a signal-less region group must not produce a series")

	// But a marker mixed into a group that otherwise has real data is kept —
	// matching the old single-series behavior, which always tolerated it.
	mixed := map[string][]*models.Result{
		"": {
			{UID: "marker", CheckUID: "check-1", PeriodStart: time.Now(), Status: &created},
			mkResult(nil, 1),
		},
	}
	mixedSeries := buildResponseTimeSeries(
		mixed, models.DefaultAvailabilityThresholdUp, models.DefaultAvailabilityThresholdDegraded)
	r.Len(mixedSeries, 1)
	r.Len(mixedSeries[0].Points, 2, "the marker point is kept once the group has real signal")
}

// TestFetchRecentResults_PerRegionBudgetNotStarved is the direct regression for
// the bug this spec fixes: previously the 100-result budget was shared across
// every region interleaved on one check, so two busy regions split it roughly
// 50/50 instead of getting their own 100. Seed 150 raw rows in each of two
// regions and assert BOTH series come back with the full 100-point budget.
func TestFetchRecentResults_PerRegionBudgetNotStarved(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, HistoryPeriod: strPtr("7d"),
	})
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	check := models.NewCheck(org.UID, "API", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	now := time.Now().UTC()
	for _, region := range []string{"eu2", "us1"} {
		for i := 0; i < 150; i++ {
			res := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
			res.PeriodStart = now.Add(-time.Duration(i) * time.Minute)
			res.Region = strPtr(region)
			r.NoError(svc.db.CreateResult(ctx, res))
		}
	}

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)

	avail := view.Sections[0].Resources[0].Availability
	r.NotNil(avail)
	r.Len(avail.ResponseTimeSeries, 2, "one series per region")

	byRegion := make(map[string]int, 2)
	for _, s := range avail.ResponseTimeSeries {
		r.NotNil(s.Region)
		byRegion[*s.Region] = len(s.Points)
	}

	r.Equal(100, byRegion["eu2"], "eu2 must get its own full 100-point budget")
	r.Equal(100, byRegion["us1"], "us1 must get its own full 100-point budget, not starved by eu2")
}

// TestViewStatusPage_NullRegionSeries pins the legacy/single-region path: rows
// with no recorded region collapse into exactly ONE series carrying no Region
// label, matching today's single-series rendering with no per-region split.
func TestViewStatusPage_NullRegionSeries(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, HistoryPeriod: strPtr("7d"),
	})
	r.NoError(err)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	check := models.NewCheck(org.UID, "API", "http")
	r.NoError(svc.db.CreateCheck(ctx, check))
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		res := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
		res.PeriodStart = now.Add(-time.Duration(i) * time.Minute)
		// Region left nil — legacy/pre-region data.
		r.NoError(svc.db.CreateResult(ctx, res))
	}

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)

	avail := view.Sections[0].Resources[0].Availability
	r.NotNil(avail)
	r.Len(avail.ResponseTimeSeries, 1, "NULL-region rows collapse into one series")
	r.Nil(avail.ResponseTimeSeries[0].Region)
	// 5 seeded points + the one "created" lifecycle marker CreateCheck always
	// inserts (also NULL-region) — it rides along in this same group exactly
	// like the old single-series behavior always tolerated it.
	r.Len(avail.ResponseTimeSeries[0].Points, 6)
}

// TestValidateSlugLengthBoundaries pins the 3-100 char slug length window
// (raised from 3-40): 2 chars rejected, 3 accepted, 100 accepted, 101
// rejected.
func TestValidateSlugLengthBoundaries(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.ErrorIs(validateSlug("ab"), ErrInvalidSlugFormat, "2-char slug must be rejected")
	r.NoError(validateSlug("abc"), "3-char slug must be accepted")
	r.NoError(validateSlug("a"+strings.Repeat("b", 99)), "100-char slug must be accepted")
	r.ErrorIs(validateSlug("a"+strings.Repeat("b", 100)), ErrInvalidSlugFormat, "101-char slug must be rejected")
}

func strPtr(s string) *string { return &s }
