# Check Runner Simplification

## Objective
Simplify the check runner architecture from a complex pool-based system to a straightforward sequential execution model.

## Current Architecture (Complex)
The current implementation in `back/internal/checkrunner/runner.go` uses:

1. **Goroutine pool** (`jobChan`, worker routines) - Multiple workers process jobs concurrently from a channel
2. **Batch job fetching** - `processCycle()` claims multiple jobs at once via `ClaimJobs()`
3. **Job distribution** - Jobs sent to worker pool via buffered channel
4. **Smart sleep calculation** - Complex sleep duration logic with early wake-up on job completion
5. **Job completion notifications** - `jobCompletedChan` to interrupt sleep when jobs finish
6. **Dynamic sleep** - Sleep duration calculated based on `FetchMaxAhead` and job completion

Configuration parameters used:
- `cfg.Server.CheckRunner.Concurrency` - Pool size
- `cfg.Server.CheckRunner.FetchNb` - Number of jobs to fetch per cycle
- `cfg.Server.CheckRunner.FetchMaxAhead` - How far ahead to fetch jobs
- `cfg.Server.CheckRunner.LeaseDuration` - Job lease timeout

## Desired Architecture (Simplified with Concurrent Workers)
Each runner spawns multiple worker goroutines that independently fetch and execute jobs:

### Worker Types
1. **Worker 0 (Event-Driven)** - Listens to `CheckNotifier.WaitChannel()` for immediate response to new checks
2. **Workers 1-N (Polling)** - Regular polling workers that fetch jobs on a fixed interval

### Worker Behavior
Each worker operates in a simple loop:
1. **Fetch one job** - Call `ClaimJobs()` with `nbJobs = 1`
2. **Execute it immediately** - Call `executeJob()`
3. **Sleep/Wait** - Worker 0 waits on notification channel, others sleep briefly
4. **Repeat** - Loop back to step 1

### What to Remove
- Job distribution channel (`jobChan`) - Workers fetch directly
- Job completion channel (`jobCompletedChan`) - Not needed
- Batch fetching logic in `processCycle()` - Each worker fetches one job
- Smart sleep calculation - Simple fixed sleep or channel wait
- Configuration: `FetchNb` (keep `Concurrency`, `FetchMaxAhead`, `LeaseDuration`)

### What to Keep
- Worker registration and heartbeat
- Job claiming via `checkJobSvc.ClaimJobs()`
- Job execution logic in `executeJob()`
- Result saving and lease release
- Context-based cancellation
- Logging
- `sync.WaitGroup` for coordinating worker shutdown
- Configuration: `Concurrency` (number of workers)

### Expected Behavior
```go
func (r *Runner) Run(ctx context.Context) error {
    // 1. Register worker
    // 2. Start heartbeat goroutine
    // 3. Start worker goroutines
    poolSize := r.config.Server.CheckRunner.Concurrency
    for i := 0; i < poolSize; i++ {
        r.wg.Add(1)
        go r.workerLoop(ctx, i)
    }

    // 4. Wait for shutdown
    <-ctx.Done()
    r.wg.Wait()
    return ctx.Err()
}

func (r *Runner) workerLoop(ctx context.Context, workerID int) {
    defer r.wg.Done()

    for {
        select {
        case <-ctx.Done():
            return
        default:
            // Fetch one job
            jobs, err := r.checkJobSvc.ClaimJobs(...)

            if len(jobs) == 0 {
                // No jobs available
                if workerID == 0 {
                    // Worker 0: wait for notification
                    select {
                    case <-ctx.Done():
                        return
                    case <-r.services.CheckNotifier.WaitChannel():
                        continue
                    case <-time.After(minSleepDuration):
                        continue
                    }
                } else {
                    // Other workers: simple sleep
                    time.Sleep(minSleepDuration)
                    continue
                }
            }

            // Execute job
            r.executeJob(ctx, jobs[0])
        }
    }
}
```

### Benefits
- **Simple per-worker logic** - Each worker is independent, no coordination needed
- **Event-driven responsiveness** - Worker 0 responds immediately to new checks
- **Controlled concurrency** - `Concurrency` config limits parallel execution
- **No complex channels** - Workers fetch directly, no job distribution
- **Easier debugging** - Each worker has clear, linear execution flow
- **Better resource utilization** - Multiple workers can process jobs in parallel

### Scaling
- **Internal concurrency**: Set `Concurrency` config (default: 5)
- **External concurrency**: Run multiple runner instances (processes/containers)
- **Hybrid approach**: Multiple instances × workers per instance = total concurrency
