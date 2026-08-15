package jobtypes

import (
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const (
	testOrgUID    = "org-123"
	testCheckUID  = "check-456"
	testRegion    = "us-east-1"
	testWorkerUID = "worker-1"
)

func TestCalculateAggregationBoundary(t *testing.T) {
	t.Parallel()

	// Mock time to 2025-12-19 13:15:00 UTC
	now := time.Date(2025, 12, 19, 13, 15, 0, 0, time.UTC)

	tests := []struct {
		name         string
		sourcePeriod string
		want         time.Time
		wantErr      bool
	}{
		{
			name:         "raw to hour",
			sourcePeriod: "raw",
			want:         time.Date(2025, 12, 19, 13, 0, 0, 0, time.UTC),
			wantErr:      false,
		},
		{
			name:         "hour to day",
			sourcePeriod: "hour",
			want:         time.Date(2025, 12, 19, 0, 0, 0, 0, time.UTC),
			wantErr:      false,
		},
		{
			name:         "day to month",
			sourcePeriod: "day",
			want:         time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
			wantErr:      false,
		},
		{
			name:         "invalid period",
			sourcePeriod: "invalid",
			want:         time.Time{},
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := calculateAggregationBoundary(tt.sourcePeriod, 1, 1, 1)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Verify it's not zero time
			assert.False(t, got.IsZero(), "boundary should not be zero time")

			// Verify it's in the past (boundaries are always before now)
			assert.True(t, got.Before(time.Now()), "boundary should be in the past")
		})
	}

	// Test with controlled time - at least verify the logic is correct
	t.Run("raw period boundary logic", func(t *testing.T) {
		t.Parallel()
		boundary, err := calculateAggregationBoundary("raw", 1, 1, 1)
		require.NoError(t, err)

		// Should be truncated to the hour
		assert.Equal(t, 0, boundary.Minute())
		assert.Equal(t, 0, boundary.Second())
		assert.Equal(t, 0, boundary.Nanosecond())
	})

	t.Run("retention > 1 extends the keep window", func(t *testing.T) {
		t.Parallel()
		// raw=24 means current hour + 23 prior completed hours stay raw
		boundary, err := calculateAggregationBoundary("raw", 24, 30, 12)
		require.NoError(t, err)

		expected := time.Now().UTC().Truncate(time.Hour).Add(-23 * time.Hour)
		assert.WithinDuration(t, expected, boundary, time.Second)
	})

	_ = now // Suppress unused variable warning
}

func TestCalculatePeriodBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		timestamp    time.Time
		targetPeriod string
		wantStart    time.Time
		wantEnd      time.Time
		wantErr      bool
	}{
		{
			name:         "hour boundary",
			timestamp:    time.Date(2025, 12, 19, 13, 45, 30, 0, time.UTC),
			targetPeriod: "hour",
			wantStart:    time.Date(2025, 12, 19, 13, 0, 0, 0, time.UTC),
			wantEnd:      time.Date(2025, 12, 19, 13, 59, 59, 999000000, time.UTC),
			wantErr:      false,
		},
		{
			name:         "day boundary",
			timestamp:    time.Date(2025, 12, 19, 13, 45, 30, 0, time.UTC),
			targetPeriod: "day",
			wantStart:    time.Date(2025, 12, 19, 0, 0, 0, 0, time.UTC),
			wantEnd:      time.Date(2025, 12, 19, 23, 59, 59, 999000000, time.UTC),
			wantErr:      false,
		},
		{
			name:         "month boundary",
			timestamp:    time.Date(2025, 12, 19, 13, 45, 30, 0, time.UTC),
			targetPeriod: "month",
			wantStart:    time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:      time.Date(2025, 12, 31, 23, 59, 59, 999000000, time.UTC),
			wantErr:      false,
		},
		{
			name:         "invalid period",
			timestamp:    time.Date(2025, 12, 19, 13, 45, 30, 0, time.UTC),
			targetPeriod: "invalid",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStart, gotEnd, err := calculatePeriodBoundaries(tt.timestamp, tt.targetPeriod)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantStart, gotStart)
			assert.Equal(t, tt.wantEnd, gotEnd)
		})
	}
}

