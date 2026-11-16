package statuspages

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func intPtr(v int) *int { return &v }

// historyDaysOutOfRange are the legacy historyDays values the write path must
// refuse when historyPeriod is absent (spec 2026-11-16-01).
func historyDaysOutOfRange() []int { return []int{0, -1, 91, 2000000000} }

// TestCreateStatusPage_RefusesOutOfRangeHistoryDays: a create carrying the
// legacy historyDays outside 1..90, with no historyPeriod, is a validation
// error and writes nothing.
func TestCreateStatusPage_RefusesOutOfRangeHistoryDays(t *testing.T) {
	t.Parallel()

	for _, days := range historyDaysOutOfRange() {
		r := require.New(t)
		ctx, svc, org := setupStatusPagesTest(t)

		_, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
			Name: "Public", Slug: testPublicSlug, HistoryDays: intPtr(days),
		})
		r.ErrorIs(err, ErrInvalidHistoryDays, "historyDays=%d must be refused", days)

		pages, err := svc.ListStatusPages(ctx, org.Slug)
		r.NoError(err)
		r.Empty(pages, "a refused create must not write a page (historyDays=%d)", days)
	}
}

// TestCreateStatusPage_AcceptsInRangeHistoryDays is the positive control: 30
// is accepted and stored as the 30d period. With historyPeriod present, the
// legacy field is ignored whatever its value.
func TestCreateStatusPage_AcceptsInRangeHistoryDays(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, HistoryDays: intPtr(30),
	})
	r.NoError(err)
	r.Equal("30d", page.HistoryPeriod)
	r.Equal(30, page.HistoryDays)

	other, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Other", Slug: "other", HistoryPeriod: strPtr("7d"), HistoryDays: intPtr(2000000000),
	})
	r.NoError(err, "historyDays is ignored when historyPeriod is present")
	r.Equal("7d", other.HistoryPeriod)
	r.Equal(7, other.HistoryDays)
}

// TestUpdateStatusPage_RefusesOutOfRangeHistoryDays: same bound on update, and
// the stored row is left as it was.
func TestUpdateStatusPage_RefusesOutOfRangeHistoryDays(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, HistoryPeriod: strPtr("7d"),
	})
	r.NoError(err)

	for _, days := range historyDaysOutOfRange() {
		_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{HistoryDays: intPtr(days)})
		r.ErrorIs(err, ErrInvalidHistoryDays, "historyDays=%d must be refused", days)
	}

	got, err := svc.GetStatusPage(ctx, org.Slug, page.UID, GetStatusPageOptions{})
	r.NoError(err)
	r.Equal("7d", got.HistoryPeriod)
	r.Equal(7, got.HistoryDays)

	updated, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{HistoryDays: intPtr(30)})
	r.NoError(err)
	r.Equal("30d", updated.HistoryPeriod)
	r.Equal(30, updated.HistoryDays)
}

// setupHistoryDaysPage creates a daily page with one resource and returns the
// raw SQLite service so the test can write history_days straight to the row.
func setupHistoryDaysPage(
	t *testing.T, period string,
) (context.Context, *Service, *sqlite.Service, *models.Organization, StatusPageResponse) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	svc := NewService(dbService, &config.Config{}, nil)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug, HistoryPeriod: strPtr(period),
	})
	r.NoError(err)

	dropDefaultSections(ctx, t, svc, page.UID)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	check := models.NewCheck(org.UID, "API", "http")
	r.NoError(dbService.CreateCheck(ctx, check))
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	return ctx, svc, dbService, org, page
}

// TestViewStatusPage_ClampsStoredHistoryDays: a row written before the write
// path validated historyDays (forced to 2e9 here) renders exactly 90 daily
// points per resource instead of sizing its allocation from the column, and
// its status-update timeline still loads.
func TestViewStatusPage_ClampsStoredHistoryDays(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, dbService, org, page := setupHistoryDaysPage(t, "90d")

	_, err := dbService.DB().ExecContext(ctx,
		`update status_pages set history_days = 2000000000 where uid = ?`, page.UID)
	r.NoError(err)

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)
	r.Len(view.Sections, 1)
	r.Len(view.Sections[0].Resources, 1)

	avail := view.Sections[0].Resources[0].Availability
	r.NotNil(avail)
	r.Equal(models.PeriodTypeDay, avail.BucketUnit)
	r.Len(avail.DailyAvailability, MaxHistoryDays, "the window is clamped to 90 days")
}

// TestViewStatusPage_NormalHistoryDays is the positive control: a 30d page
// renders 30 points.
func TestViewStatusPage_NormalHistoryDays(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, org, page := setupHistoryDaysPage(t, "30d")

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)

	avail := view.Sections[0].Resources[0].Availability
	r.NotNil(avail)
	r.Len(avail.DailyAvailability, 30)
}

// TestBuildAvailabilityData_ClampsHistoryDays pins the clamp inside the slice
// builder itself: a negative count no longer panics on make, a huge one no
// longer allocates in proportion.
func TestBuildAvailabilityData_ClampsHistoryDays(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	todayUTC := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	for days, want := range map[int]int{-1: 1, 0: 1, 30: 30, 91: 90, 2000000000: 90} {
		out := buildAvailabilityData(nil, nil, todayUTC, days, true, false,
			models.DefaultAvailabilityThresholdUp, models.DefaultAvailabilityThresholdDegraded)
		r.Len(out.DailyAvailability, want, "historyDays=%d", days)
	}
}

func TestClampHistoryDays(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(1, ClampHistoryDays(-5))
	r.Equal(1, ClampHistoryDays(0))
	r.Equal(45, ClampHistoryDays(45))
	r.Equal(90, ClampHistoryDays(91))
	r.Equal(90, ClampHistoryDays(2000000000))
}

func TestValidateHistoryDays(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.NoError(validateHistoryDays(nil, nil))
	r.NoError(validateHistoryDays(nil, intPtr(1)))
	r.NoError(validateHistoryDays(nil, intPtr(90)))
	r.NoError(validateHistoryDays(strPtr("30d"), intPtr(-1)), "the enum wins: historyDays is ignored")

	for _, days := range historyDaysOutOfRange() {
		r.ErrorIs(validateHistoryDays(nil, intPtr(days)), ErrInvalidHistoryDays, "historyDays=%d", days)
	}
}
