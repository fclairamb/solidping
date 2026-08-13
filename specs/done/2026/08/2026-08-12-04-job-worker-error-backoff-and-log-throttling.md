---
model: opus
effort: high
---

# The job worker spins on infrastructure errors and filled a 460 GB disk

## Problem

On 2026-08-11 a `solidping-e2e serve` process was left running after a Claude
Code session ended. Its two job runners hit a SQLite error, retried it with no
delay, and logged every failure. Seventeen hours later the log file was
**201 GB** and growing at **~30 GB/h**; the host disk was at zero free bytes,
to the point where unrelated tooling failed with `ENOSPC` on `mkdir`.

The log was the same two lines, forever:

```
level=WARN  msg="SQL query failed" duration=16.958µs operation=SELECT error="SQL logic error: no such table: jobs (1)"
level=ERROR msg="Error processing job" component=job_worker runner_id=1 error="SQL logic error: no such table: jobs (1)"
```

Note `duration=16.958µs`. Each attempt failed in microseconds, so the loop ran
at CPU speed.

### Why it spins

`GetJobWait` (`server/internal/jobs/jobsvc/service.go:507`) is correct for the
case it was designed for. When there is no work it blocks on the notifier or a
5-minute ticker. But that blocking path is reached **only** for
`sql.ErrNoRows`; every other error returns immediately:

```go
job, err := s.claimNextJob(ctx)
if err == nil {
    return job, nil
}
if !errors.Is(err, sql.ErrNoRows) {
    return nil, err          // instant return
}
```

`workerLoop` (`server/internal/jobs/jobworker/worker.go:112-140`) then calls it
again with nothing in between:

```go
for {
    select {
    case <-ctx.Done():
        return
    default:
        w.availableRunners.Add(1)
        err := w.processNext(ctx, logger)
        w.availableRunners.Add(-1)
        if err != nil {
            if !errors.Is(err, context.Canceled) {
                logger.ErrorContext(ctx, "Error processing job", "error", err)
            }
        }
    }
}
```

"No work to do" blocks. "The database is broken" busy-loops. The default pool
size is 2 (`worker.go:68-70`), so two goroutines each burn a core and emit two
log lines per iteration.

This is a whole class of failure, not one bug. Any error that is both
*persistent* and *fast to produce* — a dropped connection, a permission error,
a missing table, a driver-level failure — turns the worker into a log bomb.
The specific SQLite error that triggered it is the subject of spec
`2026-08-12-05`; this spec is about the loop that amplified it, which would
amplify the next one just as effectively.

## Proposal

Two changes in `workerLoop`, independent of whatever caused the error.

**1. Exponential backoff on consecutive failures.** An error path that returns
instantly must never be retried instantly. Back off from 100 ms to a 30 s cap,
with jitter to avoid two runners locking into step, and reset to zero on the
first success. The `ctx.Done()` case must remain responsive while sleeping, so
the wait is a `select`, never a bare `time.Sleep`.

Backoff alone takes 30 GB/h down to a few KB/h.

**2. Collapse repeated identical errors.** Log the first occurrence, then only
at exponentially spaced counts (2nd, 4th, 8th, …), carrying a `consecutive`
attribute. On recovery, emit one summary line with the total and the duration
of the outage. Ten thousand identical lines carry no more information than the
first — but the fact that it lasted seventeen hours is information, and the
current code loses it in the noise.

Deliberately out of scope: changing `GetJobWait`'s error semantics, and
deciding whether a given error should be fatal at all. Both belong to spec
`2026-08-12-05`. This spec makes the worker survivable under *any* persistent
error; that one makes the right errors stop the process entirely. They are
complementary — backoff without fail-fast means a silent, useless worker;
fail-fast without backoff means the next unclassified error still floods.

## Acceptance criteria

- With `processNext` failing persistently and instantly, the retry interval
  reaches the 30 s cap and stays there; sustained log output is bounded well
  under 1 MB/h with the default pool size.
