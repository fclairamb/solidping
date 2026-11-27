package statuspages

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/maintenancewindows"
)

// --- Upcoming maintenance on public pages (spec 2026-11-27-06) ---

type upcomingFixture struct {
	svc   *Service
	org   *models.Organization
	page  StatusPageResponse
	check *models.Check
}

type windowMaker func(start, end time.Time, rec string, checks, groups []string) *models.MaintenanceWindow

func newUpcomingFixture(t *testing.T) (*upcomingFixture, windowMaker) {
	t.Helper()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

	check := models.NewCheck(org.UID, "api", "http")
	apiName := "api"
	check.Name = &apiName
	r.NoError(svc.db.CreateCheck(ctx, check))

	_, err := svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	mk := func(start, end time.Time, rec string, checks, groups []string) *models.MaintenanceWindow {
		win := models.NewMaintenanceWindow(org.UID, "DB upgrade", start, end)
		desc := "Read-only mode"
		win.Description = &desc

		if rec != "" {
			win.Recurrence = rec
		}

		r.NoError(svc.db.CreateMaintenanceWindow(ctx, win))
		r.NoError(svc.db.SetMaintenanceWindowChecks(ctx, win.UID, checks, groups))

		return win
	}

	return &upcomingFixture{svc: svc, org: org, page: page, check: check}, mk
}

func (f *upcomingFixture) view(t *testing.T) StatusPageResponse {
	t.Helper()

	view, err := f.svc.ViewStatusPage(t.Context(), f.org.Slug, f.page.Slug, AllViewOptions())
	require.NoError(t, err)

	return view
}

func TestUpcomingMaintenance_OneOffListed(t *testing.T) {
	t.Parallel()

	f, mk := newUpcomingFixture(t)
	now := time.Now()
	win := mk(now.Add(2*time.Hour), now.Add(3*time.Hour), "", []string{f.check.UID}, nil)

	got := f.view(t).UpcomingMaintenance
	require.Len(t, got, 1)
	require.Equal(t, win.UID, got[0].UID)
	require.Equal(t, "DB upgrade", got[0].Title)
	require.Equal(t, "Read-only mode", got[0].Description)
	require.False(t, got[0].Active)
	require.Equal(t, []string{"api"}, got[0].AffectedResources)
}

func TestUpcomingMaintenance_BeyondHorizonAbsent(t *testing.T) {
	t.Parallel()

	f, mk := newUpcomingFixture(t)
	now := time.Now()
	mk(now.Add(8*24*time.Hour), now.Add(8*24*time.Hour+time.Hour), "", []string{f.check.UID}, nil)

	require.Empty(t, f.view(t).UpcomingMaintenance)
}

func TestUpcomingMaintenance_RecurringNextOccurrence(t *testing.T) {
	t.Parallel()

	f, mk := newUpcomingFixture(t)
	now := time.Now()
	// Started 10 days ago, daily: the next occurrence is within 24h.
	mk(now.Add(-10*24*time.Hour+30*time.Minute), now.Add(-10*24*time.Hour+time.Hour+30*time.Minute),
		models.RecurrenceDaily, []string{f.check.UID}, nil)

	got := f.view(t).UpcomingMaintenance
	require.NotEmpty(t, got)
	require.True(t, got[0].StartsAt.After(now) || got[0].Active)
	require.LessOrEqual(t, len(got), upcomingMaintenanceMax)
}

func TestUpcomingMaintenance_ActiveFlag(t *testing.T) {
	t.Parallel()

	f, mk := newUpcomingFixture(t)
	now := time.Now()
	mk(now.Add(-time.Hour), now.Add(time.Hour), "", []string{f.check.UID}, nil)

	got := f.view(t).UpcomingMaintenance
	require.Len(t, got, 1)
	require.True(t, got[0].Active)
}

func TestUpcomingMaintenance_UnshownCheckAbsent(t *testing.T) {
	t.Parallel()

	f, mk := newUpcomingFixture(t)
	other := models.NewCheck(f.org.UID, "internal", "http")
	require.NoError(t, f.svc.db.CreateCheck(t.Context(), other))

	now := time.Now()
	mk(now.Add(time.Hour), now.Add(2*time.Hour), "", []string{other.UID}, nil)

	require.Empty(t, f.view(t).UpcomingMaintenance)
}

func TestUpcomingMaintenance_GroupListedWithPublicName(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, mk := newUpcomingFixture(t)
	ctx := t.Context()

	group := models.NewCheckGroup(f.org.UID, "Web frontend", "web-frontend")
	r.NoError(f.svc.db.CreateCheckGroup(ctx, group))

	sections, err := f.svc.db.ListStatusPageSections(ctx, f.page.UID)
	r.NoError(err)

	_, err = f.svc.CreateResource(ctx, f.org.Slug, f.page.UID, sections[0].UID,
		CreateResourceRequest{CheckGroupUID: group.UID})
	r.NoError(err)

	now := time.Now()
	mk(now.Add(time.Hour), now.Add(2*time.Hour), "", nil, []string{group.UID})

	got := f.view(t).UpcomingMaintenance
	r.Len(got, 1)
	r.Equal([]string{"Web frontend"}, got[0].AffectedResources)
}

func TestUpcomingMaintenance_AdminPayloadHasNone(t *testing.T) {
	t.Parallel()

	f, mk := newUpcomingFixture(t)
	now := time.Now()
	mk(now.Add(time.Hour), now.Add(2*time.Hour), "", []string{f.check.UID}, nil)

	pages, err := f.svc.ListStatusPages(t.Context(), f.org.Slug)
	require.NoError(t, err)
	require.NotEmpty(t, pages)

	for i := range pages {
		require.Empty(t, pages[i].UpcomingMaintenance)
	}
}

func TestUpcomingMaintenance_WindowWriteInvalidatesMemo(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, org := memoTestSetup(t)
	page := seedMemoPage(ctx, t, svc, org)

	checks, _, err := svc.db.ListChecks(ctx, org.UID, nil)
	r.NoError(err)
	r.NotEmpty(checks)

	first, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)
	r.Empty(first.UpcomingMaintenance)

	mwSvc := maintenancewindows.NewService(svc.db)
	mwSvc.SetPageMemoInvalidator(svc)

	now := time.Now()
	win, err := mwSvc.CreateMaintenanceWindow(ctx, org.Slug, &maintenancewindows.CreateRequest{
		Title: "Planned", StartAt: now.Add(time.Hour), EndAt: now.Add(2 * time.Hour),
	})
	r.NoError(err)
	r.NoError(mwSvc.SetChecks(ctx, org.Slug, win.UID, maintenancewindows.SetChecksRequest{
		CheckUIDs: []string{checks[0].UID},
	}))

	second, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	r.NoError(err)
	r.Len(second.UpcomingMaintenance, 1, "the write must evict the memoized page")
}
