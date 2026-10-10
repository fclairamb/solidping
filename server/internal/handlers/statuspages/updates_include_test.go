package statuspages

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestParseViewOptionsUpdates(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		values url.Values
		want   ViewOptions
	}{
		{"updates alone", url.Values{"include": {"updates"}}, ViewOptions{Updates: true, UpdatesDays: DefaultUpdatesDays}},
		{
			"availability and updates", url.Values{"include": {"availability,updates"}},
			ViewOptions{Availability: true, Updates: true, UpdatesDays: DefaultUpdatesDays},
		},
		{
			"updatesDays widens the window", url.Values{"include": {"updates"}, "updatesDays": {"30"}},
			ViewOptions{Updates: true, UpdatesDays: 30},
		},
		{
			"updatesDays without updates is ignored", url.Values{"include": {"availability"}, "updatesDays": {"30"}},
			ViewOptions{Availability: true},
		},
		{"absent include still includes updates", url.Values{}, AllViewOptions()},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseViewOptions(testCase.values)
			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
		})
	}

	require.True(t, AllViewOptions().Updates)
	require.Zero(t, AllViewOptions().UpdatesDays)
}

func TestParseViewOptionsUpdatesErrors(t *testing.T) {
	t.Parallel()

	_, err := ParseViewOptions(url.Values{"include": {"updates,bogus"}})
	var invalid *InvalidIncludeError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, "bogus", invalid.Token)

	for _, bad := range []string{"0", "-3", "abc", ""} {
		_, err = ParseViewOptions(url.Values{"include": {"updates"}, "updatesDays": {bad}})
		var badDays *InvalidUpdatesDaysError
		require.ErrorAs(t, err, &badDays, bad)
	}
}

func seedUpdate(ctx context.Context, t *testing.T, svc *Service, org *models.Organization, pageUID, title string, age time.Duration) {
	t.Helper()

	upd := models.NewStatusUpdate(org.UID, pageUID, "")
	upd.Title = title
	upd.BodyMarkdown = "body"
	upd.Kind = models.StatusUpdateKindInvestigating
	upd.PublishedAt = time.Now().Add(-age)
	require.NoError(t, svc.db.CreateStatusUpdate(ctx, upd))
}

func TestViewStatusPageUpdatesWindow(t *testing.T) {
	t.Parallel()

	ctx, svc, counting, org := memoTestSetup(t)
	page := seedMemoPage(ctx, t, svc, org)

	ninety := 90
	_, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{HistoryDays: &ninety})
	require.NoError(t, err)

	day := 24 * time.Hour
	seedUpdate(ctx, t, svc, org, page.UID, "recent", day)
	seedUpdate(ctx, t, svc, org, page.UID, "old", 20*day)

	titles := func(view StatusPageResponse) []string {
		out := make([]string, 0, len(view.RecentUpdates))
		for _, upd := range view.RecentUpdates {
			out = append(out, upd.Title)
		}

		return out
	}

	// Narrow read without `updates`: omitted and the query never runs.
	counting.reset()
	narrow, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, ViewOptions{})
	require.NoError(t, err)
	require.Empty(t, narrow.RecentUpdates)
	require.Zero(t, counting.updateReads.Load())

	// `updates`: capped at 7 days even though historyDays is 90.
	capped, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, ViewOptions{Updates: true, UpdatesDays: DefaultUpdatesDays})
	require.NoError(t, err)
	require.Equal(t, []string{"recent"}, titles(capped))

	// Wider window, bounded by historyDays.
	wide, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, ViewOptions{Updates: true, UpdatesDays: 30})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"recent", "old"}, titles(wide))

	huge, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, ViewOptions{Updates: true, UpdatesDays: 100000})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"recent", "old"}, titles(huge))

	// Regression: absent include keeps the full historyDays window.
	all, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, AllViewOptions())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"recent", "old"}, titles(all))
}

func TestViewStatusPageNarrowHasNoAvailability(t *testing.T) {
	t.Parallel()

	ctx, svc, _, org := memoTestSetup(t)
	page := seedMemoPage(ctx, t, svc, org)

	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, ViewOptions{})
	require.NoError(t, err)
	require.Nil(t, view.OverallAvailabilityPct)

	for _, section := range view.Sections {
		for _, res := range section.Resources {
			require.Nil(t, res.Availability)
		}
	}

	require.Equal(t, page.ShowAvailability, view.ShowAvailability)
	require.Equal(t, page.ShowResponseTime, view.ShowResponseTime)
}

func TestPageMemoUpdatesKeysAreDistinct(t *testing.T) {
	t.Parallel()

	ctx, svc, counting, org := memoTestSetup(t)
	page := seedMemoPage(ctx, t, svc, org)

	_, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug, ViewOptions{})
	require.NoError(t, err)

	counting.reset()
	_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, ViewOptions{Updates: true, UpdatesDays: DefaultUpdatesDays})
	require.NoError(t, err)
	require.Positive(t, counting.updateReads.Load(), "include=updates is its own memo entry")

	counting.reset()
	_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, ViewOptions{Updates: true, UpdatesDays: 30})
	require.NoError(t, err)
	require.Positive(t, counting.updateReads.Load(), "a different updatesDays is its own entry")

	counting.reset()
	_, err = svc.ViewStatusPage(ctx, org.Slug, page.Slug, ViewOptions{})
	require.NoError(t, err)
	require.Zero(t, counting.computeReads(), "include= stays memoized")
	require.Len(t, svc.memo.memoKeysForPage(page.UID), 3)
}
