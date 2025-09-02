# Checkworker Architecture Rework

## Implementation Type
**refactor** - Improving internal architecture for better efficiency and scalability

## Problem Statement

The current checkworker has several inefficiencies:

1. **Redundant database queries**: Each of N workers independently calls `ClaimJobs()` fetching 1 job at a time, resulting in N queries instead of 1
2. **Busy polling**: Workers 1+ poll every 500ms even when no work is available, wasting CPU and database resources
3. **Inconsistent design**: Worker 0 is special (listens for events), workers 1+ poll - creating complex conditional logic
4. **No capacity awareness**: Workers don't coordinate on how many jobs to fetch based on available capacity

## Goals

1. **Efficiency**: Fetch all needed jobs in a single query
2. **Speed**: Support check periods as low as 1 second
3. **Scalability**: Handle large numbers of concurrent checks
4. **Simplicity**: Clean separation between job fetching and job execution

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                         CheckWorker                             │
│                                                                 │
│  ┌─────────────┐         jobsChan             ┌─────────────┐  │
│  │             │  ────────────────────────▶   │             │  │
│  │   Fetcher   │       (unbuffered)           │ Worker Pool │  │
│  │ (1 routine) │                              │(N routines) │  │
│  │             │  ◀────────────────────────   │             │  │
│  └─────────────┘      completionChan          └─────────────┘  │
│        │               (buffered: 1)                │          │
│        │                                            │          │
│        │ wakes on:                                  │          │
│        │ • completionChan                           │          │
│        │ • check.created event                      │          │
│        │ • timeout (FetchMaxAhead)                  │          │
│        │                                            │          │
│        ▼                                            ▼          │
│  ┌─────────────┐                            ┌───────────┐      │
│  │  Database   │                            │ Atomic    │      │
│  │ (ClaimJobs) │                            │ Counter   │      │
│  └─────────────┘                            │(avail.run)│      │
│                                             └───────────┘      │
└─────────────────────────────────────────────────────────────────┘
```

## Implementation

### Struct Changes

```go
type CheckWorker struct {
    // Existing fields (unchanged)
    worker      *models.Worker
    db          *bun.DB
    dbService   db.Service
    checkJobSvc checkjobsvc.Service
    config      *config.Config
    services    *services.Registry
    logger      *slog.Logger
    wg          sync.WaitGroup
    stats       stats.ProcessingStats

    // New fields for channel-based architecture
    poolSize         int                      // Number of worker goroutines
    availableRunners atomic.Int32             // Runners waiting for jobs
    jobsChan         chan *models.CheckJob    // Fetcher → Workers
    completionChan   chan struct{}            // Workers → Fetcher (wake-up signal)
}
```

### NewCheckWorker Changes

```go
func NewCheckWorker(
    database *bun.DB,
    dbService db.Service,
    cfg *config.Config,
    svc *services.Registry,
    checkJobSvc checkjobsvc.Service,
) *CheckWorker {
    logger := slog.Default().With("component", "check_worker")

    poolSize := cfg.Server.CheckWorker.Nb
    if poolSize <= 0 {
        poolSize = 5
    }

    return &CheckWorker{
        db:          database,
        dbService:   dbService,
        config:      cfg,
        services:    svc,
        checkJobSvc: checkJobSvc,
        logger:      logger,
        stats:       stats.NewProcessingStats(time.Minute, time.Minute, logger),
        // New fields
        poolSize:       poolSize,
        jobsChan:       make(chan *models.CheckJob),    // Unbuffered: natural backpressure
        completionChan: make(chan struct{}, 1),         // Buffered: coalesce signals
    }
}
```

### Run Method

```go
func (r *CheckWorker) Run(ctx context.Context) error {
    r.logger.InfoContext(ctx, "Starting check worker")

    // 1. Register worker in database (existing logic, unchanged)
    if err := r.registerWorker(ctx); err != nil {
        return fmt.Errorf("failed to register worker: %w", err)
    }

    r.logger.InfoContext(ctx, "Worker registered",
        "worker_uid", r.worker.UID,
        "worker_slug", r.worker.Slug,
        "pool_size", r.poolSize)

    // 2. Start heartbeat goroutine (existing, unchanged)
    r.wg.Add(1)
    go r.heartbeatLoop(ctx)

    // 3. Start worker pool
    for i := 0; i < r.poolSize; i++ {
        r.wg.Add(1)
        go r.workerLoop(ctx, i)
    }

    // 4. Start fetcher (owns jobsChan, closes it on exit)
    r.wg.Add(1)
    go r.fetcherLoop(ctx)

    // 5. Wait for shutdown signal
    <-ctx.Done()
    r.logger.InfoContext(ctx, "Check worker stopping, waiting for goroutines")

    // 6. Wait for all goroutines to finish
    // Note: fetcherLoop closes jobsChan, which signals runners to exit
    r.wg.Wait()
    r.logger.InfoContext(ctx, "Check worker stopped")

    return ctx.Err()
}
```

### Fetcher Loop (New)

```go
func (r *CheckWorker) fetcherLoop(ctx context.Context) {
    defer r.wg.Done()
    defer close(r.jobsChan) // Signal runners to exit when fetcher stops

    logger := r.logger.With("role", "fetcher")
    logger.InfoContext(ctx, "Fetcher started")
    defer logger.InfoContext(ctx, "Fetcher stopped")

    cfg := r.config.Server.CheckWorker
    checkCreatedChan := r.services.EventNotifier.Listen("check.created")

    for {
        // Check for shutdown
        select {
        case <-ctx.Done():
            return
        default:
        }

        // How many runners are available?
        available := int(r.availableRunners.Load())

        if available > 0 {
            // Fetch jobs for available runners
            jobs, err := r.checkJobSvc.ClaimJobs(
                ctx,
                r.worker.UID,
                r.worker.Region,
                available,           // Fetch exactly as many as we can handle
                cfg.FetchMaxAhead,
            )
            if err != nil {
                if !errors.Is(err, context.Canceled) {
                    logger.ErrorContext(ctx, "Failed to claim jobs", "error", err)
                }
                // Wait briefly before retry on error
                select {
                case <-ctx.Done():
                    return
                case <-time.After(time.Second):
                }
                continue
            }

            // Distribute jobs to runners
            for _, job := range jobs {
                select {
                case r.jobsChan <- job:
                    // Job delivered to a worker
                case <-ctx.Done():
                    // Shutdown: jobs already claimed will have leases expire
                    return
                }
            }

            if len(jobs) > 0 {
                logger.DebugContext(ctx, "Distributed jobs",
                    "count", len(jobs),
                    "available_runners", available)
            }
        } else {
            logger.DebugContext(ctx, "All runners busy, waiting for completion")
        }

        // Wait for next trigger
        select {
        case <-ctx.Done():
            return
        case <-r.completionChan:
            // A worker completed a job, capacity available
        case <-checkCreatedChan:
            // New check created, might be ready to execute
        case <-time.After(cfg.FetchMaxAhead):
            // Periodic check for newly-scheduled jobs
        }
    }
}
```

### Worker Loop (Simplified)

```go
func (r *CheckWorker) workerLoop(ctx context.Context, id int) {
    defer r.wg.Done()

    logger := r.logger.With("worker_id", id)
    logger.InfoContext(ctx, "Worker started")
    defer logger.InfoContext(ctx, "Worker stopped")

    for {
        // Signal: "I'm available for work"
        r.availableRunners.Add(1)

        // Wait for a job
        var job *models.CheckJob
        var ok bool

        select {
        case job, ok = <-r.jobsChan:
            // Got a job or channel was closed
        case <-ctx.Done():
            r.availableRunners.Add(-1)
            return
        }

        // Signal: "I'm now busy"
        r.availableRunners.Add(-1)

        // Channel closed = shutdown
        if !ok {
            return
        }

        // Execute the job (existing logic, unchanged)
        if err := r.executeJob(ctx, logger, job); err != nil {
            logger.ErrorContext(ctx, "Error executing job",
                "error", err,
                "check_uid", job.CheckUID)
        }

        // Signal completion to wake fetcher (non-blocking)
        select {
        case r.completionChan <- struct{}{}:
        default:
            // Channel already has a signal, that's fine
        }
    }
}
```

### Methods to Keep Unchanged

These methods require no changes:
- `registerWorker()` - Worker database registration
- `heartbeatLoop()` - Periodic heartbeat updates
- `updateHeartbeat()` - Single heartbeat update
- `executeJob()` - Job execution with timing, checker invocation, result saving
- `saveResult()` - Save check result to database
- `saveErrorResult()` - Save error result
- `releaseLease()` - Release job lease and reschedule
- `calculateNextScheduledAt()` - Calculate next execution time

### Methods to Delete

- The old `workerLoop()` with its complex polling/event logic

## Shutdown Sequence

The fetcher must stop first to prevent claiming new jobs during shutdown. Runners then complete their in-flight work before exiting.

```
1. Context is canceled (from server.go)
2. fetcherLoop sees ctx.Done() immediately (checked at loop start):
   - Stops claiming new jobs
   - Exits loop
   - defer close(r.jobsChan) executes → signals runners to stop