func TestAggregateResults_RawData(t *testing.T) {
	t.Parallel()

	orgUID := testOrgUID
	checkUID := testCheckUID
	region := testRegion
	periodStart := time.Date(2025, 12, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2025, 12, 19, 12, 59, 59, 999000000, time.UTC)

	// Create sample raw results
	statusUp := int(models.ResultStatusUp)
	statusDown := int(models.ResultStatusDown)
	duration1 := float32(100.0)
	duration2 := float32(200.0)
	duration3 := float32(150.0)
	workerUID := testWorkerUID

	results := []*models.Result{
		{
			UID:             "result-1",
			OrganizationUID: orgUID,
			CheckUID:        checkUID,
			Region:          &region,
			WorkerUID:       &workerUID,
			Status:          &statusUp,
			Duration:        &duration1,
			PeriodStart:     periodStart.Add(5 * time.Minute),
			Output:          models.JSONMap{"msg": "ok"},
		},
		{
			UID:             "result-2",
			OrganizationUID: orgUID,
			CheckUID:        checkUID,
			Region:          &region,
			WorkerUID:       &workerUID,
			Status:          &statusUp,
			Duration:        &duration2,
			PeriodStart:     periodStart.Add(10 * time.Minute),
			Output:          models.JSONMap{"msg": "ok2"},
		},
		{
			UID:             "result-3",
			OrganizationUID: orgUID,
			CheckUID:        checkUID,
			Region:          &region,
			WorkerUID:       &workerUID,
			Status:          &statusDown,
			Duration:        &duration3,
			PeriodStart:     periodStart.Add(15 * time.Minute), // Last one
			Output:          models.JSONMap{"msg": "error"},
		},
	}

	compacted := aggregateResults(results, "hour", periodStart, periodEnd)

	// Verify basic fields
	assert.NotEmpty(t, compacted.UID)
	assert.Equal(t, orgUID, compacted.OrganizationUID)
	assert.Equal(t, checkUID, compacted.CheckUID)
	assert.Equal(t, "hour", compacted.PeriodType)
	assert.Equal(t, periodStart, compacted.PeriodStart)
	assert.Equal(t, &periodEnd, compacted.PeriodEnd)
	assert.Equal(t, &region, compacted.Region)

	// Verify aggregated metrics
	require.NotNil(t, compacted.Status)
	assert.Equal(t, statusUp, *compacted.Status) // Dominant status (up appears 2 times, down appears 1 time)

	require.NotNil(t, compacted.Duration)
	expectedAvg := float32(150.0) // (100 + 200 + 150) / 3
	assert.InDelta(t, expectedAvg, *compacted.Duration, 0.01)

	require.NotNil(t, compacted.DurationMin)
	assert.InDelta(t, float32(100.0), *compacted.DurationMin, 0.01)

	require.NotNil(t, compacted.DurationMax)
	assert.InDelta(t, float32(200.0), *compacted.DurationMax, 0.01)

	// duration_avg for a raw rollup is the mean of the raw durations.
	require.NotNil(t, compacted.DurationAvg)
	assert.InDelta(t, float32(150.0), *compacted.DurationAvg, 0.01)

	require.NotNil(t, compacted.TotalChecks)
	assert.Equal(t, 3, *compacted.TotalChecks)

	require.NotNil(t, compacted.SuccessfulChecks)
	assert.Equal(t, 2, *compacted.SuccessfulChecks)
	// availability_pct is no longer stored; it derives from the counts above
	// (2/3 = 66.67%) at read time.

	// Rollup rows no longer copy the last raw output blob (storage trim): the
	// hour row's output is left empty like day/month rows.
	assert.Empty(t, compacted.Output)

	// Verify worker UID (all same, so should be preserved)
	require.NotNil(t, compacted.WorkerUID)
	assert.Equal(t, workerUID, *compacted.WorkerUID)
}

