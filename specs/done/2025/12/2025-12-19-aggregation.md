# Compaction

## Goal

Create a `results` compactor that aggregates raw check results into hourly, daily, and monthly summaries. This reduces storage requirements while maintaining historical trends.

## Implementation Overview

**Architecture Decision**: All compaction calculations are performed **in Go code**, not in SQL. The database layer provides only minimal list/delete operations.

**File Locations**:
- Job implementation: `back/internal/jobs/jobtypes/job_compaction.go` (contains all aggregation logic)
- Job type constant: Add `JobTypeCompaction = "compaction"` to `back/internal/jobs/jobdef/types.go`
- Registry: Register in `back/internal/jobs/jobtypes/registry.go`
- Database service: Extend `ListResults` and add `DeleteResults` to `back/internal/db/sqlite/sqlite.go` and `back/internal/db/postgres/postgres.go`

**Key Principles**:
- **Single source of truth**: Aggregation logic lives in one place (`job_compaction.go`)
- **Simple database layer**: Only SELECT, INSERT (existing `CreateResult`), and DELETE operations
- **Easy testing**: Go code is easier to unit test than SQL queries
- **Database agnostic**: Minimal database-specific code (just list/delete queries)

## Job Architecture

The compaction process is implemented as a **`compaction` job** using the existing job system:

- **Per-Organization**: Each organization has its own `compaction` job instance
- **Organization Scope**: All compaction queries and operations are scoped to a single organization via `JobContext.OrganizationUID`
- **Job Provisioning**: The `startup` job (`job_startup.go`) is responsible for provisioning compaction jobs
  - On startup, after creating the default organization, check if a compaction job exists
  - If no compaction job exists for the organization, create one with `scheduled_at = now()`
  - Use `JobService.CreateJob()` which provides duplicate detection (won't create duplicates)

### Job Execution Model

The compaction job follows a self-triggering execution pattern using the standard job system:

1. **Single Compaction**: Each job execution performs one compaction operation (one period + check + region combination)
2. **Self-Retriggering**: After completing a compaction, schedule a new job using `JobService.CreateJob()`
   - **If work was done**: Set `scheduled_at` to current time (immediate execution)
   - **If nothing to compact**: Set `scheduled_at` to current time + 1 hour
3. **Continuous Operation**: The duplicate detection in `CreateJob()` ensures only one pending compaction job exists per organization
4. **Error Handling**: Use `jobdef.NewRetryableError()` for transient failures (database locks, etc.) to trigger automatic retry with exponential backoff (1min, 5min, 15min)

## Compaction Process

**IMPORTANT**: Compaction calculations are performed **in Go code**, not in SQL. The database layer provides minimal methods for listing and deleting results.

For each compaction period, the process is:
1. **Identify** expired period data (data that can now be aggregated)
2. **Begin** database transaction using `jctx.DB.BeginTx(ctx, nil)`
3. **Fetch** all result rows from the expired period using extended `ListResults()` method (one check, one region, one period at a time)
4. **Aggregate** the data **in Go** using compaction rules (see below) - calculate averages, percentiles, max status, etc.
5. **Insert** a single new row with the compacted data using existing `CreateResult()` method
6. **Delete** the source rows by their UIDs using new `DeleteResults()` method (passing explicit list of result UIDs)
7. **Commit** the transaction
8. **Schedule next job** using `jctx.Services.Jobs.CreateJob()`

**Architecture Rationale**:
- Go code handles all aggregation logic (single implementation, easier to test)
- Database layer only provides simple list/delete operations
- Existing `CreateResult()` method handles insertion
- Database-specific code is minimal and consistent across SQLite/PostgreSQL

**Important**: Compaction is performed **per region** to preserve regional granularity in aggregated data.

**Note on NULL regions**: If results exist with `region = NULL`, they are treated as a separate group and compacted independently from results with non-NULL regions.

### Job Configuration

The compaction job uses this configuration structure:

```go
type CompactionJobConfig struct {
    // Empty - job discovers work dynamically
    // All operations scoped to JobContext.OrganizationUID
}
```

### Job Execution Algorithm

```go
func (r *CompactionJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
    orgUID := *jctx.OrganizationUID  // Never nil for compaction jobs

    // Define compaction stages in priority order
    compactions := []struct {
        sourcePeriod string
        targetPeriod string
    }{
        {"raw", "hour"},   // Priority 1: raw → hour
        {"hour", "day"},   // Priority 2: hour → day
        {"day", "month"},  // Priority 3: day → month
    }

    // Try each compaction stage until one succeeds
    workDone := false
    for _, c := range compactions {
        compacted, err := r.compactPeriod(ctx, jctx, orgUID, c.sourcePeriod, c.targetPeriod)
        if err != nil {
            return jobdef.NewRetryableError(err)
        }
        if compacted {
            workDone = true
            break  // Process one compaction per run
        }
    }

    // Schedule next run
    delay := 1 * time.Hour
    if workDone {
        delay = 0 // Immediate retry if work was done
    }

    scheduledAt := time.Now().Add(delay)
    _, err := jctx.Services.Jobs.CreateJob(ctx, orgUID, jobdef.JobTypeCompaction, nil, &jobsvc.JobOptions{
        ScheduledAt: &scheduledAt,
    })

    return err  // CreateJob handles duplicates automatically
}
```

## Identifying Results Ready for Compaction

The compactor searches for periods that are now complete and ready to be aggregated.

**Note**: All queries below are scoped to `organization_uid = $orgUID`.

### Strategy: Process One Check-Region Pair at a Time

To avoid overwhelming the database and allow incremental progress, each compaction operation processes **one check-region combination** for **one period**:

1. Find ONE check-region pair that has data ready to compact
2. Compact that check-region's data for that period
3. Return (workDone = true)
4. Next job run will pick up more work

**Rationale**: Compacting by region preserves regional granularity in aggregated data, allowing users to analyze performance trends by region even in historical data.

### Finding Work Using ListResults

**IMPORTANT**: Time boundaries are calculated **in Go code**, not in SQL. This keeps the database layer simple and database-agnostic.

**Helper function** to calculate the boundary timestamp for a given source period:

```go
// calculateCompactionBoundary returns the timestamp before which data is ready to compact
func calculateCompactionBoundary(sourcePeriod string) (time.Time, error) {
    now := time.Now().UTC()

    switch sourcePeriod {
    case "raw":
        // Raw data older than current hour is ready to compact into hourly
        return now.Truncate(time.Hour), nil
    case "hour":
        // Hourly data older than current day is ready to compact into daily
        return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), nil
    case "day":
        // Daily data older than current month is ready to compact into monthly
        return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), nil
    default:
        return time.Time{}, fmt.Errorf("invalid source period: %s", sourcePeriod)
    }
}
```

**Algorithm** to find one check-region pair with data ready to compact:

```go
func (r *CompactionJobRun) findCompactableResults(ctx context.Context, jctx *jobdef.JobContext, orgUID string, sourcePeriod string) (checkUID string, region string, periodStart time.Time, found bool, err error) {
    // 1. Calculate boundary in Go based on source period type
    boundary, err := calculateCompactionBoundary(sourcePeriod)
    if err != nil {
        return "", "", time.Time{}, false, err
    }

    // 2. Use ListResults to find data before the boundary
    // Query with limit to avoid loading too much data
    results, err := jctx.Services.DB.ListResults(ctx, orgUID, db.ListResultsFilter{
        PeriodType:      &sourcePeriod,
        PeriodEndBefore: &boundary,  // period_start < boundary
        Limit:           100,  // Get a sample to find check-region pairs
    })
    if err != nil {
        return "", "", time.Time{}, false, err
    }

    if len(results) == 0 {
        return "", "", time.Time{}, false, nil  // Nothing to compact
    }

    // 3. Extract first check-region pair
    // We only need one pair per compaction run
    firstResult := results[0]
    return firstResult.CheckUID, firstResult.Region, firstResult.PeriodStart, true, nil
}
```

**Key Points**:
- Time boundary calculation happens in Go using `time.Truncate()` and `time.Date()`
- No database-specific date functions (`date_trunc`, `strftime`) needed
- Simple query using extended `ListResults` with a `PeriodEndBefore` filter
- Returns the first check-region pair found
- Database layer stays simple and database-agnostic

### Example: Current time is `2025-12-19T13:15:00Z`

Data ready to compact:

1. **Raw → Hour**: `period_type='raw'` AND `period_start < '2025-12-19T13:00:00Z'`
   - Creates hourly aggregates for completed hours (e.g., 12:00-13:00)

2. **Hour → Day**: `period_type='hour'` AND `period_start < '2025-12-19T00:00:00Z'`
   - Creates daily aggregates for completed days (e.g., 2025-12-18)

3. **Day → Month**: `period_type='day'` AND `period_start < '2025-12-01T00:00:00Z'`
   - Creates monthly aggregates for completed months (e.g., November 2025)

## Compaction Rules

When aggregating multiple result rows into a single compacted row:

### Field Aggregation Rules

| Field | Aggregation Rule | Example |
|-------|------------------|---------|
| `output` | Keep only the **last** value (by period_start) | Discard all but the most recent output |
| `metrics` | Aggregate according to metric type | See metric naming conventions below |
| `period_start` | Set to period boundary start | For hour 12:00-13:00: `2025-12-19T12:00:00Z` |
| `period_end` | Set to period boundary end | For hour 12:00-13:00: `2025-12-19T13:00:00Z` |
| `status` | **Most severe** status wins | `4 (error)` > `3 (timeout)` > `2 (down)` > `1 (up)` |
| `duration` | **Average** of all durations | `(d1 + d2 + ... + dn) / n` |
| `duration_min` | **Minimum** duration observed | `min(d1, d2, ..., dn)` |
| `duration_max` | **Maximum** duration observed | `max(d1, d2, ..., dn)` |
| `duration_p95` | **95th percentile** duration | 95% of requests were faster than this |
| `total_checks` | **Count** of all source rows | `COUNT(*)` |
| `successful_checks` | **Count** of successful checks | `COUNT(*) WHERE status = 1` |
| `availability_pct` | Percentage uptime in period | See calculation below |
| `worker_uid` | Single worker or NULL | Keep if all rows have same worker_uid, else NULL |
| `region` | **Preserved** from source data | All rows in compaction have same region |

### Result Status Values

From `back/internal/db/models/models.go`:
```go
ResultStatusUp      = 1  // Successful check
ResultStatusDown    = 2  // Check failed
ResultStatusTimeout = 3  // Check timed out
ResultStatusError   = 4  // Error during check
```

### Status Priority

The aggregated status uses the most severe status encountered:
- If **any** result has `status = 4` (error) → aggregated status is `4`
- Else if **any** result has `status = 3` (timeout) → aggregated status is `3`
- Else if **any** result has `status = 2` (down) → aggregated status is `2`
- Else (all have `status = 1`) → aggregated status is `1`

**SQL**: `MAX(status)` gives the most severe status

### Availability Calculation

**For raw data** (compacting into hourly):
```sql
availability_pct = (COUNT(*) FILTER (WHERE status = 1)) * 100.0 / COUNT(*)
```

Or in SQLite (no FILTER support):
```sql
availability_pct = (SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END)) * 100.0 / COUNT(*)
```

**For aggregated data** (compacting hourly into daily, or daily into monthly):
```sql
availability_pct = AVG(availability_pct)
```

**Example**: If a day has 24 hourly records with availability [100%, 100%, 95%, 100%, ...], the daily availability is the average of these percentages.

### Metrics Aggregation

Based on `agent_docs/checker_metrics_conventions.md`, metrics are aggregated according to their suffix:

| Metric Suffix | Type | Aggregation Rule |
|---------------|------|------------------|
| `*_min` | float | Keep **minimum** value: `MIN(metrics->>'key')` |
| `*_max` | float | Keep **maximum** value: `MAX(metrics->>'key')` |
| `*_avg` | float | Keep **average** value: `AVG(metrics->>'key')` |
| `*_pct` | float | Keep **average** percentage: `AVG(metrics->>'key')` |
| `*_sum` | float | Keep **sum** of values: `SUM(metrics->>'key')` |
| `*_cnt` | int | Keep **sum** of counts: `SUM(metrics->>'key')` |
| `*_val` | object | Merge value counts: `{"200": 95, "404": 5}` |
| (no suffix) int | int | **Sum** of counts |
| (no suffix) float | float | **Average** of values |

**Implementation Note**: For simplicity in the first version, you may:
1. Keep only the `duration`, `duration_min`, `duration_max`, `duration_p95` fields
2. Drop the `metrics` JSON field when compacting (or keep it as NULL)
3. Add full metrics aggregation in a future iteration

This approach still provides value by:
- Reducing storage (deleting raw rows)
- Preserving key performance metrics
- Maintaining availability trends

### Period Boundaries

Period boundaries are **fixed** to calendar boundaries, not the actual min/max timestamps of the data:

- **Hour**: `YYYY-MM-DDTHH:00:00Z` to `YYYY-MM-DDTHH:59:59.999Z`
- **Day**: `YYYY-MM-DDT00:00:00Z` to `YYYY-MM-DDT23:59:59.999Z`
- **Month**: `YYYY-MM-01T00:00:00Z` to `YYYY-MM-[last-day]T23:59:59.999Z`

This ensures consistent time ranges even if no data exists at the boundaries.

**Helper function** to calculate period boundaries for a given timestamp and target period:

```go
// calculatePeriodBoundaries returns the start and end of the period containing timestamp t
func calculatePeriodBoundaries(t time.Time, targetPeriod string) (start, end time.Time, err error) {
    switch targetPeriod {
    case "hour":
        start = t.Truncate(time.Hour)
        end = start.Add(time.Hour).Add(-time.Millisecond)
        return start, end, nil
    case "day":
        start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
        end = start.AddDate(0, 0, 1).Add(-time.Millisecond)
        return start, end, nil
    case "month":
        start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
        end = start.AddDate(0, 1, 0).Add(-time.Millisecond)
        return start, end, nil
    default:
        return time.Time{}, time.Time{}, fmt.Errorf("invalid target period: %s", targetPeriod)
    }
}
```

## Go Aggregation Implementation

**IMPORTANT**: All aggregation calculations are performed in Go code. The database layer only provides list and delete operations.

### Generic Compaction Flow

Single generic function handles all compaction stages (raw→hour, hour→day, day→month):

```go
func (r *CompactionJobRun) compactPeriod(ctx context.Context, jctx *jobdef.JobContext, orgUID, sourcePeriod, targetPeriod string) (bool, error) {
    // 1. Find work: get one check-region pair with data ready to compact
    checkUID, region, periodStart, found, err := r.findCompactableResults(ctx, jctx, orgUID, sourcePeriod)
    if err != nil || !found {
        return false, err
    }

    // 2. Calculate period boundaries for the target period
    periodStart, periodEnd, err := calculatePeriodBoundaries(periodStart, targetPeriod)
    if err != nil {
        return false, err
    }

    // 3. Begin transaction
    tx, err := jctx.DB.BeginTx(ctx, nil)
    if err != nil {
        return false, err
    }
    defer tx.Rollback()

    // 4. Fetch all source results for this check-region-period using extended ListResults
    results, err := jctx.Services.DB.ListResults(ctx, orgUID, db.ListResultsFilter{
        CheckUID:         &checkUID,
        Region:           &region,
        PeriodType:       &sourcePeriod,
        PeriodStartAfter: &periodStart,
        PeriodEndBefore:  &periodEnd,
    })
    if err != nil {
        return false, err
    }

    if len(results) == 0 {
        return false, nil // Nothing to compact
    }

    // 5. Aggregate in Go (calculation happens here!)
    compacted := aggregateResults(results, targetPeriod, periodStart, periodEnd)

    // 6. Insert compacted result using existing CreateResult
    if err := jctx.Services.DB.CreateResult(ctx, &compacted); err != nil {
        return false, err
    }

    // 7. Collect UIDs of source results to delete
    resultUIDs := make([]string, len(results))
    for i, r := range results {
        resultUIDs[i] = r.UID
    }

    // 8. Delete source results by their UIDs
    if _, err := jctx.Services.DB.DeleteResults(ctx, orgUID, resultUIDs); err != nil {
        return false, err
    }

    // 9. Commit transaction
    return true, tx.Commit()
}

// aggregateResults performs all aggregation calculations in Go
func aggregateResults(results []models.Result, targetPeriodType string, periodStart, periodEnd time.Time) models.Result {
    if len(results) == 0 {
        panic("cannot aggregate empty results")
    }

    // Calculate aggregations
    var (
        totalDuration    int64
        minDuration      = results[0].Duration
        maxDuration      = results[0].Duration
        maxStatus        = results[0].Status
        successCount     int64
        durations        = make([]int64, 0, len(results))
        lastOutput       string
        lastPeriodStart  time.Time
        workerUIDs       = make(map[string]bool)
    )

    for _, r := range results {
        totalDuration += r.Duration
        durations = append(durations, r.Duration)

        if r.Duration < minDuration {
            minDuration = r.Duration
        }
        if r.Duration > maxDuration {
            maxDuration = r.Duration
        }
        if r.Status > maxStatus {
            maxStatus = r.Status
        }
        if r.Status == models.ResultStatusUp {
            successCount++
        }

        // Track last output (by period_start)
        if r.PeriodStart.After(lastPeriodStart) {
            lastPeriodStart = r.PeriodStart
            lastOutput = r.Output
        }

        if r.WorkerUID != nil {
            workerUIDs[*r.WorkerUID] = true
        }
    }

    // Calculate percentiles
    sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
    p95Index := int(float64(len(durations)) * 0.95)
    if p95Index >= len(durations) {
        p95Index = len(durations) - 1
    }

    avgDuration := totalDuration / int64(len(results))
    p95Duration := durations[p95Index]
    availabilityPct := float64(successCount) * 100.0 / float64(len(results))

    // Determine worker_uid
    var workerUID *string
    if len(workerUIDs) == 1 {
        for uid := range workerUIDs {
            workerUID = &uid
            break
        }
    }

    // Build compacted result
    return models.Result{
        UID:              generateUID(),
        OrganizationUID:  results[0].OrganizationUID,
        CheckUID:         results[0].CheckUID,
        PeriodType:       targetPeriodType,
        PeriodStart:      periodStart,
        PeriodEnd:        periodEnd,
        Region:           results[0].Region,
        WorkerUID:        workerUID,
        Status:           maxStatus,
        Duration:         avgDuration,
        DurationMin:      &minDuration,
        DurationMax:      &maxDuration,
        DurationP95:      &p95Duration,
        TotalChecks:      int64(len(results)),
        SuccessfulChecks: successCount,
        AvailabilityPct:  availabilityPct,
        Output:           lastOutput,
        Metrics:          nil, // Simplified: skip metrics aggregation initially
        CreatedAt:        time.Now(),
    }
}
```

### Aggregation Logic Based on Source Period

The `aggregateResults()` function should handle both raw data and already-aggregated data:

**For raw data** (sourcePeriod = "raw"):
- Calculate percentiles from individual durations
- Count successful checks directly
- Keep the last output value

**For already-aggregated data** (sourcePeriod = "hour" or "day"):
- Use `MIN(duration_min)`, `MAX(duration_max)`, `AVG(duration_p95)` from existing aggregates
- Sum `total_checks` and `successful_checks` fields
- Average `availability_pct` values
- Set `output` to empty string (not meaningful for multi-hour/day aggregates)

The function can detect the source type by checking if `DurationMin` is nil (raw data) or populated (aggregated data).

## Database Service Methods

**IMPORTANT**: Keep database-specific code minimal. Extend existing methods instead of creating custom compaction queries.

### Required Changes to `db.Service` Interface

1. **Extend existing `ListResults`** to support filtering by time range:

```go
type ListResultsFilter struct {
    CheckUID         *string    // Existing
    Region           *string    // NEW: filter by region (supports NULL)
    PeriodType       *string    // NEW: filter by period_type ('raw', 'hour', 'day', 'month')
    PeriodStartAfter *time.Time // NEW: filter period_start >= this value
    PeriodEndBefore  *time.Time // NEW: filter period_start < this value
    Limit            int        // Existing
    Offset           int        // Existing
}

// Existing method signature - just extend the filter struct
ListResults(ctx context.Context, orgUID string, filter ListResultsFilter) ([]models.Result, error)
```

**Implementation Notes**:
- `PeriodStartAfter`: Include results where `period_start >= PeriodStartAfter`
- `PeriodEndBefore`: Include results where `period_start < PeriodEndBefore`
- Both filters are optional (nil means no filtering on that dimension)
- When both are provided, they define a time range: `[PeriodStartAfter, PeriodEndBefore)`

2. **Add new `DeleteResults`** method:

```go
// DeleteResults removes results with the specified UIDs
// Returns number of rows deleted
// This is safer than filter-based deletion as it only deletes exactly what was aggregated
DeleteResults(ctx context.Context, orgUID string, resultUIDs []string) (int64, error)
```

**Implementation**:
- Simple `DELETE FROM results WHERE organization_uid = $1 AND uid = ANY($2)` (PostgreSQL)
- For SQLite: `DELETE FROM results WHERE organization_uid = ? AND uid IN (?, ?, ...)`
- More explicit and safer than period-based filtering
- Deletes exactly the results that were aggregated

### Implementation Notes

**SQLite** (`back/internal/db/sqlite/sqlite.go`):
- Extend `ListResults` with additional WHERE clauses for the new filter fields
- Implement `DeleteResults` as a simple DELETE query with WHERE clause matching the filter

**PostgreSQL** (`back/internal/db/postgres/postgres.go`):
- Same approach as SQLite
- Identical query structure since we're not using database-specific date functions

**Key Points**:
- No complex aggregation queries in the database layer
- All aggregation logic lives in `job_compaction.go`
- Database methods are simple CRUD operations
- Easy to test and maintain
- Consistent behavior across SQLite and PostgreSQL

## Testing Strategy

1. **Unit Tests**:
   - Test metric aggregation logic
   - Test period boundary calculations
   - Test status priority logic

2. **Integration Tests**:
   - Insert raw results for multiple checks
   - Run compaction job
   - Verify aggregated results are correct
   - Verify source results are deleted
   - Verify job reschedules correctly

3. **Edge Cases**:
   - Empty periods (no data to compact)
   - Single result in period
   - All failures vs all successes
   - Multiple workers in same region
   - NULL regions (if any exist in the data)
   - Concurrent compaction attempts (database locking)

## Implementation Checklist

- [ ] Add `JobTypeCompaction = "compaction"` to `back/internal/jobs/jobdef/types.go`
- [ ] Create `back/internal/jobs/jobtypes/job_compaction.go` with Go-based aggregation logic
  - [ ] Implement `calculateCompactionBoundary()` helper (no `date_trunc` or `strftime`)
  - [ ] Implement `calculatePeriodBoundaries()` helper
  - [ ] Implement `findCompactableResults()` using `ListResults`
  - [ ] Implement generic `compactPeriod()` function (handles raw→hour, hour→day, day→month)
  - [ ] Implement `aggregateResults()` with logic for both raw and aggregated data
- [ ] Register compaction job in `back/internal/jobs/jobtypes/registry.go`
- [ ] Extend `ListResultsFilter` struct to add:
  - [ ] `Region` field
  - [ ] `PeriodType` field
  - [ ] `PeriodStartAfter` field
  - [ ] `PeriodEndBefore` field
- [ ] Add `DeleteResults(ctx, orgUID, resultUIDs []string)` method to db.Service interface
- [ ] Implement extended `ListResults` in SQLite and PostgreSQL
- [ ] Implement `DeleteResults` by UID list in SQLite and PostgreSQL
- [ ] Update `job_startup.go` to provision compaction jobs
- [ ] Add unit tests for Go aggregation logic (percentiles, status priority, boundary calculations)
- [ ] Add integration tests for full compaction flow
- [ ] Test with both SQLite and PostgreSQL
- [ ] Document any performance considerations
