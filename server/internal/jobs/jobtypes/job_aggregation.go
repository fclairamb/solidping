package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

var (
	// ErrInvalidSourcePeriod indicates an invalid source period type.
	ErrInvalidSourcePeriod = errors.New("invalid source period")
	// ErrInvalidTargetPeriod indicates an invalid target period type.
	ErrInvalidTargetPeriod = errors.New("invalid target period")
)

// Aggregation period type identifiers (aliases of the canonical models constants).
const (
	periodRaw   = models.PeriodTypeRaw
	periodHour  = models.PeriodTypeHour
	periodDay   = models.PeriodTypeDay
	periodMonth = models.PeriodTypeMonth
)

// AggregationJobDefinition is the factory for aggregation jobs.
type AggregationJobDefinition struct{}

// Type returns the job type for aggregation jobs.
func (d *AggregationJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeAggregation
}

// AggregationJobConfig is the configuration for an aggregation job.
// Empty - job discovers work dynamically.
type AggregationJobConfig struct{}

// CreateJobRun creates a new aggregation job run from the given configuration.
func (d *AggregationJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg AggregationJobConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}

	return &AggregationJobRun{config: cfg}, nil
}

// AggregationJobRun is an executable aggregation job instance.
type AggregationJobRun struct {
	config AggregationJobConfig
}

// Run executes the aggregation job.
func (r *AggregationJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	orgUID := *jctx.OrganizationUID // Never nil for aggregation jobs

	log.InfoContext(ctx, "Starting aggregation job", "organization_uid", orgUID)

	// Define aggregation stages in priority order
	aggregations := []struct {
		sourcePeriod string
		targetPeriod string
	}{
		{periodRaw, periodHour},  // Priority 1: raw → hour
		{periodHour, periodDay},  // Priority 2: hour → day
		{periodDay, periodMonth}, // Priority 3: day → month
	}

	// Try each aggregation stage until one succeeds
	workDone := false
	for i := range aggregations {
		agg := &aggregations[i]
		aggregated, err := r.aggregatePeriod(ctx, jctx, orgUID, agg.sourcePeriod, agg.targetPeriod)
		if err != nil {
			log.ErrorContext(ctx, "Aggregation error", "error", err, "source", agg.sourcePeriod, "target", agg.targetPeriod)
			return jobdef.NewRetryableError(err)
		}
		if aggregated {
			workDone = true
			log.InfoContext(ctx, "Aggregated data", "source", agg.sourcePeriod, "target", agg.targetPeriod)
			break // Process one aggregation per run
		}
	}

	if !workDone {
		log.InfoContext(ctx, "No aggregation work found")
	}

	// Schedule next run
	delay := 1 * time.Hour
	if workDone {
		delay = 0 // Immediate retry if work was done
	}

	scheduledAt := time.Now().Add(delay)
	_, err := jctx.Services.Jobs.CreateJob(ctx, orgUID, string(jobdef.JobTypeAggregation), nil, &jobsvc.JobOptions{
		ScheduledAt: &scheduledAt,
	})
	if err != nil {
		log.ErrorContext(ctx, "Failed to schedule next aggregation job", "error", err)
	}

	return err // CreateJob handles duplicates automatically
}

