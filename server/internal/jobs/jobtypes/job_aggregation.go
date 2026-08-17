package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
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

	// Table-wide gauge refresh, throttled process-wide — see
	// maybeSampleResultsRowCount for why this must not run on every org's job.
	maybeSampleResultsRowCount(ctx, jctx)

	// Define aggregation stages in priority order
	aggregations := []struct {
		sourcePeriod string
		targetPeriod string
	}{
		{periodRaw, periodHour},  // Priority 1: raw → hour
		{periodHour, periodDay},  // Priority 2: hour → day
		{periodDay, periodMonth}, // Priority 3: day → month
	}

	// Try each aggregation stage until one succeeds or errors. A stage error is
	// captured (not returned early) so the "schedule next run" below still fires:
	// a poisoned bucket must degrade to "this bucket is stuck", never
	// "aggregation for the org is dead" (spec 2026-07-12-01 §1). Discovery (§2)
	// already excludes the FK-orphan buckets that caused the original halt; this
	// is the backend-independent safety net for any other persistent stage error.
	workDone := false
	var stageErr error
	for i := range aggregations {
		agg := &aggregations[i]
		aggregated, err := r.aggregatePeriod(ctx, jctx, orgUID, agg.sourcePeriod, agg.targetPeriod)
		if err != nil {
			log.ErrorContext(ctx, "Aggregation error", "error", err, "source", agg.sourcePeriod, "target", agg.targetPeriod)
			stageErr = err

			break // Stop after a failing stage, but still schedule the follow-up.
		}
		if aggregated {
			workDone = true
			log.InfoContext(ctx, "Aggregated data", "source", agg.sourcePeriod, "target", agg.targetPeriod)

			break // Process one aggregation per run
		}
	}

	if stageErr == nil && !workDone {
		log.InfoContext(ctx, "No aggregation work found")
	}

	// Schedule next run. Only a run that actually rolled a bucket up retries
	// immediately (delay=0) to drain the backlog; a stage error uses the +1h
	// delay so a persistently poisoned bucket can never spin at delay=0.
	delay := 1 * time.Hour
	if workDone {
		delay = 0
	}

	scheduledAt := time.Now().Add(delay)
	if _, schedErr := jctx.Services.Jobs.CreateJob(
		ctx, orgUID, string(jobdef.JobTypeAggregation), nil, &jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	); schedErr != nil {
		log.ErrorContext(ctx, "Failed to schedule next aggregation job", "error", schedErr)

		// If there is no stage error to surface, propagate the scheduling failure
		// so the run is visibly unhealthy; otherwise the stage error wins below.
		if stageErr == nil {
			return schedErr
		}
	}

	// Surface the stage error (retryable) after the follow-up has been scheduled,
	// so this job run still retries/fails visibly without killing the org's
	// aggregation. CreateJob dedupes against the retry chain automatically.
	if stageErr != nil {
		return jobdef.NewRetryableError(stageErr)
	}

	return nil
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

	// 2. Calculate period boundaries for the target period.
	periodStart, periodEnd, err := calculatePeriodBoundaries(periodStart, targetPeriod)
	if err != nil {
		return false, err
	}

	// 3. Bound the source fetch by the exclusive next-bucket start
	// (periodEnd + 1ms), NOT the inclusive display end (periodEnd itself). The
	// fetch filters period_start < fetchEnd; using periodEnd (e.g. HH:59:59.999)
	// strictly excludes a raw row whose period_start lands exactly on that final
	// millisecond, so work-discovery would re-find that row forever while the
	// targeted fetch returned nothing — stranding it un-compacted (spec
	// 2026-07-22-03 diagnosis). The next bucket's fetch begins at fetchEnd, so
	// no row is ever fetched by two buckets.
	fetchEnd := periodEnd.Add(time.Millisecond)

	filter := models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        []string{checkUID},
		PeriodTypes:      []string{sourcePeriod},
		PeriodStartAfter: &periodStart,
		PeriodEndBefore:  &fetchEnd,
	}
	if region != nil {
		filter.Regions = []string{*region}
	}

	// 4. Compact the bucket transactionally: fetch → aggregate (in Go) → upsert
	// rollup → delete the measurable source rows, committing or rolling back as
	// one unit so a mid-flight failure can never leave a rollup+raw hybrid (spec
	// 2026-07-22-03). The aggregate closure runs inside the transaction and skips
	// lifecycle-marker rows (created, running) via measurableSourceUIDs: those
	// contribute nothing to the aggregate (see processRawResult) and are
	// intentionally preserved (permalink preservation), so they are never among
	// the deleted UIDs. A bucket whose only rows are markers yields no source
	// UIDs; CompactResults then writes nothing and reports Compacted=false.
	outcome, err := jctx.DBService.CompactResults(ctx, &filter,
		func(sources []*models.Result) (*models.Result, []string, error) {
			sourceUIDs := measurableSourceUIDs(sources)
			if len(sourceUIDs) == 0 {
				return nil, nil, nil
			}

			// Idempotent upsert on the bucket key replaces any existing row for
			// (org, check, coalesce(region,''), period_type, period_start), so a
			// re-aggregation yields exactly one row, never a NULL-region duplicate
			// (spec 2026-07-11-16 §3).
			return aggregateResults(sources, targetPeriod, periodStart, periodEnd), sourceUIDs, nil
		})
	if err != nil {
		return false, fmt.Errorf("failed to compact results: %w", err)
	}

	if outcome.Fetched == 0 {
		return false, nil // Nothing to aggregate.
	}

	// Progress guard (spec 2026-07-11-16 §2): a marker-only bucket has nothing to
	// roll up. CompactResults left the markers in place (Compacted=false);
	// reporting work done here would make Run reschedule at delay=0 and
	// re-process this same bucket forever (the poison-pill loop). "Work done" now
	// requires a committed rollup.
	if !outcome.Compacted {
		log.WarnContext(ctx, "Skipping marker-only aggregation bucket (no measurable rows)",
			"check_uid", checkUID, "region", region, "period_start", periodStart,
			"fetched", outcome.Fetched)

		return false, nil
	}

	if outcome.DeletedCount == 0 {
		log.WarnContext(ctx, "Aggregation fetched rows but deleted none",
			"check_uid", checkUID, "region", region, "period_start", periodStart,
			"source_count", outcome.Fetched)
	}

	log.InfoContext(ctx, "Aggregation complete",
		"source_count", outcome.Fetched, "deleted_count", outcome.DeletedCount)

	return true, nil
}

