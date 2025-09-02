# Rename Compaction Job to Aggregation Job

## Overview
Rename the `compaction` job to `aggregation` to better reflect its primary purpose of computing summary statistics and time-series rollups, rather than just storage reduction.

## Rationale

The current "compaction" job name emphasizes storage cleanup, but the job's primary value is:
1. **Aggregating** raw monitoring results into hourly, daily, and monthly summaries
2. **Calculating** derived metrics: averages, percentiles, availability percentages, min/max values
3. **Creating** meaningful time-series data for historical analysis

Storage reduction through deletion of source data is a secondary benefit, not the main purpose.

### Current Implementation Analysis
- **900+ lines** of sophisticated aggregation logic in `job_compaction.go`
- Multiple aggregation strategies for different metric types (min, max, avg, pct, sum, cnt, val)
- Complex percentile calculations and availability computations
- Only **~10 lines** actually handle deletion

### Naming Precedent
- The spec file itself uses "**aggregates** raw check results" in the goal statement
- Time-series databases commonly use "aggregation", "rollup", or "downsampling" for this pattern
- "Compaction" in databases typically refers to LSM tree optimization, not metric summarization

## Proposed Changes

### 1. Job Type Constant
**File**: `back/internal/jobs/jobdef/types.go`

```go
// Before
JobTypeCompaction JobType = "compaction"

// After
JobTypeAggregation JobType = "aggregation"
```

### 2. Job Implementation Files
Rename files to reflect new purpose:

```bash
# Before
back/internal/jobs/jobtypes/job_compaction.go
back/internal/jobs/jobtypes/job_compaction_test.go

# After
back/internal/jobs/jobtypes/job_aggregation.go
back/internal/jobs/jobtypes/job_aggregation_test.go
```

### 3. Type and Function Names
Update all types and functions in the renamed files:

```go
// Before
type CompactionJobDefinition struct{}
type CompactionJobConfig struct{}
type CompactionJobRun struct{}
func (r *CompactionJobRun) compactPeriod(...)

// After
type AggregationJobDefinition struct{}
type AggregationJobConfig struct{}
type AggregationJobRun struct{}
func (r *AggregationJobRun) aggregatePeriod(...)
```

### 4. Registry Registration
**File**: `back/internal/jobs/jobtypes/registry.go`

```go
// Before
registry.Register(&CompactionJobDefinition{})

// After
registry.Register(&AggregationJobDefinition{})
```

### 5. Startup Job Provisioning
**File**: `back/internal/jobs/jobtypes/job_startup.go`

Update references from `JobTypeCompaction` to `JobTypeAggregation` in the startup job that provisions aggregation jobs for each organization.

### 6. Log Messages
Update log messages to use "aggregation" terminology:

```go
// Before
log.InfoContext(ctx, "Starting compaction job", ...)
log.InfoContext(ctx, "Compaction complete", ...)

// After
log.InfoContext(ctx, "Starting aggregation job", ...)
log.InfoContext(ctx, "Aggregation complete", ...)
```

### 7. Comments and Documentation
Update all comments referring to "compaction" to use "aggregation":

```go
// Before
// CompactionJobDefinition is the factory for compaction jobs.
// Run executes the compaction job.

// After
// AggregationJobDefinition is the factory for aggregation jobs.
// Run executes the aggregation job.
```

### 8. Spec File
Move the original spec to reflect the new name:

```bash
# Move
specs/past/2025-12-19-compaction.md
# To
specs/past/2025-12-19-aggregation.md
```

Update references within the spec from "compaction" to "aggregation" where appropriate.

## Migration Strategy

### Database Migration
**No database migration required** - the job type is stored as a string in the `jobs` table, and the system will naturally transition as new jobs are created.

### Existing Jobs
Existing `compaction` jobs in the database will continue to work because:
1. Job execution is based on the handler registry
2. We can temporarily register both job types to the same handler during transition
3. The startup job will provision new jobs with the correct type
4. Old jobs will naturally be replaced as they complete and reschedule

### Transition Code
Add temporary compatibility in `registry.go`:

```go
// Register both names during transition
aggregationDef := &AggregationJobDefinition{}
registry.Register(aggregationDef)

// TODO: Remove after 2026-01-15 (allows existing compaction jobs to complete)
registry.RegisterAlias("compaction", aggregationDef)
```

## Implementation Checklist

- [ ] Update `JobTypeCompaction` to `JobTypeAggregation` in `types.go`
- [ ] Rename `job_compaction.go` to `job_aggregation.go`
- [ ] Rename `job_compaction_test.go` to `job_aggregation_test.go`
- [ ] Update all type names: `CompactionJobDefinition` → `AggregationJobDefinition`
- [ ] Update all type names: `CompactionJobConfig` → `AggregationJobConfig`
- [ ] Update all type names: `CompactionJobRun` → `AggregationJobRun`
- [ ] Rename method: `compactPeriod` → `aggregatePeriod`
- [ ] Update registry registration in `registry.go`
- [ ] Update job provisioning in `job_startup.go`
- [ ] Update all log messages to use "aggregation" terminology
- [ ] Update all comments and documentation
- [ ] Update error messages and variable names
- [ ] Move and update spec file: `2025-12-19-compaction.md` → `2025-12-19-aggregation.md`
- [ ] Run tests to ensure all functionality works: `make test`
- [ ] Verify startup job provisions `aggregation` jobs correctly
- [ ] Update CLAUDE.md if it references compaction jobs

## Testing Requirements

### Verification Tests
- [ ] New `aggregation` jobs are created by startup job
- [ ] Aggregation jobs execute successfully with new name
- [ ] Job reschedules itself with correct job type
- [ ] All existing tests pass with renamed types
- [ ] Log messages use correct terminology

### Manual Testing
1. Start the server
2. Check logs for "Starting aggregation job" messages
3. Verify jobs table contains jobs with `job_type = 'aggregation'`
4. Confirm aggregated results are created correctly
5. Verify old raw data is deleted after aggregation

## Backward Compatibility

### Handling Existing Jobs
During the transition period, existing `compaction` jobs in the database will either:
1. **Option A (Graceful)**: Add a temporary alias in the registry to handle old job type names
2. **Option B (Clean)**: Let old jobs fail and rely on the startup job to provision new ones

**Recommended**: Option A (graceful transition) to avoid any service disruption.

### Timeline
1. **Week 1**: Deploy with alias support for both job types
2. **Week 2-4**: Monitor and verify all organizations have new aggregation jobs
3. **After 30 days**: Remove alias support and drop old compaction jobs from database

## Benefits

1. **Clearer Intent**: Name reflects the primary purpose (summarizing data)
2. **Better Documentation**: Developers immediately understand the job's function
3. **Industry Alignment**: Matches terminology used by other time-series systems
4. **Spec Consistency**: Aligns with the original spec's language ("aggregates raw check results")
5. **Future-Proof**: If we add actual compaction (e.g., database optimization), the name won't be ambiguous

## Notes

- This is purely a **naming change** with no functional modifications
- All aggregation logic remains identical
- Storage reduction (deletion) remains part of the aggregation process
- The term "compaction" may still appear in internal comments where referring to the storage aspect