// TestAggregateResults_DurationAvgWeighted verifies that rolling up already
// aggregated children (hour → day) computes duration_avg as a total_checks
// weighted mean of the children's own duration_avg, not a plain average.
func TestAggregateResults_DurationAvgWeighted(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	orgUID := testOrgUID
	checkUID := testCheckUID
	region := testRegion
	periodStart := time.Date(2025, 12, 19, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(24 * time.Hour)

	statusUp := int(models.ResultStatusUp)

	// Child A: avg 100ms over 90 checks. Child B: avg 200ms over 10 checks.
	// Weighted mean = (100*90 + 200*10) / (90+10) = 11000/100 = 110ms.
	mkChild := func(avg float32, total int, start time.Time) *models.Result {
		dMin := avg
		dMax := avg
		dP95 := avg
		dAvg := avg
		tc := total
		sc := total

		return &models.Result{
			UID: uuid.Must(uuid.NewV7()).String(), OrganizationUID: orgUID, CheckUID: checkUID,
			Region: &region, PeriodType: "hour", Status: &statusUp,
			Duration: &avg, DurationMin: &dMin, DurationMax: &dMax, DurationP95: &dP95, DurationAvg: &dAvg,
			TotalChecks: &tc, SuccessfulChecks: &sc,
			PeriodStart: start, Output: models.JSONMap{},
		}
	}

	results := []*models.Result{
		mkChild(100.0, 90, periodStart.Add(1*time.Hour)),
		mkChild(200.0, 10, periodStart.Add(2*time.Hour)),
	}

	compacted := aggregateResults(results, "day", periodStart, periodEnd)

	r.NotNil(compacted.DurationAvg)
	r.InDelta(float32(110.0), *compacted.DurationAvg, 0.01)
}

// TestAggregateResults_DurationAvgNilWhenNoDurations verifies that a bucket with
// no measured durations leaves duration_avg nil.
func TestAggregateResults_DurationAvgNilWhenNoDurations(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	orgUID := testOrgUID
	checkUID := testCheckUID
	region := testRegion
	periodStart := time.Date(2025, 12, 19, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(24 * time.Hour)

	statusUp := int(models.ResultStatusUp)

	// Aggregated children that never carried a duration_avg (e.g. all checks
	// produced no duration). DurationMin is set so isRawData stays false.
	mkChild := func(total int, start time.Time) *models.Result {
		dMin := float32(0)
		tc := total
		sc := total

		return &models.Result{
			UID: uuid.Must(uuid.NewV7()).String(), OrganizationUID: orgUID, CheckUID: checkUID,
			Region: &region, PeriodType: "hour", Status: &statusUp,
			DurationMin: &dMin, DurationAvg: nil,
			TotalChecks: &tc, SuccessfulChecks: &sc,
			PeriodStart: start, Output: models.JSONMap{},
		}
	}

	results := []*models.Result{
		mkChild(5, periodStart.Add(1*time.Hour)),
		mkChild(5, periodStart.Add(2*time.Hour)),
	}

	compacted := aggregateResults(results, "day", periodStart, periodEnd)

	r.Nil(compacted.DurationAvg, "duration_avg must be nil when no child carries it")
}

func TestAggregateResults_MultipleWorkers(t *testing.T) {
	t.Parallel()

	orgUID := testOrgUID
	checkUID := testCheckUID
	region := testRegion
	periodStart := time.Date(2025, 12, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2025, 12, 19, 12, 59, 59, 999000000, time.UTC)

	statusUp := int(models.ResultStatusUp)
	duration := float32(100.0)
	worker1 := testWorkerUID
	worker2 := "worker-2"

	results := []*models.Result{
		{
			UID:             "result-1",
			OrganizationUID: orgUID,
			CheckUID:        checkUID,
			Region:          &region,
			WorkerUID:       &worker1,
			Status:          &statusUp,
			Duration:        &duration,
			PeriodStart:     periodStart,
			Output:          models.JSONMap{},
		},
		{
			UID:             "result-2",
			OrganizationUID: orgUID,
			CheckUID:        checkUID,
			Region:          &region,
			WorkerUID:       &worker2, // Different worker
			Status:          &statusUp,
			Duration:        &duration,
			PeriodStart:     periodStart,
			Output:          models.JSONMap{},
		},
	}

	compacted := aggregateResults(results, "hour", periodStart, periodEnd)

	// Worker UID should be nil when multiple workers are involved
	assert.Nil(t, compacted.WorkerUID)
}

func TestAggregateResults_StatusPriority(t *testing.T) {
	t.Parallel()

	orgUID := testOrgUID
	checkUID := testCheckUID
	region := testRegion
	periodStart := time.Date(2025, 12, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2025, 12, 19, 12, 59, 59, 999000000, time.UTC)

	tests := []struct {
		name           string
		statuses       []models.ResultStatus
		expectedStatus models.ResultStatus
	}{
		{
			name:           "most frequent status wins (down dominant)",
			statuses:       []models.ResultStatus{models.ResultStatusUp, models.ResultStatusDown, models.ResultStatusDown},
			expectedStatus: models.ResultStatusDown,
		},
		{
			name:           "tie broken by higher status (error over up)",
			statuses:       []models.ResultStatus{models.ResultStatusUp, models.ResultStatusError},
			expectedStatus: models.ResultStatusError,
		},
		{
			name:           "up is dominant when most frequent",
			statuses:       []models.ResultStatus{models.ResultStatusUp, models.ResultStatusDown, models.ResultStatusUp},
			expectedStatus: models.ResultStatusUp,
		},
		{
			name:           "all up",
			statuses:       []models.ResultStatus{models.ResultStatusUp, models.ResultStatusUp, models.ResultStatusUp},
			expectedStatus: models.ResultStatusUp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			results := make([]*models.Result, 0, len(tt.statuses))
			duration := float32(100.0)

			for i, status := range tt.statuses {
				statusInt := int(status)
				results = append(results, &models.Result{
					UID:             string(rune('a' + i)),
					OrganizationUID: orgUID,
					CheckUID:        checkUID,
					Region:          &region,
					Status:          &statusInt,
					Duration:        &duration,
					PeriodStart:     periodStart,
					Output:          models.JSONMap{},
				})
			}

			compacted := aggregateResults(results, "hour", periodStart, periodEnd)

			require.NotNil(t, compacted.Status)
			assert.Equal(t, int(tt.expectedStatus), *compacted.Status)
		})
	}
}

func TestAggregateResults_ExcludesNonDataStatuses(t *testing.T) {
	t.Parallel()

	orgUID := testOrgUID
	checkUID := testCheckUID
	region := testRegion
	periodStart := time.Date(2025, 12, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2025, 12, 19, 12, 59, 59, 999000000, time.UTC)

	statusUp := int(models.ResultStatusUp)
	statusDown := int(models.ResultStatusDown)
	statusRunning := int(models.ResultStatusRunning)
	statusInitial := int(models.ResultStatusCreated)
	duration := float32(100.0)
	workerUID := testWorkerUID

	// Mix of data and non-data statuses: 2 UP, 1 DOWN, 1 RUNNING, 1 INITIAL
	// Only the 3 data results (2 UP + 1 DOWN) should be counted
	results := []*models.Result{
		{
			UID: "result-1", OrganizationUID: orgUID, CheckUID: checkUID, Region: &region,
			WorkerUID: &workerUID, Status: &statusUp, Duration: &duration,
			PeriodStart: periodStart.Add(5 * time.Minute), Output: models.JSONMap{},
		},
		{
			UID: "result-2", OrganizationUID: orgUID, CheckUID: checkUID, Region: &region,
			WorkerUID: &workerUID, Status: &statusRunning, Duration: &duration,
			PeriodStart: periodStart.Add(10 * time.Minute), Output: models.JSONMap{},
		},
		{
			UID: "result-3", OrganizationUID: orgUID, CheckUID: checkUID, Region: &region,
			WorkerUID: &workerUID, Status: &statusUp, Duration: &duration,
			PeriodStart: periodStart.Add(15 * time.Minute), Output: models.JSONMap{},
		},
		{
			UID: "result-4", OrganizationUID: orgUID, CheckUID: checkUID, Region: &region,
			WorkerUID: &workerUID, Status: &statusInitial, Duration: &duration,
			PeriodStart: periodStart.Add(20 * time.Minute), Output: models.JSONMap{},
		},
		{
			UID: "result-5", OrganizationUID: orgUID, CheckUID: checkUID, Region: &region,
			WorkerUID: &workerUID, Status: &statusDown, Duration: &duration,
			PeriodStart: periodStart.Add(25 * time.Minute), Output: models.JSONMap{},
		},
	}

	compacted := aggregateResults(results, "hour", periodStart, periodEnd)

	// Only 3 data results should be counted (2 UP + 1 DOWN)
	require.NotNil(t, compacted.TotalChecks)
	assert.Equal(t, 3, *compacted.TotalChecks)

	require.NotNil(t, compacted.SuccessfulChecks)
	assert.Equal(t, 2, *compacted.SuccessfulChecks)
	// Availability (2/3 = 66.67%) is derived from these counts at read time and
	// is no longer stored on the row.
}

func TestAggregateMetrics_Min(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{Metrics: models.JSONMap{"response_min": 100.0}},
		{Metrics: models.JSONMap{"response_min": 50.0}},
		{Metrics: models.JSONMap{"response_min": 75.0}},
	}

	compacted := aggregateMetrics(results)

	require.Contains(t, compacted, "response_min")
	assert.InDelta(t, 50.0, compacted["response_min"], 0.01)
}