// aggregatePeriod performs a single aggregation operation for a specific period type.
func (r *AggregationJobRun) aggregatePeriod(
	ctx context.Context,
	jctx *jobdef.JobContext,
	orgUID, sourcePeriod, targetPeriod string,
) (bool, error) {
	log := jctx.Logger

	// 1. Find work: get one check-region pair with data ready to aggregate
	checkUID, region, periodStart, found, err := r.findAggregatableResults(ctx, jctx, orgUID, sourcePeriod)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil // Nothing to aggregate
	}

	log.InfoContext(ctx, "Found data to aggregate",
		"check_uid", checkUID,
		"region", region,
		"period_start", periodStart,
		"source_period", sourcePeriod,
		"target_period", targetPeriod)

	// 2. Calculate period boundaries for the target period
	periodStart, periodEnd, err := calculatePeriodBoundaries(periodStart, targetPeriod)
	if err != nil {
		return false, err
	}

	// 3. Fetch all source results for this check-region-period
	// TODO: Wrap in transaction for atomicity
	filter := models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        []string{checkUID},
		PeriodTypes:      []string{sourcePeriod},
		PeriodStartAfter: &periodStart,
		PeriodEndBefore:  &periodEnd,
	}
	if region != nil {
		filter.Regions = []string{*region}
	}

	resultsResp, err := jctx.DBService.ListResults(ctx, &filter)
	if err != nil {
		return false, fmt.Errorf("failed to list results: %w", err)
	}

	if len(resultsResp.Results) == 0 {
		return false, nil // Nothing to aggregate
	}

	log.InfoContext(ctx, "Aggregating results", "count", len(resultsResp.Results))

	// 4. Aggregate in Go (calculation happens here!)
	aggregated := aggregateResults(resultsResp.Results, targetPeriod, periodStart, periodEnd)

	// 5. Insert aggregated result using existing CreateResult
	// This will automatically set last_for_status=true for the aggregated result's status
	// and clear any existing last_for_status for that check+status combination
	if createErr := jctx.DBService.CreateResult(ctx, aggregated); createErr != nil {
		return false, fmt.Errorf("failed to create aggregated result: %w", createErr)
	}

	// 6. Collect UIDs of source results to delete
	resultUIDs := make([]string, len(resultsResp.Results))
	for i, result := range resultsResp.Results {
		resultUIDs[i] = result.UID
	}

	// 7. Delete source results by their UIDs
	deletedCount, err := jctx.DBService.DeleteResults(ctx, orgUID, resultUIDs)
	if err != nil {
		return false, fmt.Errorf("failed to delete source results: %w", err)
	}

	log.InfoContext(ctx, "Deleted source results", "count", deletedCount)
	log.InfoContext(ctx, "Aggregation complete", "source_count", len(resultsResp.Results), "deleted_count", deletedCount)

	return true, nil
}

// findAggregatableResults finds one check-region pair with data ready to aggregate.
func (r *AggregationJobRun) findAggregatableResults(
	ctx context.Context,
	jctx *jobdef.JobContext,
	orgUID, sourcePeriod string,
) (string, *string, time.Time, bool, error) {
	rawHours, hourDays, dayMonths := retentionFromConfig(jctx)

	// 1. Calculate boundary in Go based on source period type
	boundary, err := calculateAggregationBoundary(sourcePeriod, rawHours, hourDays, dayMonths)
	if err != nil {
		return "", nil, time.Time{}, false, err
	}

	// 2. Use ListResults to find data before the boundary
	// Query with limit to avoid loading too much data
	filter := models.ListResultsFilter{
		OrganizationUID: orgUID,
		PeriodTypes:     []string{sourcePeriod},
		PeriodEndBefore: &boundary, // period_start < boundary
		Limit:           100,       // Get a sample to find check-region pairs
	}

	resultsResp, err := jctx.DBService.ListResults(ctx, &filter)
	if err != nil {
		return "", nil, time.Time{}, false, err
	}

	if len(resultsResp.Results) == 0 {
		return "", nil, time.Time{}, false, nil // Nothing to aggregate
	}

	// 3. Extract first check-region pair
	// We only need one pair per aggregation run
	firstResult := resultsResp.Results[0]

	return firstResult.CheckUID, firstResult.Region, firstResult.PeriodStart, true, nil
}

// retentionFromConfig pulls the per-tier retention values from JobContext.AppConfig,
// falling back to the historical "1/1/1" behavior when the config is absent (e.g.
// in tests that don't wire AppConfig).
func retentionFromConfig(jctx *jobdef.JobContext) (int, int, int) {
	rawHours := 1
	hourDays := 1
	dayMonths := 1

	if jctx == nil || jctx.AppConfig == nil {
		return rawHours, hourDays, dayMonths
	}

	if v := jctx.AppConfig.Aggregation.RetentionRaw; v >= 1 {
		rawHours = v
	}

	if v := jctx.AppConfig.Aggregation.RetentionHour; v >= 1 {
		hourDays = v
	}

	if v := jctx.AppConfig.Aggregation.RetentionDay; v >= 1 {
		dayMonths = v
	}

	return rawHours, hourDays, dayMonths
}

// calculateAggregationBoundary returns the timestamp before which data of
// sourcePeriod is ready to be rolled up. With retention=N, the current
// (incomplete) period plus N-1 prior completed periods are kept; everything
// older is rolled up.
func calculateAggregationBoundary(
	sourcePeriod string,
	retentionRawHours, retentionHourDays, retentionDayMonths int,
) (time.Time, error) {
	now := time.Now().UTC()

	switch sourcePeriod {
	case periodRaw:
		return now.Truncate(time.Hour).
			Add(-time.Duration(retentionRawHours-1) * time.Hour), nil
	case periodHour:
		startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

		return startOfToday.AddDate(0, 0, -(retentionHourDays - 1)), nil
	case periodDay:
		startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

		return startOfMonth.AddDate(0, -(retentionDayMonths - 1), 0), nil
	default:
		return time.Time{}, fmt.Errorf("%w: %s", ErrInvalidSourcePeriod, sourcePeriod)
	}
}

