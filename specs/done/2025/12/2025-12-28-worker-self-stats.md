# Worker Self-Reporting Stats

## Overview

Workers (both jobworker and checkworker) should report their own health and performance metrics to the database using the existing results infrastructure. This replaces the current `Processing stats` log-based reporting (`back/internal/stats/processingStats.go:47`) with persistent, queryable data.

## Implementation Details

### 1. Modify ProcessingStats (`back/internal/stats/processingStats.go`)

Add a callback mechanism to report stats:

```go
type StatsReporter func(stats ReportedStats)

type ReportedStats struct {
    TotalChecks     int
    FailedChecks    int
    AverageDuration float64 // milliseconds
    AverageDelay    float64 // seconds
    FreeRunners     float64
}

type ProcessingStats struct {
    // ... existing fields ...
    reporter    StatsReporter
    freeRunners func() float64 // callback to get current free runners
}
```

Modify `AddMetric()` to call the reporter instead of just logging.

### 2. CheckWorker Changes (`back/internal/checkworker/worker.go`)

#### On Startup (in `Run()` after `registerWorker()`):

Create an internal check if it doesn't exist:

```go
func (r *CheckWorker) createInternalCheck(ctx context.Context) error {
    slug := fmt.Sprintf("int-checks-%s", r.worker.Slug)

    // Check if already exists
    existing, err := r.dbService.GetCheckBySlug(ctx, "default-org-uid", slug)
    if err == nil && existing != nil {
        r.internalCheckUID = existing.UID
        return nil
    }

    check := models.NewCheck(defaultOrgUID, slug, "internal:checkworker")
    check.Name = ptr(fmt.Sprintf("Check Worker: %s", r.worker.Name))
    check.Enabled = false // Don't schedule it as a regular check

    if err := r.dbService.CreateCheck(ctx, check); err != nil {
        return err
    }
    r.internalCheckUID = check.UID
    return nil
}
```

#### Stats Reporter Callback:

```go
func (r *CheckWorker) reportStats(stats stats.ReportedStats) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    status := models.ResultStatusUp
    if stats.TotalChecks == 0 || stats.TotalChecks == stats.FailedChecks {
        status = models.ResultStatusDown
    }

    result := &models.Result{
        UID:             uuid.Must(uuid.NewV7()).String(),
        OrganizationUID: defaultOrgUID,
        CheckUID:        r.internalCheckUID,
        PeriodType:      "raw",
        PeriodStart:     time.Now(),
        WorkerUID:       &r.worker.UID,
        Status:          ptr(int(status)),
        Metrics: models.JSONMap{
            "job_runs":         stats.TotalChecks,
            "free_runners":     stats.FreeRunners,
            "average_duration": stats.AverageDuration,
            "average_delay":    stats.AverageDelay,
        },
        CreatedAt: time.Now(),
    }

    _ = r.dbService.CreateResult(ctx, result)
}
```

#### Wire It Up:

In `NewCheckWorker()`:
```go
stats := stats.NewProcessingStats(time.Minute, time.Minute, logger)
stats.SetReporter(r.reportStats)
stats.SetFreeRunnersFunc(func() float64 {
    return float64(r.availableRunners.Load())
})
```

### 3. JobWorker Changes (`back/internal/jobs/jobworker/worker.go`)

Similar pattern:
- Add `ProcessingStats` field (currently missing)
- Create internal check with slug `int-jobs-{worker-slug}` and type `internal:jobworker`
- Report stats on each job completion

### 4. Get Default Organization UID

Add a helper or use `dbService.GetOrganizationBySlug(ctx, "default")` at startup to get the default org UID.

## Check Configuration

| Field | Value |
|-------|-------|
| `type` | `internal:checkworker` or `internal:jobworker` |
| `slug` | `int-checks-{worker-slug}` or `int-jobs-{worker-slug}` |
| `organization` | `default` |
| `enabled` | `false` (not scheduled as regular check) |

## Result Format

| Field | Value |
|-------|-------|
| `status` | `1` (UP) if at least one check succeeded, `2` (DOWN) if all failed or none executed |
| `period_type` | `raw` |

### Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `job_runs` | integer | Number of jobs/checks executed since last report (reset after each report) |
| `free_runners` | float | Number of available runner slots at report time |
| `average_duration` | float | Average execution duration in milliseconds (EWMA) |
| `average_delay` | float | Average delay between scheduled and actual start time in seconds (EWMA) |

## Files to Modify

1. `back/internal/stats/processingStats.go` - Add reporter callback and reset mechanism
2. `back/internal/checkworker/worker.go` - Create internal check, wire stats reporter
3. `back/internal/jobs/jobworker/worker.go` - Add ProcessingStats, create internal check, wire reporter

## Benefits

- Worker health visible in the same dashboard as regular checks
- Historical performance data for capacity planning
- Alerting on worker issues using existing result-based alerting
- Replaces log-based monitoring with queryable metrics