// findAggregatableResults finds one check-region pair with data ready to aggregate.
func (r *AggregationJobRun) findAggregatableResults(
	ctx context.Context,
	jctx *jobdef.JobContext,
	orgUID, sourcePeriod string,
) (string, *string, time.Time, bool, error) {
	rawHours, hourDays, dayMonths := retentionFromConfig(ctx, jctx)

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
		// Never select a bucket on the strength of lifecycle-marker rows
		// (created/running): a marker-only bucket must simply stop being found,
		// so the intentionally preserved markers become inert (spec
		// 2026-07-11-16 §1). The step-3 aggregation fetch reads all statuses, so
		// markers still contribute their skip/keep behavior there.
		ExcludeStatuses: lifecycleMarkerStatuses(),
		// Never select a bucket whose check has been hard-deleted: its rows are
		// FK-orphans and rolling them up fails the INSERT (results.check_uid →
		// checks.uid), which — without the §1 always-schedule guard — permanently
		// halts aggregation for the org. Ordered period_start DESC, the newest
		// orphan is otherwise a deterministic poison pill (spec 2026-07-12-01 §2).
		RequireCheckExists: true,
		Limit:              100, // Get a sample to find check-region pairs
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

// Default per-tier aggregation retention. These replace the historical "1/1/1"
// fallback: an unconfigured deployment keeps a full day of raw data (24 hours)
// before the hourly rollup, a week of hourly rollups, and two months of daily
// rollups — never the most aggressive possible schedule, which was what let a
// lifecycle marker become "aggregatable" within the hour. The raw floor stays
// 24h; the hourly (30→7) and daily (12→2) defaults were tightened by spec
// 2026-07-14-02 to bound how much granular history accumulates by default.
// Operators raise them from the server "Aggregation" settings tab.
const (
	defaultRetentionRawHours  = 24
	defaultRetentionHourDays  = 7
	defaultRetentionDayMonths = 2
)

// lifecycleMarkerStatuses returns the raw statuses (created, running) that must
// not, on their own, make a bucket look aggregatable. A fresh slice each call
// keeps callers free to pass it straight into a filter without aliasing.
func lifecycleMarkerStatuses() []int {
	return []int{int(models.ResultStatusCreated), int(models.ResultStatusRunning)}
}

// measurableSourceUIDs returns the UIDs of the rows that carry real
// measurements, skipping lifecycle-marker rows (created, running) — those are
// intentionally preserved (permalink preservation) and never deleted by a
// rollup.
func measurableSourceUIDs(results []*models.Result) []string {
	uids := make([]string, 0, len(results))
	for _, result := range results {
		if result.Status != nil && models.ResultStatus(*result.Status).IsLifecycleMarker() {
			continue
		}

		uids = append(uids, result.UID)
	}

	return uids
}

// retentionFromConfig resolves the per-tier aggregation retention (raw→hour,
// hour→day, day→month) at job-run time from the performance.* global
// parameters, with precedence: env SP_PERFORMANCE_* → global DB parameter
// (organization_uid IS NULL) → legacy koanf AppConfig.Aggregation.Retention*
// (deprecated, one release) → hardcoded default (24/7/2). Resolving live on
// each run is what lets a server "Aggregation" settings edit take effect on the
// next scheduled run without a restart. The historical 1/1/1 fallback is gone.
func retentionFromConfig(ctx context.Context, jctx *jobdef.JobContext) (int, int, int) {
	var (
		dbSvc systemconfig.RetentionParameterReader
		cfg   *config.Config
	)

	if jctx != nil {
		if jctx.DBService != nil {
			dbSvc = jctx.DBService
		}

		cfg = jctx.AppConfig
	}

	return systemconfig.ResolveAggregationRetention(ctx, dbSvc, cfg)
}

// resolveRetentionTier resolves a single retention tier through the documented
// precedence. Values must be >= 1; an invalid env/DB value logs a warning and
// falls through to the next layer rather than breaking the job.
func resolveRetentionTier(
	ctx context.Context, jctx *jobdef.JobContext,
	key systemconfig.ParameterKey, envVar string, legacy, def int,
) int {
	log := aggregationLogger(jctx)

	// 1. Environment variable (highest precedence).
	if n, ok := retentionFromEnv(ctx, envVar, log); ok {
		return n
	}

	// 2. Global DB parameter (organization_uid IS NULL).
	if n, ok := retentionFromDBParam(ctx, jctx, key, log); ok {
		return n
	}

	// 3. Legacy koanf field (deprecated back-compat, removed a release from now).
	if legacy >= 1 {
		log.WarnContext(ctx, "Using deprecated koanf aggregation retention; set the performance.* parameter instead",
			"key", string(key), "value", legacy)

		return legacy
	}

	// 4. Hardcoded default.
	return def
}

// retentionFromEnv reads a retention tier from an SP_PERFORMANCE_* env var.
// Returns ok=false (and warns) on absent or invalid (< 1 / unparseable) values.
func retentionFromEnv(ctx context.Context, envVar string, log *slog.Logger) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(envVar))
	if raw == "" {
		return 0, false
	}

	if n, err := strconv.Atoi(raw); err == nil && n >= 1 {
		return n, true
	}

	log.WarnContext(ctx, "Ignoring invalid retention env override, falling through",
		"env", envVar, "value", raw)

	return 0, false
}