// calculatePeriodBoundaries returns the start and end of the period containing the given timestamp.
func calculatePeriodBoundaries(timestamp time.Time, targetPeriod string) (time.Time, time.Time, error) {
	switch targetPeriod {
	case periodHour:
		start := timestamp.Truncate(time.Hour)
		end := start.Add(time.Hour).Add(-time.Millisecond)
		return start, end, nil
	case periodDay:
		start := time.Date(timestamp.Year(), timestamp.Month(), timestamp.Day(), 0, 0, 0, 0, timestamp.Location())
		end := start.AddDate(0, 0, 1).Add(-time.Millisecond)
		return start, end, nil
	case periodMonth:
		start := time.Date(timestamp.Year(), timestamp.Month(), 1, 0, 0, 0, 0, timestamp.Location())
		end := start.AddDate(0, 1, 0).Add(-time.Millisecond)
		return start, end, nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("%w: %s", ErrInvalidTargetPeriod, targetPeriod)
	}
}

// aggregateResults performs all aggregation calculations in Go.
func aggregateResults(
	results []*models.Result, targetPeriodType string, periodStart, periodEnd time.Time,
) *models.Result {
	if len(results) == 0 {
		panic("cannot aggregate empty results")
	}

	// Initialize aggregation state
	state := initializeAggregationState(results)

	// Process all results
	for _, result := range results {
		if state.isRawData {
			processRawResult(
				result, &state.totalDuration, &state.durations, &state.minDuration, &state.maxDuration,
				&state.maxStatus, state.statusCounts, &state.successCount, &state.totalChecks,
				&state.lastPeriodStart, &state.lastOutput)
		} else {
			processAggregatedResult(
				result, &state.totalDuration, &state.minDuration, &state.maxDuration,
				&state.maxStatus, state.statusCounts, &state.totalChecks, &state.successCount,
				&state.availabilitySum, &state.lastOutput)
		}

		if result.WorkerUID != nil {
			state.workerUIDs[*result.WorkerUID] = true
		}
	}

	// Calculate final metrics
	avgDuration, p95Duration, availabilityPct := calculateFinalMetrics(
		state.isRawData, state.durations, state.totalDuration, state.totalChecks,
		state.successCount, state.availabilitySum, results,
	)

	// Calculate dominant status (most frequent, ties broken by higher status number)
	dominantStatus := calculateDominantStatus(state.statusCounts)

	// Build and return aggregated result
	return buildAggregatedResult(results, targetPeriodType, periodStart, periodEnd, state,
		avgDuration, p95Duration, availabilityPct, dominantStatus)
}

// metricAggregationType represents how a metric should be aggregated.
type metricAggregationType int

const (
	metricAggMin metricAggregationType = iota
	metricAggMax
	metricAggAvg
	metricAggPct
	metricAggRte
	metricAggSum
	metricAggCnt
	metricAggVal
	metricAggDefault
)

// determineMetricAggregation determines how a metric should be aggregated based on its name.
func determineMetricAggregation(name string, value any) metricAggregationType {
	// Check suffixes first
	if strings.HasSuffix(name, "_min") {
		return metricAggMin
	}
	if strings.HasSuffix(name, "_max") {
		return metricAggMax
	}
	if strings.HasSuffix(name, "_avg") {
		return metricAggAvg
	}
	if strings.HasSuffix(name, "_pct") {
		return metricAggPct
	}
	if strings.HasSuffix(name, "_rte") {
		return metricAggRte
	}
	if strings.HasSuffix(name, "_sum") {
		return metricAggSum
	}
	if strings.HasSuffix(name, "_cnt") {
		return metricAggCnt
	}
	if strings.HasSuffix(name, "_val") {
		return metricAggVal
	}

	// No suffix match, use type-based defaults
	switch value.(type) {
	case string:
		return metricAggVal
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return metricAggCnt
	case float32, float64:
		return metricAggAvg
	default:
		return metricAggDefault
	}
}

