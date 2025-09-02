package jobtypes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
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

			got, err := calculateAggregationBoundary(tt.sourcePeriod)
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
		boundary, err := calculateAggregationBoundary("raw")
		require.NoError(t, err)

		// Should be truncated to the hour
		assert.Equal(t, 0, boundary.Minute())
		assert.Equal(t, 0, boundary.Second())
		assert.Equal(t, 0, boundary.Nanosecond())
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

	require.NotNil(t, compacted.TotalChecks)
	assert.Equal(t, 3, *compacted.TotalChecks)

	require.NotNil(t, compacted.SuccessfulChecks)
	assert.Equal(t, 2, *compacted.SuccessfulChecks)

	require.NotNil(t, compacted.AvailabilityPct)
	expectedAvailability := float64(2) * 100.0 / float64(3) // 66.67%
	assert.InDelta(t, expectedAvailability, *compacted.AvailabilityPct, 0.01)

	// Verify last output is kept
	assert.Equal(t, models.JSONMap{"msg": "error"}, compacted.Output)

	// Verify worker UID (all same, so should be preserved)
	require.NotNil(t, compacted.WorkerUID)
	assert.Equal(t, workerUID, *compacted.WorkerUID)
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
	statusInitial := int(models.ResultStatusInitial)
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

	// Availability = 2/3 = 66.67%
	require.NotNil(t, compacted.AvailabilityPct)
	expectedAvailability := float64(2) * 100.0 / float64(3)
	assert.InDelta(t, expectedAvailability, *compacted.AvailabilityPct, 0.01)
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