func TestAggregateMetrics_Max(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{Metrics: models.JSONMap{"response_max": 100.0}},
		{Metrics: models.JSONMap{"response_max": 200.0}},
		{Metrics: models.JSONMap{"response_max": 150.0}},
	}

	compacted := aggregateMetrics(results)

	require.Contains(t, compacted, "response_max")
	assert.InDelta(t, 200.0, compacted["response_max"], 0.01)
}

func TestAggregateMetrics_Avg(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{Metrics: models.JSONMap{"response_avg": 100.0}},
		{Metrics: models.JSONMap{"response_avg": 200.0}},
		{Metrics: models.JSONMap{"response_avg": 150.0}},
	}

	compacted := aggregateMetrics(results)

	require.Contains(t, compacted, "response_avg")
	assert.InDelta(t, 150.0, compacted["response_avg"], 0.01) // (100 + 200 + 150) / 3
}

func TestAggregateMetrics_Pct(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{Metrics: models.JSONMap{"success_pct": 95.0}},
		{Metrics: models.JSONMap{"success_pct": 98.0}},
		{Metrics: models.JSONMap{"success_pct": 97.0}},
	}

	compacted := aggregateMetrics(results)

	require.Contains(t, compacted, "success_pct")
	assert.InDelta(t, 96.67, compacted["success_pct"], 0.01) // (95 + 98 + 97) / 3
}