// aggregateMetrics aggregates metrics from multiple results according to conventions.
func aggregateMetrics(results []*models.Result) models.JSONMap {
	if len(results) == 0 {
		return make(models.JSONMap)
	}

	// Collect all metric values by name
	metricValues := make(map[string][]any)
	for _, result := range results {
		if result.Metrics == nil {
			continue
		}
		for name, value := range result.Metrics {
			metricValues[name] = append(metricValues[name], value)
		}
	}

	if len(metricValues) == 0 {
		return make(models.JSONMap)
	}

	// Aggregate each metric
	aggregated := make(models.JSONMap)
	for name, values := range metricValues {
		if len(values) == 0 {
			continue
		}

		aggType := determineMetricAggregation(name, values[0])
		aggregated[name] = aggregateMetricValues(values, aggType)
	}

	return aggregated
}

// aggregateMetricValues aggregates a slice of metric values according to the aggregation type.
func aggregateMetricValues(values []any, aggType metricAggregationType) any {
	if len(values) == 0 {
		return nil
	}

	switch aggType {
	case metricAggMin:
		return aggregateMin(values)
	case metricAggMax:
		return aggregateMax(values)
	case metricAggAvg:
		return aggregateAvg(values)
	case metricAggPct:
		return aggregatePct(values)
	case metricAggRte:
		return aggregateRte(values)
	case metricAggSum:
		return aggregateSum(values)
	case metricAggCnt:
		return aggregateCount(values)
	case metricAggVal:
		return aggregateValues(values)
	case metricAggDefault:
		// For unknown types, return the last value
		return values[len(values)-1]
	default:
		// Unreachable, but satisfies exhaustive check
		return values[len(values)-1]
	}
}

// aggregateMin returns the minimum numeric value.
func aggregateMin(values []any) any {
	var minVal *float64
	for _, v := range values {
		f := toFloat64(v)
		if f == nil {
			continue
		}
		if minVal == nil || *f < *minVal {
			minVal = f
		}
	}
	if minVal == nil {
		return nil
	}
	return *minVal
}

// aggregateMax returns the maximum numeric value.
func aggregateMax(values []any) any {
	var maxVal *float64
	for _, v := range values {
		f := toFloat64(v)
		if f == nil {
			continue
		}
		if maxVal == nil || *f > *maxVal {
			maxVal = f
		}
	}
	if maxVal == nil {
		return nil
	}
	return *maxVal
}

// aggregateAvg returns the average numeric value.
func aggregateAvg(values []any) any {
	var sum float64
	var count int
	for _, v := range values {
		f := toFloat64(v)
		if f == nil {
			continue
		}
		sum += *f
		count++
	}
	if count == 0 {
		return nil
	}
	return sum / float64(count)
}

// aggregatePct returns the average percentage value.
func aggregatePct(values []any) any {
	return aggregateAvg(values)
}

// aggregateRte returns the average rate value.
func aggregateRte(values []any) any {
	return aggregateAvg(values)
}

// aggregateSum returns the sum of numeric values.
func aggregateSum(values []any) any {
	var sum float64
	for _, v := range values {
		f := toFloat64(v)
		if f == nil {
			continue
		}
		sum += *f
	}
	return sum
}

// aggregateCount returns the sum of integer counts.
func aggregateCount(values []any) any {
	var sum int64
	for _, v := range values {
		i := toInt64(v)
		if i == nil {
			continue
		}
		sum += *i
	}
	return sum
}

// aggregateValues aggregates string values with their counts.
// Input can be either strings or maps of string->count.
func aggregateValues(values []any) any {
	counts := make(map[string]int64)

	for _, v := range values {
		switch val := v.(type) {
		case string:
			counts[val]++
		case map[string]any:
			// Already aggregated values
			for k, count := range val {
				if c := toInt64(count); c != nil {
					counts[k] += *c
				}
			}
		}
	}

	if len(counts) == 0 {
		return nil
	}

	return counts
}

// toFloat64 converts various numeric types to float64.
func toFloat64(v any) *float64 {
	switch val := v.(type) {
	case float64:
		return &val
	case float32:
		f := float64(val)
		return &f
	case int:
		f := float64(val)
		return &f
	case int8:
		f := float64(val)
		return &f
	case int16:
		f := float64(val)
		return &f
	case int32:
		f := float64(val)
		return &f
	case int64:
		f := float64(val)
		return &f
	case uint:
		f := float64(val)
		return &f
	case uint8:
		f := float64(val)
		return &f
	case uint16:
		f := float64(val)
		return &f
	case uint32:
		f := float64(val)
		return &f
	case uint64:
		f := float64(val)
		return &f
	default:
		return nil
	}
}

