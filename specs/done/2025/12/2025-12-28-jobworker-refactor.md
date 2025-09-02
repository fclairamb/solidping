# Jobworker Refactoring

## Implementation Type
**chore** - Refactoring internal architecture without changing external behavior

## Summary
Refactor the `jobrunner.Runner` to follow the same architectural pattern as `checkworker.CheckWorker`:
- Single worker instance with multiple internal runners (goroutines)
- Move from `jobrunner` package to a new `jobworker` package

## Current State

### jobrunner.Runner (Current)
- Located in `back/internal/jobs/jobrunner/`
- Creates **N independent Runner instances** (default 2)
- Each runner runs in its own goroutine
- No database registration
- Simple polling with `FOR UPDATE SKIP LOCKED` for job claiming

### checkworker.CheckWorker (Reference Pattern)
- Located in `back/internal/checkworker/`
- Creates **ONE CheckWorker instance** that internally spawns N worker goroutines
- Single database-registered worker identity
- Heartbeat mechanism for worker availability tracking
- Lease-based job distribution

## Requirements

### Functional Requirements
1. Rename package from `jobrunner` to `jobworker`
2. Rename `Runner` to `JobWorker` (matching `CheckWorker` naming)
3. Single `JobWorker` instance per application instead of N independent runners
4. Internal worker pool: spawn N goroutines inside `Run()` method
5. All internal workers share the same `JobWorker` instance
6. Maintain existing job processing logic (panic recovery, retry strategy)

### Non-Functional Requirements
1. Maintain backward compatibility - existing jobs should continue to work
2. No changes to the `Job` model or `jobsvc.Service`
3. Preserve existing retry strategy (exponential backoff: 1min, 5min, 15min)
4. Preserve panic recovery mechanism

## Acceptance Criteria

1. [ ] New `jobworker` package exists at `back/internal/jobs/jobworker/`
2. [ ] `JobWorker` type with single-instance architecture
3. [ ] Internal worker pool spawning N goroutines
4. [ ] Server.go updated to use new `JobWorker` pattern
5. [ ] Old `jobrunner` package removed
6. [ ] All existing tests pass
7. [ ] Linting passes
8. [ ] Build succeeds

## Technical Considerations

### What to Keep
- Job processing logic (`processNext`, `executeWithRecovery`, `handleResult`)
- Panic recovery mechanism
- Retry strategy with exponential backoff
- Integration with `jobsvc.Service`
- Integration with `jobtypes` registry

### What to Change
- Package location: `jobrunner` → `jobworker`
- Type name: `Runner` → `JobWorker`
- Instantiation: N instances → 1 instance with N internal workers
- `Run()` method: simple loop → spawn internal workers + wait

### What NOT to Add (Keeping it Simple)
- No database worker registration (unlike checkworker)
- No heartbeat mechanism (not needed for jobs)
- No lease-based distribution (current `FOR UPDATE SKIP LOCKED` works fine)
- No event-based notifications (polling is sufficient for background jobs)

## Implementation Plan

### Files to Create

1. **`back/internal/jobs/jobworker/worker.go`** - New JobWorker implementation
   - Copy structure from `checkworker/worker.go` as reference
   - Adapt for job processing instead of check processing
   - Single instance with internal worker pool

2. **`back/internal/jobs/jobworker/errors.go`** - Error definitions (copy from jobrunner)

### Files to Modify

1. **`back/internal/app/server.go`** (lines 510-550)
   - Change import from `jobrunner` to `jobworker`
   - Update `startJobWorker()` to instantiate single `JobWorker` instead of N `Runner` instances
   - Follow the exact pattern of `startCheckWorker()`

### Files to Delete

1. **`back/internal/jobs/jobrunner/`** - Entire directory (runner.go, errors.go)

### Detailed Changes

#### 1. New `jobworker/worker.go` Structure

```go
type JobWorker struct {
    db        *bun.DB
    dbService db.Service
    config    *config.Config
    services  *services.Registry
    jobSvc    jobsvc.Service
    logger    *slog.Logger
    wg        sync.WaitGroup  // NEW: for tracking worker goroutines
}

func NewJobWorker(...) *JobWorker  // No runnerID parameter

func (w *JobWorker) Run(ctx context.Context) error {
    // 1. Log startup
    // 2. Start N worker goroutines (from config)
    // 3. Wait for context cancellation
    // 4. Wait for all workers to finish (wg.Wait())
}

func (w *JobWorker) workerLoop(ctx context.Context, runnerID int) {
    // Individual worker loop (moved from current Run())
    // Uses runnerID only for logging
}

// Keep existing methods:
// - processNext()
// - executeWithRecovery()
// - handleResult()
// - createJobContext()
```

#### 2. server.go Changes

**Before:**
```go
for i := 0; i < numRunners; i++ {
    runner := jobrunner.NewRunner(i, ...)
    s.workersWg.Add(1)
    go func(id int, r *jobrunner.Runner) {
        defer s.workersWg.Done()
        r.Run(ctx)
    }(i, runner)
}
```

**After:**
```go
worker := jobworker.NewJobWorker(...)
s.workersWg.Add(1)
go func() {
    defer s.workersWg.Done()
    worker.Run(ctx)
}()
```

### Testing Strategy

1. No existing unit tests for jobrunner to update
2. Manual testing:
   - Start server with `make dev-backend`
   - Create a job (e.g., via check creation which triggers jobs)
   - Verify jobs are processed correctly
3. Run existing test suite: `make test`
4. Verify build: `make build`

### Risk Assessment

**Low Risk:**
- No changes to job processing logic (processNext, handleResult, retry strategy)
- No changes to Job model or jobsvc.Service
- No changes to jobtypes registry
- Only structural refactoring of how workers are spawned

**Verification Points:**
- Jobs continue to be processed
- Retry logic works correctly
- Panic recovery still functional
- Graceful shutdown still works