func TestAggregateMetrics_Rte(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{Metrics: models.JSONMap{"error_rte": 0.01}},
		{Metrics: models.JSONMap{"error_rte": 0.02}},
		{Metrics: models.JSONMap{"error_rte": 0.015}},
	}

	compacted := aggregateMetrics(results)

	require.Contains(t, compacted, "error_rte")
	assert.InDelta(t, 0.015, compacted["error_rte"], 0.001) // (0.01 + 0.02 + 0.015) / 3
}

func TestAggregateMetrics_Sum(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{Metrics: models.JSONMap{"bytes_sum": 1000.0}},
		{Metrics: models.JSONMap{"bytes_sum": 2000.0}},
		{Metrics: models.JSONMap{"bytes_sum": 1500.0}},
	}

	compacted := aggregateMetrics(results)

	require.Contains(t, compacted, "bytes_sum")
	assert.InDelta(t, 4500.0, compacted["bytes_sum"], 0.01) // 1000 + 2000 + 1500
}

func TestAggregateMetrics_Cnt(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{Metrics: models.JSONMap{"requests_cnt": int64(100)}},
		{Metrics: models.JSONMap{"requests_cnt": int64(200)}},
		{Metrics: models.JSONMap{"requests_cnt": int64(150)}},
	}

	compacted := aggregateMetrics(results)

	require.Contains(t, compacted, "requests_cnt")
	assert.Equal(t, int64(450), compacted["requests_cnt"]) // 100 + 200 + 150
}

func TestAggregateMetrics_Val_Strings(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{Metrics: models.JSONMap{"status_val": "200"}},
		{Metrics: models.JSONMap{"status_val": "200"}},
		{Metrics: models.JSONMap{"status_val": "404"}},
	}

	compacted := aggregateMetrics(results)

	require.Contains(t, compacted, "status_val")
	counts, ok := compacted["status_val"].(map[string]int64)
	require.True(t, ok)
	assert.Equal(t, int64(2), counts["200"])
	assert.Equal(t, int64(1), counts["404"])
}

func TestAggregateMetrics_Val_Maps(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{Metrics: models.JSONMap{"status_val": map[string]any{"200": 100, "404": 2}}},
		{Metrics: models.JSONMap{"status_val": map[string]any{"200": 50, "500": 1}}},
	}

	compacted := aggregateMetrics(results)

	require.Contains(t, compacted, "status_val")
	counts, ok := compacted["status_val"].(map[string]int64)
	require.True(t, ok)
	assert.Equal(t, int64(150), counts["200"]) // 100 + 50
	assert.Equal(t, int64(2), counts["404"])
	assert.Equal(t, int64(1), counts["500"])
}