// retentionFromDBParam reads a retention tier from the global (organization_uid
// IS NULL) system parameter. Returns ok=false on absent/unreadable values, and
// warns (then ok=false) on a present-but-invalid (< 1) value.
func retentionFromDBParam(
	ctx context.Context, jctx *jobdef.JobContext, key systemconfig.ParameterKey, log *slog.Logger,
) (int, bool) {
	if jctx == nil || jctx.DBService == nil {
		return 0, false
	}

	param, err := jctx.DBService.GetSystemParameter(ctx, string(key))
	if err != nil || param == nil {
		return 0, false
	}

	n, ok := parameterInt(param)
	if !ok {
		return 0, false
	}

	if n >= 1 {
		return n, true
	}

	log.WarnContext(ctx, "Ignoring invalid retention parameter, falling through",
		"key", string(key), "value", n)

	return 0, false
}

// parameterInt extracts an integer from a system parameter's stored value,
// which arrives as float64 from encoding/json (or int/string in tests).
func parameterInt(param *models.Parameter) (int, bool) {
	value, ok := param.Value["value"]
	if !ok {
		return 0, false
	}

	switch n := value.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return parsed, true
		}
	}

	return 0, false
}

// aggregationLogger returns the job's logger, falling back to the default so
// retention resolution never panics when a test wires a bare JobContext.
func aggregationLogger(jctx *jobdef.JobContext) *slog.Logger {
	if jctx != nil && jctx.Logger != nil {
		return jctx.Logger
	}

	return slog.Default()
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
				&state.maxStatus, state.statusCounts, &state.successCount, &state.totalChecks)
		} else {
			processAggregatedResult(result, state)
		}

		if result.WorkerUID != nil {
			state.workerUIDs[*result.WorkerUID] = true
		}
	}

	// Calculate final metrics. Availability is no longer stored — it is derived
	// at read time from successful_checks / total_checks.
	avgDuration, p95Duration := calculateFinalMetrics(
		state.isRawData, state.durations, state.totalDuration, results,
	)

	// Calculate dominant status (most frequent, ties broken by higher status number)
	dominantStatus := calculateDominantStatus(state.statusCounts)

	// Resolve the average duration to persist (duration_avg). For raw data it is
	// the mean of the raw durations; for aggregated children it is the
	// total_checks-weighted mean of their duration_avg values. Nil when no
	// measured duration is available for any contributing row.
	durationAvg := resolveDurationAvg(state, avgDuration)

	// Build and return aggregated result
	return buildAggregatedResult(results, targetPeriodType, periodStart, periodEnd, state,
		avgDuration, p95Duration, dominantStatus, durationAvg)
}

