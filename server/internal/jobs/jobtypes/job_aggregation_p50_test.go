package jobtypes

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func p50Child(avg float32, p50 *float32, start time.Time) *models.Result {
	status := int(models.ResultStatusUp)
	total := 10
	dMin, dMax, dP95 := avg, avg, avg

	return &models.Result{
		UID: uuid.Must(uuid.NewV7()).String(), OrganizationUID: testOrgUID, CheckUID: testCheckUID,
		PeriodType: "hour", Status: &status,
		Duration: &avg, DurationMin: &dMin, DurationMax: &dMax, DurationP95: &dP95,
		DurationAvg: &avg, DurationP50: p50,
		TotalChecks: &total, SuccessfulChecks: &total,
		PeriodStart: start, Output: models.JSONMap{},
	}
}

func TestCalculateRawMetricsP50IsNearestRank(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// An even-sized set: nearest-rank picks the upper middle (index n/2), never
	// the interpolated 25.
	_, _, p50 := calculateRawMetrics([]float32{40, 10, 30, 20}, 100)
	r.InDelta(float32(30), p50, 0.0001)

	_, _, empty := calculateRawMetrics(nil, 0)
	r.Zero(empty)
}

func TestAggregateResults_RawRollupStoresP50(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	start := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	status := int(models.ResultStatusUp)

	mk := func(d float32) *models.Result {
		return &models.Result{
			UID: uuid.Must(uuid.NewV7()).String(), OrganizationUID: testOrgUID, CheckUID: testCheckUID,
			PeriodType: "raw", Status: &status, Duration: &d, PeriodStart: start,
		}
	}

	out := aggregateResults([]*models.Result{mk(5), mk(500), mk(10)}, "hour", start, start.Add(time.Hour))

	r.NotNil(out.DurationP50)
	r.InDelta(float32(10), *out.DurationP50, 0.0001, "median ignores the 500 outlier")
}

func TestAggregateResults_AggregatedP50MeanSkipsNilChildren(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	a, b := float32(100), float32(200)

	out := aggregateResults([]*models.Result{
		p50Child(100, &a, start.Add(time.Hour)),
		p50Child(900, nil, start.Add(2*time.Hour)), // predates the column
		p50Child(200, &b, start.Add(3*time.Hour)),
	}, "day", start, start.Add(24*time.Hour))

	r.NotNil(out.DurationP50)
	r.InDelta(float32(150), *out.DurationP50, 0.0001, "nil child is skipped, not averaged in as 0")
}

func TestAggregateResults_AggregatedP50NilWhenNoChildHasOne(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)

	out := aggregateResults([]*models.Result{
		p50Child(100, nil, start.Add(time.Hour)),
		p50Child(200, nil, start.Add(2*time.Hour)),
	}, "day", start, start.Add(24*time.Hour))

	r.Nil(out.DurationP50, "old rows have no median; it must stay NULL, not 0")
}