func TestAggregateMetrics_TypeDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metrics  []models.JSONMap
		expected map[string]any
	}{
		{
			name: "int defaults to count",
			metrics: []models.JSONMap{
				{"count": 10},
				{"count": 20},
			},
			expected: map[string]any{
				"count": int64(30),
			},
		},
		{
			name: "float defaults to avg",
			metrics: []models.JSONMap{
				{"latency": 100.0},
				{"latency": 200.0},
			},
			expected: map[string]any{
				"latency": 150.0,
			},
		},
		{
			name: "string defaults to values",
			metrics: []models.JSONMap{
				{"region": "us-east"},
				{"region": "us-west"},
				{"region": "us-east"},
			},
			expected: map[string]any{
				"region": map[string]int64{"us-east": 2, "us-west": 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			results := make([]*models.Result, len(tt.metrics))
			for i, m := range tt.metrics {
				results[i] = &models.Result{Metrics: m}
			}

			compacted := aggregateMetrics(results)

			for key, expectedValue := range tt.expected {
				require.Contains(t, compacted, key)
				assert.Equal(t, expectedValue, compacted[key])
			}
		})
	}
}

func TestAggregateMetrics_MixedTypes(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{
			Metrics: models.JSONMap{
				"response_min": 50.0,
				"response_max": 200.0,
				"requests_cnt": 100,
				"status_val":   "200",
			},
		},
		{
			Metrics: models.JSONMap{
				"response_min": 30.0,
				"response_max": 180.0,
				"requests_cnt": 150,
				"status_val":   "404",
			},
		},
		{
			Metrics: models.JSONMap{
				"response_min": 40.0,
				"response_max": 220.0,
				"requests_cnt": 120,
				"status_val":   "200",
			},
		},
	}

	compacted := aggregateMetrics(results)

	// Verify min
	require.Contains(t, compacted, "response_min")
	assert.InDelta(t, 30.0, compacted["response_min"], 0.01)

	// Verify max
	require.Contains(t, compacted, "response_max")
	assert.InDelta(t, 220.0, compacted["response_max"], 0.01)

	// Verify count
	require.Contains(t, compacted, "requests_cnt")
	assert.Equal(t, int64(370), compacted["requests_cnt"])

	// Verify values
	require.Contains(t, compacted, "status_val")
	counts, ok := compacted["status_val"].(map[string]int64)
	require.True(t, ok)
	assert.Equal(t, int64(2), counts["200"])
	assert.Equal(t, int64(1), counts["404"])
}

func TestAggregateMetrics_EmptyResults(t *testing.T) {
	t.Parallel()

	results := []*models.Result{}
	compacted := aggregateMetrics(results)
	assert.NotNil(t, compacted)
	assert.Empty(t, compacted)
}

func TestAggregateMetrics_NilMetrics(t *testing.T) {
	t.Parallel()

	results := []*models.Result{
		{Metrics: nil},
		{Metrics: nil},
	}
	compacted := aggregateMetrics(results)
	assert.NotNil(t, compacted)
	assert.Empty(t, compacted)
}

func TestAggregateResults_WithMetrics(t *testing.T) {
	t.Parallel()

	orgUID := testOrgUID
	checkUID := testCheckUID
	region := testRegion
	periodStart := time.Date(2025, 12, 19, 12, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2025, 12, 19, 12, 59, 59, 999000000, time.UTC)

	statusUp := int(models.ResultStatusUp)
	duration := float32(100.0)

	results := []*models.Result{
		{
			UID:             "result-1",
			OrganizationUID: orgUID,
			CheckUID:        checkUID,
			Region:          &region,
			Status:          &statusUp,
			Duration:        &duration,
			PeriodStart:     periodStart,
			Output:          models.JSONMap{},
			Metrics: models.JSONMap{
				"response_min": 50.0,
				"response_max": 150.0,
				"requests_cnt": 100,
				"status_val":   "200",
			},
		},
		{
			UID:             "result-2",
			OrganizationUID: orgUID,
			CheckUID:        checkUID,
			Region:          &region,
			Status:          &statusUp,
			Duration:        &duration,
			PeriodStart:     periodStart,
			Output:          models.JSONMap{},
			Metrics: models.JSONMap{
				"response_min": 30.0,
				"response_max": 180.0,
				"requests_cnt": 200,
				"status_val":   "200",
			},
		},
	}

	compacted := aggregateResults(results, "hour", periodStart, periodEnd)

	// Verify metrics are properly aggregated
	require.NotNil(t, compacted.Metrics)
	require.Contains(t, compacted.Metrics, "response_min")
	assert.InDelta(t, 30.0, compacted.Metrics["response_min"], 0.01)

	require.Contains(t, compacted.Metrics, "response_max")
	assert.InDelta(t, 180.0, compacted.Metrics["response_max"], 0.01)

	require.Contains(t, compacted.Metrics, "requests_cnt")
	assert.Equal(t, int64(300), compacted.Metrics["requests_cnt"])

	require.Contains(t, compacted.Metrics, "status_val")
	counts, ok := compacted.Metrics["status_val"].(map[string]int64)
	require.True(t, ok)
	assert.Equal(t, int64(2), counts["200"])
}

