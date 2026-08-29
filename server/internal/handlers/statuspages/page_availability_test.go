package statuspages

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// --- Page-level uptime aggregate (spec 2026-08-29-08) -----------------------
//
// `overallAvailabilityPct` is the number a wallboard renders a metre high, so
// the two ways of getting it wrong both matter: computing a *different* number
// from the rows printed beneath it, and printing a confident number for
// resources that have never reported anything.

// TestMeanResourceAvailability is the pure-math half — the definition, tested
// without a database so the boundary cases are unambiguous.
func TestMeanResourceAvailability(t *testing.T) {
	t.Parallel()

	pct := func(v float64) *ResourceAvailabilityData {
		return &ResourceAvailabilityData{OverallAvailabilityPct: &v}
	}

	testCases := []struct {
		name     string
		sections []StatusPageSectionResponse
		want     *float64
	}{
		{
			name: "mean of the per-resource percentages",
			sections: []StatusPageSectionResponse{{Resources: []StatusPageResourceResponse{
				{Availability: pct(100)},
				{Availability: pct(98)},
			}}},
			want: float64Ptr(99),
		},
		{
			name: "the mean spans sections, not just one",
			sections: []StatusPageSectionResponse{
				{Resources: []StatusPageResourceResponse{{Availability: pct(100)}}},
				{Resources: []StatusPageResourceResponse{{Availability: pct(50)}}},
			},
			want: float64Ptr(75),
		},
		{
			name: "resources with no data are excluded, not counted as 0",
			sections: []StatusPageSectionResponse{{Resources: []StatusPageResourceResponse{
				{Availability: pct(90)},
				{Availability: &ResourceAvailabilityData{}},
				{Availability: nil},
			}}},
			want: float64Ptr(90),
		},
		{
			name: "...and not counted as 100 either",
			sections: []StatusPageSectionResponse{{Resources: []StatusPageResourceResponse{
				{Availability: pct(50)},
				{Availability: nil},
			}}},
			want: float64Ptr(50),
		},
		{
			name: "nothing has data at all: no number rather than a lie",
			sections: []StatusPageSectionResponse{{Resources: []StatusPageResourceResponse{
				{Availability: nil},
			}}},
			want: nil,
		},
		{
			name:     "a page with no resources yields nothing",
			sections: nil,
			want:     nil,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got := meanResourceAvailability(testCase.sections)

			if testCase.want == nil {
				r.Nil(got)

				return
			}

			r.NotNil(got)
			r.InDelta(*testCase.want, *got, 0.0001)
		})
	}
}

func float64Ptr(v float64) *float64 { return &v }

// seedCheckWithResults attaches a check to the page's first section and writes
// `up` results for it, plus `down` ones, so the resource lands on a known
// availability.
func seedCheckWithResults(
	ctx context.Context, t *testing.T, svc *Service, org *models.Organization,
	pageUID, sectionUID, name string, up, down int,
) {
	t.Helper()

	r := require.New(t)

	check := models.NewCheck(org.UID, name, "http")
	r.NoError(svc.db.CreateCheck(ctx, check))

	_, err := svc.CreateResource(ctx, org.Slug, pageUID, sectionUID, CreateResourceRequest{CheckUID: check.UID})
	r.NoError(err)

	now := time.Now().UTC()

	for i := range up {
		res := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
		res.PeriodStart = now.Add(-time.Duration(i) * time.Minute)
		r.NoError(svc.db.CreateResult(ctx, res))
	}

	for i := range down {
		res := models.NewResult(org.UID, check.UID, models.ResultStatusDown, 42)
		res.PeriodStart = now.Add(-time.Duration(up+i) * time.Minute)
		r.NoError(svc.db.CreateResult(ctx, res))
	}
}