- The first failure is still logged immediately — backoff must not delay the
  operator's first signal.
- A single transient failure followed by success does not delay the next job by
  more than the minimum backoff, and the counter resets: a later isolated
  failure is again logged immediately.
- Recovery emits exactly one summary line with the consecutive-failure count
  and outage duration.
- A worker sleeping at the 30 s cap still returns from `workerLoop` promptly on
  context cancellation (assert shutdown completes in well under the cap).
- `availableRunners` is not left incremented while a runner is backing off — a
  backing-off runner is not available for work, and queue-depth metrics must
  not claim otherwise.

## Implementation Plan

### Steps

1. `server/internal/jobs/jobworker/worker.go` — add the backoff constants
   (`errBackoffMin = 100ms`, `errBackoffMax = 30s`) and restructure
   `workerLoop`:

   ```go
   backoff, consecutive := time.Duration(0), 0
   var firstFailure time.Time

   for {
       if backoff > 0 {
           select {
           case <-ctx.Done():
               return
           case <-time.After(jitter(backoff)):
           }
       }

       select {
       case <-ctx.Done():
           return
       default:
       }

       w.availableRunners.Add(1)
       err := w.processNext(ctx, logger)
       w.availableRunners.Add(-1)

       switch {
       case err == nil:
           if consecutive > 0 {
               logger.InfoContext(ctx, "Job processing recovered",
                   "consecutive_failures", consecutive,
                   "outage", time.Since(firstFailure))
           }
           backoff, consecutive = 0, 0
       case errors.Is(err, context.Canceled):
           return
       default:
           if consecutive == 0 {
               firstFailure = time.Now()
           }
           consecutive++
           if shouldLog(consecutive) { // 1, 2, 4, 8, …
               logger.ErrorContext(ctx, "Error processing job",
                   "error", err, "consecutive", consecutive)
           }
           backoff = nextBackoff(backoff)
       }
   }
   ```

   Note the `availableRunners` pairing stays tight around `processNext` and the
   backoff sleep sits outside it, satisfying the queue-depth criterion.

2. Keep `nextBackoff`, `jitter` and `shouldLog` as unexported helpers in the
   same file — they are three lines each and used nowhere else. `jitter` should
   be full-jitter (`rand` in `[backoff/2, backoff]`) so two runners diverge.

3. `context.Canceled` currently only suppresses the log and keeps looping; the
   loop then exits on the next `ctx.Done()` check anyway. Returning directly
   (as above) is equivalent and clearer — verify no test depends on the extra
   iteration.

### Tests

`server/internal/jobs/jobworker/` already has `worker_metrics_test.go` with a
`fakeJobSvc`; extend that fake rather than introducing a new one.

- **Backoff engages**: a `fakeJobSvc` returning an error instantly. Assert the
  call count over a fixed window is bounded (a few calls, not thousands) and
  that observed gaps grow. Drive time with an injectable clock if one exists;
  otherwise assert on call-count bounds rather than wall-clock sleeps, to keep
  the test fast and non-flaky.
- **Reset on success**: fail N times, then succeed, then fail once — assert the
  last failure is logged immediately (not suppressed by the pre-recovery
  counter) and that the next attempt is not delayed by the old backoff.
- **Log throttling**: capture with a `slog` handler into a buffer; assert
  exactly the 1st, 2nd, 4th, 8th … failures produce records, each carrying
  `consecutive`.
- **Recovery summary**: exactly one `Job processing recovered` record, with the
  right count.
- **Cancellation during backoff**: cancel while a runner is parked at a long
  backoff; assert `Start` returns well inside the cap.
- **Availability accounting**: assert `availableRunners` is 0 while runners are
  backing off.

### Verification

Reproduce the original condition end to end: start the server, drop the `jobs`
table underneath it, let it run for a few minutes, and confirm the log grows by
kilobytes rather than gigabytes and CPU stays near idle.