// TestAggregatePeriod_KeepsLifecycleMarkerRows exercises aggregatePeriod
// end-to-end against an in-memory SQLite DB (spec 2026-07-08-04): a raw
// window mixing lifecycle-marker rows (created, running) with measurable
// rows (up) must roll up and delete only the measurable rows, while the
// lifecycle-marker rows survive as period_type=raw. Before the fix, the
// UID-collection step deleted every source row indiscriminately.
func TestAggregatePeriod_KeepsLifecycleMarkerRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("agg-lifecycle-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "agg-lifecycle-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// CreateCheck auto-seeds a "created" marker at the real current
	// wall-clock time; remove it so it doesn't leak into (or out of) our
	// synthetic old-hour window below.
	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		Limit:           10,
	})
	r.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, seededUIDs)
		r.NoError(delErr)
	}

	// An old, fully-elapsed hour bucket, older than the default raw retention
	// (24 hours, spec 2026-07-11-16 §4), so it is ready to aggregate.
	baseHour := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Hour)

	newRaw := func(status models.ResultStatus, offset time.Duration) *models.Result {
		res := models.NewResult(org.UID, check.UID, status, 0.1)
		res.PeriodStart = baseHour.Add(offset)
		r.NoError(dbSvc.CreateResult(ctx, res))

		return res
	}

	createdRow := newRaw(models.ResultStatusCreated, 1*time.Minute)
	runningRow := newRaw(models.ResultStatusRunning, 2*time.Minute)
	up1 := newRaw(models.ResultStatusUp, 3*time.Minute)
	up2 := newRaw(models.ResultStatusUp, 4*time.Minute)

	jctx := &jobdef.JobContext{
		DBService: dbSvc,
		Logger:    slog.Default(),
	}

	run := &AggregationJobRun{}
	aggregated, aggErr := run.aggregatePeriod(ctx, jctx, org.UID, periodRaw, periodHour)
	r.NoError(aggErr)
	r.True(aggregated, "expected an hour aggregation to be produced from the raw window")

	// The measurable rows were rolled up and must be deleted.
	_, err = dbSvc.GetResult(ctx, up1.UID)
	r.Error(err, "measurable raw row must be deleted after aggregation")
	_, err = dbSvc.GetResult(ctx, up2.UID)
	r.Error(err, "measurable raw row must be deleted after aggregation")

	// The lifecycle-marker rows must survive untouched, still period_type=raw.
	keptCreated, err := dbSvc.GetResult(ctx, createdRow.UID)
	r.NoError(err, "created marker row must NOT be deleted by aggregation")
	r.Equal(int(models.ResultStatusCreated), *keptCreated.Status)
	r.Equal(periodRaw, keptCreated.PeriodType)

	keptRunning, err := dbSvc.GetResult(ctx, runningRow.UID)
	r.NoError(err, "running marker row must NOT be deleted by aggregation")
	r.Equal(int(models.ResultStatusRunning), *keptRunning.Status)
	r.Equal(periodRaw, keptRunning.PeriodType)

	// The new hour aggregation reflects only the 2 measurable rows — keeping
	// the lifecycle rows changes no rollup numbers.
	hourRows, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		PeriodTypes:     []string{periodHour},
		Limit:           10,
	})
	r.NoError(err)
	r.Len(hourRows.Results, 1)
	r.NotNil(hourRows.Results[0].TotalChecks)
	r.Equal(2, *hourRows.Results[0].TotalChecks)
}