// toInt64 converts various integer types to int64.
func toInt64(v any) *int64 {
	switch val := v.(type) {
	case int64:
		return &val
	case int:
		i := int64(val)
		return &i
	case int8:
		i := int64(val)
		return &i
	case int16:
		i := int64(val)
		return &i
	case int32:
		i := int64(val)
		return &i
	case uint:
		i := int64(val)
		return &i
	case uint8:
		i := int64(val)
		return &i
	case uint16:
		i := int64(val)
		return &i
	case uint32:
		i := int64(val)
		return &i
	case uint64:
		if val <= 9223372036854775807 { // max int64
			i := int64(val)
			return &i
		}
		return nil
	case float64:
		i := int64(val)
		return &i
	case float32:
		i := int64(val)
		return &i
	default:
		return nil
	}
}

// processRawResult processes a single raw result and updates aggregation state.
func processRawResult(
	result *models.Result,
	totalDuration *float32,
	durations *[]float32,
	minDuration, maxDuration *float32,
	maxStatus *int,
	statusCounts map[int]int,
	successCount, totalChecks *int,
	lastPeriodStart *time.Time,
	lastOutput *models.JSONMap,
) {
	// Skip non-data statuses (initial, running) — they are lifecycle markers, not measurements
	if result.Status != nil &&
		(*result.Status == int(models.ResultStatusRunning) || *result.Status == int(models.ResultStatusCreated)) {
		return
	}

	if result.Duration != nil {
		*totalDuration += *result.Duration
		*durations = append(*durations, *result.Duration)

		if *result.Duration < *minDuration {
			*minDuration = *result.Duration
		}
		if *result.Duration > *maxDuration {
			*maxDuration = *result.Duration
		}
	}

	if result.Status != nil {
		statusCounts[*result.Status]++
		if *result.Status > *maxStatus {
			*maxStatus = *result.Status
		}
		if *result.Status == int(models.ResultStatusUp) {
			*successCount++
		}
	}

	*totalChecks++

	// Track last output (by period_start)
	if result.PeriodStart.After(*lastPeriodStart) {
		*lastPeriodStart = result.PeriodStart
		*lastOutput = result.Output
	}
}

// processAggregatedResult processes a single aggregated result and updates aggregation state.
func processAggregatedResult(
	result *models.Result,
	totalDuration *float32,
	minDuration, maxDuration *float32,
	maxStatus *int,
	statusCounts map[int]int,
	totalChecks, successCount *int,
	availabilitySum *float64,
	lastOutput *models.JSONMap,
) {
	if result.Duration != nil {
		*totalDuration += *result.Duration
	}

	if result.DurationMin != nil && *result.DurationMin < *minDuration {
		*minDuration = *result.DurationMin
	}
	if result.DurationMax != nil && *result.DurationMax > *maxDuration {
		*maxDuration = *result.DurationMax
	}

	if result.Status != nil {
		// For aggregated results, count the status by the number of total checks it represents
		count := 1
		if result.TotalChecks != nil {
			count = *result.TotalChecks
		}
		statusCounts[*result.Status] += count
		if *result.Status > *maxStatus {
			*maxStatus = *result.Status
		}
	}

	if result.TotalChecks != nil {
		*totalChecks += *result.TotalChecks
	}
	if result.SuccessfulChecks != nil {
		*successCount += *result.SuccessfulChecks
	}
	if result.AvailabilityPct != nil {
		*availabilitySum += *result.AvailabilityPct
	}

	// For aggregated data, output is not meaningful
	*lastOutput = make(models.JSONMap)
}

// aggregationState holds the state during result aggregation.
type aggregationState struct {
	isRawData       bool
	totalDuration   float32
	minDuration     float32
	maxDuration     float32
	maxStatus       int
	statusCounts    map[int]int // Track count of each status for dominant calculation
	successCount    int
	totalChecks     int
	durations       []float32
	lastOutput      models.JSONMap
	lastPeriodStart time.Time
	workerUIDs      map[string]bool
	availabilitySum float64
}

// initializeAggregationState initializes the aggregation state from the first result.
func initializeAggregationState(results []*models.Result) *aggregationState {
	state := &aggregationState{
		isRawData:    results[0].DurationMin == nil,
		workerUIDs:   make(map[string]bool),
		statusCounts: make(map[int]int),
	}

	// Initialize based on data type
	if state.isRawData {
		state.durations = make([]float32, 0, len(results))
		if results[0].Duration != nil {
			state.minDuration = *results[0].Duration
			state.maxDuration = *results[0].Duration
		}
	} else {
		if results[0].DurationMin != nil {
			state.minDuration = *results[0].DurationMin
		}
		if results[0].DurationMax != nil {
			state.maxDuration = *results[0].DurationMax
		}
	}

	if results[0].Status != nil {
		state.maxStatus = *results[0].Status
	}

	return state
}

