package statuspages

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// rtRaw builds a raw result row for the response-time series.
func rtRaw(status models.ResultStatus, minutesAgo int) *models.Result {
	statusInt := int(status)
	duration := float32(42)

	return &models.Result{
		UID:         "raw",
		CheckUID:    "check-1",
		PeriodType:  models.PeriodTypeRaw,
		PeriodStart: time.Now().UTC().Add(-time.Duration(minutesAgo) * time.Minute),
		Status:      &statusInt,
		Duration:    &duration,
	}
}

// rtRollup builds an hour rollup row for the response-time series.
func rtRollup(total, success, minutesAgo int) *models.Result {
	p95 := float32(88)

	return &models.Result{
		UID:              "hour",
		CheckUID:         "check-1",
		PeriodType:       models.PeriodTypeHour,
		PeriodStart:      time.Now().UTC().Add(-time.Duration(minutesAgo) * time.Minute),
		TotalChecks:      &total,
		SuccessfulChecks: &success,
		DurationP95:      &p95,
	}
}

// TestBuildResponseTimeData_PointAvailability pins the phase-2 contract of spec
// 2026-08-26-10: every response-time point carries the availability of the slice
// it covers, folded through the canonical engine, so the public strip under the
// chart can be colored by availability while staying exactly aligned to the
// chart's own slots.
//
// The lifecycle/abandoned cases each sit next to a countable control in the same
// table: a test that only asserted "excluded rows report no data" would pass
// whether or not the exclusion rule was applied at all.
func TestBuildResponseTimeData_PointAvailability(t *testing.T) {
	t.Parallel()

	upThreshold, degradedThreshold := uptimebar.DefaultThresholds()

	tests := []struct {
		name       string
		row        *models.Result
		wantPct    *float64
		wantTotal  int
		wantUp     int
		wantStatus string
	}{
		{
			name:       "a successful raw probe is 100% and up",
			row:        rtRaw(models.ResultStatusUp, 5),
			wantPct:    ptrFloat(100),
			wantTotal:  1,
			wantUp:     1,
			wantStatus: uptimebar.StatusUp,
		},
		{
			name: "a warning raw probe still counts as up",
			row:  rtRaw(models.ResultStatusWarning, 5),
			// The positive control for this row is the `down` case below: both are
			// non-"up" probe outcomes, and only one of them may move availability.
			wantPct:    ptrFloat(100),
			wantTotal:  1,
			wantUp:     1,
			wantStatus: uptimebar.StatusUp,
		},
		{
			name:      "a failed raw probe is 0% — and a single failure is amber, not red",
			row:       rtRaw(models.ResultStatusDown, 5),
			wantPct:   ptrFloat(0),
			wantTotal: 1,
			wantUp:    0,
			// The shared small-bucket guard: one failed sample never renders red.
			wantStatus: uptimebar.StatusDegraded,
		},
		{
			name:       "a lifecycle marker carries no availability at all",
			row:        rtRaw(models.ResultStatusCreated, 5),
			wantPct:    nil,
			wantTotal:  0,
			wantUp:     0,
			wantStatus: uptimebar.StatusNoData,
		},
		{
			name:       "a running marker carries no availability at all",
			row:        rtRaw(models.ResultStatusRunning, 5),
			wantPct:    nil,
			wantTotal:  0,
			wantUp:     0,
			wantStatus: uptimebar.StatusNoData,
		},
		{
			name:       "an abandoned attempt carries no availability at all",
			row:        rtRaw(models.ResultStatusAbandoned, 5),
			wantPct:    nil,
			wantTotal:  0,
			wantUp:     0,
			wantStatus: uptimebar.StatusNoData,
		},
		{
			name:       "a healthy rollup reports its own ratio",
			row:        rtRollup(60, 60, 90),
			wantPct:    ptrFloat(100),
			wantTotal:  60,
			wantUp:     60,
			wantStatus: uptimebar.StatusUp,
		},
		{
			name:       "a rollup with one bad minute is amber, not red",
			row:        rtRollup(60, 59, 90),
			wantPct:    ptrFloat(98.33333333333333),
			wantTotal:  60,
			wantUp:     59,
			wantStatus: uptimebar.StatusDegraded,
		},
		{
			name:       "a rollup with a real outage is red",
			row:        rtRollup(60, 20, 90),
			wantPct:    ptrFloat(33.333333333333336),
			wantTotal:  60,
			wantUp:     20,
			wantStatus: uptimebar.StatusDown,
		},
		{
			name:       "an empty rollup is no-data, never 100%",
			row:        rtRollup(0, 0, 90),
			wantPct:    nil,
			wantTotal:  0,
			wantUp:     0,
			wantStatus: uptimebar.StatusNoData,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			points := buildResponseTimeData(
				[]*models.Result{tc.row}, upThreshold, degradedThreshold)
			r.Len(points, 1)

			point := points[0]
			r.Equal(tc.wantTotal, point.TotalChecks)
			r.Equal(tc.wantUp, point.SuccessfulChecks)
			r.Equal(tc.wantStatus, point.AvailabilityStatus)

			if tc.wantPct == nil {
				r.Nil(point.AvailabilityPct,
					"an uncountable row must report null, not 0 and not 100")

				return
			}

			r.NotNil(point.AvailabilityPct)
			r.InDelta(*tc.wantPct, *point.AvailabilityPct, 0.0001)
		})
	}
}