// TestAggregatePeriod_SparseLongPeriodSeries proves — rather than assumes —
// that the raw→hour rollup handles a check whose results are spaced far
// apart in time (e.g. domain's new 336h/2-week period option, spec
// 2026-08-15-07), not just a dense once-a-minute series:
//   - each bucket that actually HAS a raw row aggregates to total_checks=1
//     with the correct status, i.e. nothing divides by an assumed
//     rows-per-bucket count;
//   - the many hour buckets in between that have ZERO raw rows are simply
//     never materialized as rollup rows — aggregatePeriod is driven by
//     findAggregatableResults locating an actual row, not by iterating a
//     fixed grid of buckets, so a sparse series cannot "corrupt" or
//     zero-out buckets that never had data to begin with.
func TestAggregatePeriod_SparseLongPeriodSeries(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("agg-sparse-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "agg-sparse-check", "domain")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// Remove the auto-seeded "created" marker so it doesn't leak into our
	// synthetic old windows below (same cleanup as
	// TestAggregatePeriod_KeepsLifecycleMarkerRows).
	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		Limit:           10,
	})
	r.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, seededUIDs)
		r.NoError(delErr)
	}

	// Two raw executions 336 hours (2 weeks) apart, both older than the
	// default 24h raw-retention floor so both are ready to aggregate.
	firstHour := time.Now().UTC().Add(-400 * time.Hour).Truncate(time.Hour)
	secondHour := firstHour.Add(336 * time.Hour)

	newRaw := func(hour time.Time, status models.ResultStatus) *models.Result {
		res := models.NewResult(org.UID, check.UID, status, 0.1)
		res.PeriodStart = hour.Add(1 * time.Minute)
		r.NoError(dbSvc.CreateResult(ctx, res))

		return res
	}

	first := newRaw(firstHour, models.ResultStatusUp)
	second := newRaw(secondHour, models.ResultStatusDown)

	jctx := &jobdef.JobContext{DBService: dbSvc, Logger: slog.Default()}
	run := &AggregationJobRun{}

	// Aggregate repeatedly: each call finds and compacts exactly one
	// check-region's ready bucket (findAggregatableResults returns ONE
	// pair per call), so two calls are needed to roll up both sparse rows.
	aggregatedOnce, aggErr := run.aggregatePeriod(ctx, jctx, org.UID, periodRaw, periodHour)
	r.NoError(aggErr)
	r.True(aggregatedOnce, "expected the first sparse bucket to aggregate")

	aggregatedTwice, aggErr := run.aggregatePeriod(ctx, jctx, org.UID, periodRaw, periodHour)
	r.NoError(aggErr)
	r.True(aggregatedTwice, "expected the second sparse bucket (2 weeks later) to aggregate")

	// A third call must find nothing left to aggregate — no phantom
	// zero-row buckets were ever created for the ~335 empty hours between.
	aggregatedThrice, aggErr := run.aggregatePeriod(ctx, jctx, org.UID, periodRaw, periodHour)
	r.NoError(aggErr)
	r.False(aggregatedThrice, "no third bucket should exist to aggregate")

	// Both raw rows must have been rolled up and deleted.
	_, err = dbSvc.GetResult(ctx, first.UID)
	r.Error(err, "first sparse raw row must be deleted after aggregation")
	_, err = dbSvc.GetResult(ctx, second.UID)
	r.Error(err, "second sparse raw row must be deleted after aggregation")

	// Exactly two hour rollups exist — one per real data point, zero for
	// the empty hours in between — each with total_checks=1 and the
	// correct dominant status (never a count inflated/deflated by an
	// assumed bucket size).
	hourRows, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		PeriodTypes:     []string{periodHour},
		Limit:           10,
	})
	r.NoError(err)
	r.Len(hourRows.Results, 2, "exactly one hour rollup per real data point, no phantom empty buckets")

	byPeriodStart := map[time.Time]*models.Result{}
	for _, row := range hourRows.Results {
		byPeriodStart[row.PeriodStart.UTC()] = row
	}

	firstBucket, ok := byPeriodStart[firstHour]
	r.True(ok, "expected a rollup at the first bucket's hour")
	r.NotNil(firstBucket.TotalChecks)
	r.Equal(1, *firstBucket.TotalChecks)
	r.NotNil(firstBucket.SuccessfulChecks)
	r.Equal(1, *firstBucket.SuccessfulChecks, "the Up row must count as successful")

	secondBucket, ok := byPeriodStart[secondHour]
	r.True(ok, "expected a rollup at the second bucket's hour, 2 weeks later")
	r.NotNil(secondBucket.TotalChecks)
	r.Equal(1, *secondBucket.TotalChecks)
	r.NotNil(secondBucket.SuccessfulChecks)
	r.Equal(0, *secondBucket.SuccessfulChecks, "the Down row must NOT count as successful")
}