// calculateDominantStatus calculates the most frequent status, with ties broken by higher status number.
func calculateDominantStatus(statusCounts map[int]int) int {
	if len(statusCounts) == 0 {
		return 0
	}

	dominantStatus := 0
	maxCount := 0

	for status, count := range statusCounts {
		// If this status has more occurrences, or same occurrences but higher status number, it wins
		if count > maxCount || (count == maxCount && status > dominantStatus) {
			dominantStatus = status
			maxCount = count
		}
	}

	return dominantStatus
}

// buildAggregatedResult builds the final aggregated result.
func buildAggregatedResult(
	results []*models.Result,
	targetPeriodType string,
	periodStart, periodEnd time.Time,
	state *aggregationState,
	avgDuration, p95Duration float32,
	availabilityPct float64,
	dominantStatus int,
) *models.Result {
	// Determine worker_uid
	var workerUID *string
	if len(state.workerUIDs) == 1 {
		for uid := range state.workerUIDs {
			workerUID = &uid
			break
		}
	}

	// Aggregate metrics according to conventions
	aggregatedMetrics := aggregateMetrics(results)

	totalChecksInt := state.totalChecks
	successfulChecksInt := state.successCount
	lastForStatus := true

	return &models.Result{
		UID:              uuid.Must(uuid.NewV7()).String(),
		OrganizationUID:  results[0].OrganizationUID,
		CheckUID:         results[0].CheckUID,
		PeriodType:       targetPeriodType,
		PeriodStart:      periodStart,
		PeriodEnd:        &periodEnd,
		Region:           results[0].Region,
		WorkerUID:        workerUID,
		Status:           &dominantStatus,
		Duration:         &avgDuration,
		DurationMin:      &state.minDuration,
		DurationMax:      &state.maxDuration,
		DurationP95:      &p95Duration,
		TotalChecks:      &totalChecksInt,
		SuccessfulChecks: &successfulChecksInt,
		AvailabilityPct:  &availabilityPct,
		Output:           state.lastOutput,
		Metrics:          aggregatedMetrics,
		LastForStatus:    &lastForStatus,
		CreatedAt:        time.Now(),
	}
}

// calculateFinalMetrics computes the final aggregated metrics.
func calculateFinalMetrics(
	isRawData bool,
	durations []float32,
	totalDuration float32,
	totalChecks, successCount int,
	availabilitySum float64,
	results []*models.Result,
) (float32, float32, float64) {
	if isRawData {
		return calculateRawMetrics(durations, totalDuration, totalChecks, successCount)
	}
	return calculateAggregatedMetrics(totalDuration, availabilitySum, results)
}

// calculateRawMetrics computes metrics from raw data.
func calculateRawMetrics(
	durations []float32, totalDuration float32, totalChecks, successCount int,
) (float32, float32, float64) {
	var avgDuration, p95Duration float32
	var availabilityPct float64

	if len(durations) > 0 {
		avgDuration = totalDuration / float32(len(durations))

		// Calculate p95
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		p95Index := int(float64(len(durations)) * 0.95)
		if p95Index >= len(durations) {
			p95Index = len(durations) - 1
		}
		p95Duration = durations[p95Index]
	}

	if totalChecks > 0 {
		availabilityPct = float64(successCount) * 100.0 / float64(totalChecks)
	}

	return avgDuration, p95Duration, availabilityPct
}

// calculateAggregatedMetrics computes metrics from aggregated data.
func calculateAggregatedMetrics(
	totalDuration float32, availabilitySum float64, results []*models.Result,
) (float32, float32, float64) {
	var avgDuration, p95Duration float32
	var availabilityPct float64

	if len(results) > 0 {
		avgDuration = totalDuration / float32(len(results))
		availabilityPct = availabilitySum / float64(len(results))
	}

	// For p95, we approximate by averaging the p95 values
	p95Sum := float32(0)
	p95Count := 0
	for _, result := range results {
		if result.DurationP95 != nil {
			p95Sum += *result.DurationP95
			p95Count++
		}
	}
	if p95Count > 0 {
		p95Duration = p95Sum / float32(p95Count)
	}

	return avgDuration, p95Duration, availabilityPct
}