// resolveDurationAvg returns the duration_avg to persist for the aggregated row.
// Raw rollups use the directly computed mean (only when at least one raw
// duration was seen); aggregated rollups use the total_checks-weighted mean of
// children's duration_avg. Returns nil when no contributing duration exists.
func resolveDurationAvg(state *aggregationState, rawAvg float32) *float32 {
	if state.isRawData {
		if len(state.durations) == 0 {
			return nil
		}

		avg := rawAvg

		return &avg
	}

	if state.durationAvgWeight == 0 {
		return nil
	}

	avg := float32(state.durationAvgWeightedSum / float64(state.durationAvgWeight))

	return &avg
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
) {
	// Skip non-data statuses (initial, running) — they are lifecycle markers, not measurements
	if result.Status != nil && models.ResultStatus(*result.Status).IsLifecycleMarker() {
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
		// Warning counts as up for availability (Decision A): the target is
		// up, there is just something to report. The window's promotion to
		// the aggregated Degraded status is handled by the dominant-status
		// selection, not here, so availability can be 100% on a Degraded row.
		if models.ResultStatus(*result.Status).CountsAsUp() {
			*successCount++
		}
	}

	*totalChecks++
}

// processAggregatedResult processes a single aggregated result and updates aggregation state.
func processAggregatedResult(
	result *models.Result,
	state *aggregationState,
) {
	totalDuration := &state.totalDuration
	minDuration := &state.minDuration
	maxDuration := &state.maxDuration
	maxStatus := &state.maxStatus
	statusCounts := state.statusCounts
	totalChecks := &state.totalChecks
	successCount := &state.successCount

	if result.Duration != nil {
		*totalDuration += *result.Duration
	}

	// total_checks-weighted mean of children's duration_avg. Children that do
	// not carry a duration_avg (nil) are skipped entirely so they neither pull
	// the average nor add to the weight.
	if result.DurationAvg != nil {
		weight := 1
		if result.TotalChecks != nil {
			weight = *result.TotalChecks
		}

		state.durationAvgWeightedSum += float64(*result.DurationAvg) * float64(weight)
		state.durationAvgWeight += weight
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
}

// aggregationState holds the state during result aggregation.
type aggregationState struct {
	isRawData     bool
	totalDuration float32
	minDuration   float32
	maxDuration   float32
	maxStatus     int
	statusCounts  map[int]int // Track count of each status for dominant calculation
	successCount  int
	totalChecks   int
	durations     []float32
	workerUIDs    map[string]bool

	// durationAvg tracks the total_checks-weighted average duration across
	// children for hour/day/month rollups. For raw data the average is computed
	// directly from the collected durations instead.
	durationAvgWeightedSum float64
	durationAvgWeight      int
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

// calculateDominantStatus selects the status to persist on the aggregated row.
//
//  1. A dominating hard failure (Down/Timeout/Error, by most-frequent with a
//     Severity() tie-break) wins outright — a tie between a failure and
//     anything else resolves to the failure, never to Warning/Degraded.
//  2. Otherwise, if the window contained anything "to report" — a raw Warning
//     (raw rollups) or an already-promoted Degraded child (hour→day→month
//     rollups) — the row is promoted to the aggregated Degraded status
//     ("there was something to report in this window") — Decision D,
//     promotion rule "any warning".
//  3. Otherwise the dominant non-failing status (Up, or a lifecycle marker
//     when that is all there was).
func calculateDominantStatus(statusCounts map[int]int) int {
	if len(statusCounts) == 0 {
		return 0
	}

	dominantStatus := 0
	maxCount := 0
	hasSomethingToReport := false

	for status, count := range statusCounts {
		if status == int(checkerdef.StatusWarning) || status == int(checkerdef.StatusDegraded) {
			hasSomethingToReport = true
		}

		// Most occurrences wins; ties broken by gravity (Severity()), not by
		// raw numeric value — numbers no longer encode severity once
		// Degraded=7 / Warning=8 exist.
		if count > maxCount ||
			(count == maxCount && checkerdef.Status(status).Severity() > checkerdef.Status(dominantStatus).Severity()) {
			dominantStatus = status
			maxCount = count
		}
	}

	// A dominating hard failure always wins — including a tie that resolved to
	// the failure above (Severity ranks failures highest).
	if checkerdef.Status(dominantStatus).Severity() >= checkerdef.StatusDown.Severity() {
		return dominantStatus
	}

	// Non-failing window containing a raw Warning or a Degraded child →
	// promote/carry the aggregated Degraded status.
	if hasSomethingToReport {
		return int(checkerdef.StatusDegraded)
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
	dominantStatus int,
	durationAvg *float32,
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
		DurationAvg:      durationAvg,
		TotalChecks:      &totalChecksInt,
		SuccessfulChecks: &successfulChecksInt,
		// availability_pct is intentionally not stored: it is derived at read time
		// from successful_checks / total_checks (handlers/results, availability,
		// badges).
		// Output is intentionally left nil: rollup rows no longer copy the last
		// raw output blob (it lives 7× longer on an hour row than on the raw
		// rows). Recent-detail views read raw rows for output. Day/month rows
		// already carried no output; hour rows now match.
		Output:    nil,
		Metrics:   aggregatedMetrics,
		CreatedAt: time.Now(),
	}
}

// calculateFinalMetrics computes the final aggregated metrics.
func calculateFinalMetrics(
	isRawData bool,
	durations []float32,
	totalDuration float32,
	results []*models.Result,
) (float32, float32) {
	if isRawData {
		return calculateRawMetrics(durations, totalDuration)
	}
	return calculateAggregatedMetrics(totalDuration, results)
}

// calculateRawMetrics computes duration metrics from raw data. Availability is
// no longer computed here — it is derived at read time from the count columns.
func calculateRawMetrics(
	durations []float32, totalDuration float32,
) (float32, float32) {
	var avgDuration, p95Duration float32

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

	return avgDuration, p95Duration
}

// calculateAggregatedMetrics computes duration metrics from aggregated data.
func calculateAggregatedMetrics(
	totalDuration float32, results []*models.Result,
) (float32, float32) {
	var avgDuration, p95Duration float32

	if len(results) > 0 {
		avgDuration = totalDuration / float32(len(results))
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

	return avgDuration, p95Duration
}
