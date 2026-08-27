package uptimebar

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// regionRawRow is rawRow with a region tag.
func regionRawRow(
	checkUID string, status models.ResultStatus, start time.Time, region string,
) *models.Result {
	row := rawRow(checkUID, status, start, 0.1)
	row.Region = &region

	return row
}

// regionHourRow is hourRow with a region tag.
func regionHourRow(checkUID string, total, success int, start time.Time, region string) *models.Result {
	row := hourRow(checkUID, total, success, start)
	row.Region = &region

	return row
}

// TestBucketAvailabilityInRegions covers both halves of the region rule, with the
// fixture chosen so summing and averaging disagree: eu contributes one failed
// probe, us three successful ones. Summing up/total gives 75%; averaging the two
// regions' percentages would give 50%.
func TestBucketAvailabilityInRegions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	hour := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)

	lister := &fakeLister{results: []*models.Result{
		regionRawRow("c1", models.ResultStatusDown, hour.Add(time.Minute), "eu-1"),
		regionRawRow("c1", models.ResultStatusUp, hour.Add(2*time.Minute), "us-1"),
		regionRawRow("c1", models.ResultStatusUp, hour.Add(3*time.Minute), "us-1"),
		regionRawRow("c1", models.ResultStatusUp, hour.Add(4*time.Minute), "us-1"),
	}}

	all, err := BucketAvailability(ctx, lister, "org", []string{"c1"}, time.Hour, hour, 1, hints())
	r.NoError(err)

	allPct, ok := all["c1"][hour].AvailabilityPct()
	r.True(ok)
	r.InDelta(75.0, allPct, 0.001, "no filter must SUM up/total across regions, not average percentages")

	// A fresh lister so the captured filters below belong to the FILTERED read
	// only (filterFor returns the first tier match, and the unfiltered read above
	// issued the same tier lists).
	euLister := &fakeLister{results: lister.results}

	eu, err := BucketAvailabilityInRegions(
		ctx, euLister, "org", []string{"c1"}, []string{"eu-1"}, time.Hour, hour, 1, hints())
	r.NoError(err)

	euStats := eu["c1"][hour]
	r.Equal(1, euStats.Total)

	euPct, ok := euStats.AvailabilityPct()
	r.True(ok)
	r.InDelta(0.0, euPct, 0.001)

	// Positive control: the filter genuinely reaches the query, so the two reads
	// disagree. A filter applied nowhere would return 75% here too.
	r.NotEqual(allPct, euPct)

	// And the filter is pushed into BOTH tier queries, not just one of them.
	for _, tiers := range [][]string{
		{models.PeriodTypeHour, models.PeriodTypeDay},
		{models.PeriodTypeRaw},
	} {
		filter := euLister.filterFor(tiers...)
		r.NotNil(filter)
		r.Equal([]string{"eu-1"}, filter.Regions,
			"every tier must carry the region filter — rollups keep their region too")
	}
}

// TestBucketAvailabilityInRegions_ReachesRollups is the region rule's second
// positive control: hour/day rollups carry the region they were rolled up from,
// so a region-scoped read must reach them. Filtering raw only (or after the
// fetch) would report the sum of both regions here.
func TestBucketAvailabilityInRegions_ReachesRollups(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		regionHourRow("c1", 60, 30, hour, "eu-1"),
		regionHourRow("c1", 60, 60, hour, "us-1"),
	}}

	eu, err := BucketAvailabilityInRegions(
		ctx, lister, "org", []string{"c1"}, []string{"eu-1"}, time.Hour, hour, 1, hints())
	r.NoError(err)
	r.Equal(60, eu["c1"][hour].Total)

	pct, ok := eu["c1"][hour].AvailabilityPct()
	r.True(ok)
	r.InDelta(50.0, pct, 0.001)
}

// TestWindowAvailabilityInRegions mirrors the bucketed test for the whole-window
// fold, so the chart's header stat and its strip can never disagree about which
// region they are describing.
func TestWindowAvailabilityInRegions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	start := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	end := start.Add(time.Hour)

	lister := &fakeLister{results: []*models.Result{
		regionRawRow("c1", models.ResultStatusDown, start.Add(time.Minute), "eu-1"),
		regionRawRow("c1", models.ResultStatusUp, start.Add(2*time.Minute), "us-1"),
		regionRawRow("c1", models.ResultStatusUp, start.Add(3*time.Minute), "us-1"),
		regionRawRow("c1", models.ResultStatusUp, start.Add(4*time.Minute), "us-1"),
	}}

	all, err := WindowAvailability(ctx, lister, "org", []string{"c1"}, start, end, hints())
	r.NoError(err)

	allPct, ok := all["c1"].AvailabilityPct()
	r.True(ok)
	r.InDelta(75.0, allPct, 0.001)

	us, err := WindowAvailabilityInRegions(
		ctx, lister, "org", []string{"c1"}, []string{"us-1"}, start, end, hints())
	r.NoError(err)

	usPct, ok := us["c1"].AvailabilityPct()
	r.True(ok)
	r.InDelta(100.0, usPct, 0.001)
	r.NotEqual(allPct, usPct)
}

// TestClassify pins the shared green/amber/red mapping, including the
// small-bucket calibration guard that keeps a single failed sample out of red.
func TestClassify(t *testing.T) {
	t.Parallel()

	up, degraded := DefaultThresholds()
	r := require.New(t)
	r.InDelta(99.9, up, 0.0001)
	r.InDelta(99.0, degraded, 0.0001)

	tests := []struct {
		name     string
		pct      float64
		failures int
		want     string
	}{
		{"perfect", 100, 0, StatusUp},
		{"at the up threshold", 99.9, 1, StatusUp},
		{"just under the up threshold", 99.5, 3, StatusDegraded},
		{"at the degraded threshold", 99.0, 5, StatusDegraded},
		{"well under with many failures", 50, 30, StatusDown},
		{"well under but a single failed sample stays amber", 50, 1, StatusDegraded},
		{"two failed samples may go red", 50, 2, StatusDown},
		{"zero availability with one sample stays amber", 0, 1, StatusDegraded},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, Classify(tc.pct, tc.failures, up, degraded))
		})
	}
}

// TestClassifyStats covers the no-data rule the whole spec turns on: an empty
// bucket is gray, never a manufactured 100%.
func TestClassifyStats(t *testing.T) {
	t.Parallel()

	up, degraded := DefaultThresholds()
	r := require.New(t)

	r.Equal(StatusNoData, ClassifyStats(BucketStats{}, up, degraded),
		"a bucket with no countable probes is noData, not up")
	r.Equal(StatusUp, ClassifyStats(BucketStats{Up: 60, Total: 60}, up, degraded))
	r.Equal(StatusDown, ClassifyStats(BucketStats{Up: 30, Total: 60}, up, degraded))
}