// buildAggregatePage stands up a 24h page with two reporting checks (100 % and
// 50 %) and one check that has never reported, which is the case the mean must
// exclude.
func buildAggregatePage(
	ctx context.Context, t *testing.T, svc *Service, org *models.Organization,
	showAvailability bool,
) StatusPageResponse {
	t.Helper()

	r := require.New(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name:          "Public",
		Slug:          testPublicSlug,
		HistoryPeriod: strPtr("24h"),
	})
	r.NoError(err)

	// Turned off through UPDATE rather than CREATE on purpose: bun omits a
	// zero-valued field whose column tag declares a `default:`, so
	// `showAvailability: false` on create never reaches the database at all
	// (the same trap documented on StatusPage.AutoPublishDelaySeconds). That
	// is a pre-existing bug in the create path, not this spec's, and going
	// through the update path keeps this fixture honest about which state it
	// is actually testing.
	if !showAvailability {
		_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
			ShowAvailability: &showAvailability,
		})
		r.NoError(err)
	}

	dropDefaultSections(ctx, t, svc, page.UID)

	section, err := svc.CreateSection(ctx, org.Slug, page.UID, CreateSectionRequest{Name: "Core", Slug: "core"})
	r.NoError(err)

	seedCheckWithResults(ctx, t, svc, org, page.UID, section.UID, "Healthy", 10, 0)
	seedCheckWithResults(ctx, t, svc, org, page.UID, section.UID, "Half", 5, 5)

	// A third resource that has never reported: it must not drag the mean to
	// 66 % (counted as 0) nor lift it to 83 % (counted as 100).
	silent := models.NewCheck(org.UID, "Silent", "http")
	r.NoError(svc.db.CreateCheck(ctx, silent))
	_, err = svc.CreateResource(ctx, org.Slug, page.UID, section.UID, CreateResourceRequest{CheckUID: silent.UID})
	r.NoError(err)

	return page
}

// TestPageOverallAvailabilityIsTheMeanOfItsRows drives the real view: the
// number on the board has to be the mean of the numbers on the rows, which is
// the property that makes it verifiable by a reader standing in front of it.
func TestPageOverallAvailabilityIsTheMeanOfItsRows(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)
	buildAggregatePage(ctx, t, svc, org, true)

	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.NotNil(view.OverallAvailabilityPct)

	// Recompute from the rendered rows rather than from the fixture's intent:
	// if the aggregate ever stops matching what the page shows, this fails.
	var (
		sum   float64
		count int
	)

	for _, section := range view.Sections {
		for _, resource := range section.Resources {
			if resource.Availability != nil && resource.Availability.OverallAvailabilityPct != nil {
				sum += *resource.Availability.OverallAvailabilityPct
				count++
			}
		}
	}

	r.Equal(2, count, "the silent resource must be excluded from the mean")
	r.InDelta(sum/float64(count), *view.OverallAvailabilityPct, 0.0001)
	r.InDelta(75.0, *view.OverallAvailabilityPct, 0.0001, "mean of 100 and 50")
}

// TestPageOverallAvailabilityOmittedWhenHidden — a page whose operator chose
// not to publish per-resource uptime must not publish the aggregate through
// the back door.
func TestPageOverallAvailabilityOmittedWhenHidden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)
	buildAggregatePage(ctx, t, svc, org, false)

	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.Nil(view.OverallAvailabilityPct)

	summary, err := svc.ViewStatusPageSummary(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.Nil(summary.OverallAvailabilityPct)
}

// TestSummaryAndPageAgreeOnTheNumber — two surfaces publishing two different
// uptime figures for the same page is worse than publishing none.
func TestSummaryAndPageAgreeOnTheNumber(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)
	buildAggregatePage(ctx, t, svc, org, true)

	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.NotNil(view.OverallAvailabilityPct)

	summary, err := svc.ViewStatusPageSummary(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.NotNil(summary.OverallAvailabilityPct)
	r.InDelta(*view.OverallAvailabilityPct, *summary.OverallAvailabilityPct, 0.0001)
}

// TestBadgeDoesNotPayForTheAggregate pins the opt-out: the badge is embedded in
// READMEs and is the hottest caller of the summary path, and it has nowhere to
// render a percentage.
func TestBadgeDoesNotPayForTheAggregate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)
	buildAggregatePage(ctx, t, svc, org, true)

	lean, err := svc.viewStatusPageSummary(ctx, org.Slug, testPublicSlug, false)
	r.NoError(err)
	r.Nil(lean.OverallAvailabilityPct)

	full, err := svc.viewStatusPageSummary(ctx, org.Slug, testPublicSlug, true)
	r.NoError(err)
	r.NotNil(full.OverallAvailabilityPct, "positive control: the JSON summary does carry it")
}