// TestBuildResponseTimeData_ExclusionsMoveNothing is the exclusion rule's
// positive control at series level: adding six uncountable rows to a series must
// leave every countable point's availability byte-identical, while adding a
// genuine failure changes the series. Without the second half, a broken
// implementation that dropped ALL availability would pass the first half.
func TestBuildResponseTimeData_ExclusionsMoveNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	upThreshold, degradedThreshold := uptimebar.DefaultThresholds()

	healthy := []*models.Result{
		rtRaw(models.ResultStatusUp, 3),
		rtRaw(models.ResultStatusUp, 2),
		rtRaw(models.ResultStatusUp, 1),
	}

	withMarkers := append([]*models.Result{
		rtRaw(models.ResultStatusCreated, 6),
		rtRaw(models.ResultStatusRunning, 5),
		rtRaw(models.ResultStatusAbandoned, 4),
	}, healthy...)

	base := buildResponseTimeData(healthy, upThreshold, degradedThreshold)
	polluted := buildResponseTimeData(withMarkers, upThreshold, degradedThreshold)

	r.Len(base, 3)
	r.Len(polluted, 6, "excluded rows still appear as CHART points — only their availability is empty")

	countable := 0

	for _, point := range polluted {
		if point.AvailabilityPct != nil {
			countable++

			r.InDelta(100.0, *point.AvailabilityPct, 0.0001)
		}
	}

	r.Equal(3, countable, "exactly the three real probes contribute availability")

	// Positive control: a genuine failure DOES change what the strip paints.
	withFailure := buildResponseTimeData(
		append([]*models.Result{rtRaw(models.ResultStatusDown, 4)}, healthy...),
		upThreshold, degradedThreshold)

	failed := 0

	for _, point := range withFailure {
		if point.AvailabilityStatus != uptimebar.StatusUp {
			failed++
		}
	}

	r.Equal(1, failed, "the failed probe must be the one point that is not up")
}

// TestBuildResponseTimeData_HonorsPageThresholds proves the classification uses
// the PAGE's configured thresholds rather than a hardcoded pair — the same
// numbers must be able to read green on a lenient page and amber on a strict
// one, exactly like the availability bar above the chart.
func TestBuildResponseTimeData_HonorsPageThresholds(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// 55/60 = 91.67%, with five failed samples so the small-bucket guard (which
	// only spares a SINGLE failure) is not what decides the color here.
	row := []*models.Result{rtRollup(60, 55, 30)}

	strict := buildResponseTimeData(row, 99.9, 99.0)
	r.Equal(uptimebar.StatusDown, strict[0].AvailabilityStatus)

	lenient := buildResponseTimeData(row, 90.0, 80.0)
	r.Equal(uptimebar.StatusUp, lenient[0].AvailabilityStatus,
		"the same ratio must follow the page's own thresholds")
}

func ptrFloat(value float64) *float64 { return &value }