3. workerLoop instances:
   - Runners waiting on jobsChan: see channel closed, exit
   - Runners waiting on ctx.Done(): exit immediately
   - Runners in executeJob: complete current job (using fallback context),
     then loop back and see closed channel, exit
4. heartbeatLoop sees ctx.Done(), exits
5. Run() proceeds past wg.Wait() (all goroutines finished)
6. Run() returns ctx.Err()
```

**Key guarantee**: The fetcher closes `jobsChan` before exiting, which is the signal for runners to stop. This ensures:
- No new jobs are claimed during shutdown
- In-flight jobs complete with fallback contexts
- Clean drain of the job channel

## Race Condition Analysis

### Counter Accuracy

The atomic counter may briefly show a worker as "available" before it's actually blocked on the channel receive. This is harmless:

1. Worker calls `availableRunners.Add(1)`
2. Fetcher reads counter, sees worker available
3. Worker enters `select` on jobsChan
4. Fetcher sends job to jobsChan
5. Send succeeds (worker is receiving)

If step 4 happens before step 3, the fetcher simply blocks briefly until the worker reaches the receive. The unbuffered channel provides natural synchronization.

### Over-fetching

If fetcher reads `available=3` but only 2 runners are actually ready:
- Fetcher claims 3 jobs from database
- Sends job 1 → delivered immediately
- Sends job 2 → delivered immediately
- Sends job 3 → blocks until a runner finishes and loops back

This is acceptable: the fetcher will eventually deliver all claimed jobs. Jobs are already leased in the database, so no other worker will claim them.

### Completion Signal Coalescing

Multiple runners might complete simultaneously:
- Runner A sends to completionChan → succeeds (buffer was empty)
- Runner B sends to completionChan → drops (buffer full, `default` case)

This is by design: one wake-up is sufficient to trigger a new fetch cycle.

## Benefits Summary

| Aspect | Before | After |
|--------|--------|-------|
| DB queries per cycle | N (one per worker) | 1 (batch fetch) |
| CPU when idle | Polling every 500ms | Zero (channel wait) |
| Code complexity | Worker 0 special case | Uniform worker logic |
| Responsiveness | 500ms poll interval | Immediate (channel) |
| Capacity awareness | None | Exact capacity matching |
| Lines of code | ~90 (workerLoop) | ~50 (fetcher + worker) |

## Testing Strategy

1. **Existing tests**: All tests in `worker_test.go` should pass without modification
2. **New unit tests**:
   - Test atomic counter accuracy under concurrent load
   - Test graceful shutdown with in-flight jobs
   - Test completion signal coalescing
3. **Integration tests**:
   - Verify batch fetching reduces DB queries
   - Verify sub-second check periods work correctly
4. **Load tests**:
   - Measure throughput with 100+ concurrent checks
   - Verify no goroutine leaks under sustained load

## Acceptance Criteria

- [ ] Single fetcher goroutine fetches jobs in batches
- [ ] Runners wait on channel with zero CPU usage when idle
- [ ] Completion signals immediately wake fetcher
- [ ] `check.created` events immediately wake fetcher
- [ ] Atomic counter accurately tracks available runners
- [ ] All existing tests pass
- [ ] Graceful shutdown completes in-flight jobs
- [ ] No goroutine leaks
- [ ] Linter passes
- [ ] Build succeeds

## Implementation Notes

_To be filled during implementation_
