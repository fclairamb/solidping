---
model: sonnet
effort: medium
---

# Job worker metrics tests assert absolute histogram counts, breaking `go test -count>1`

## Problem

`server/internal/jobs/jobworker/worker_metrics_test.go` asserts an **absolute**
Prometheus histogram sample count of `1` against process-global metric vectors
with fixed label sets:

- [worker_metrics_test.go:276-279](server/internal/jobs/jobworker/worker_metrics_test.go:276) —
  `TestProcessNextRecordsUnknownTypeFailure` asserts
  `r.Equal(uint64(1), histogramSampleCount(r, prommetrics.JobDuration.WithLabelValues(jobType, outcomeFailed)))`
  and the same for `prommetrics.JobSchedulingDelay.WithLabelValues(jobType)`.
- [worker_metrics_test.go:356-359](server/internal/jobs/jobworker/worker_metrics_test.go:356) —
  `TestRecordJobMetricsSchedulingDelay` has the same absolute-`1` shape against
  `prommetrics.JobSchedulingDelay` for `metrics-delay-past` / `metrics-delay-zero`.

`prommetrics.*` vectors are package-global and never reset between test
iterations, and the label values are compile-time constants, so each repetition
of the test observes into the *same* child histogram. The count grows 1, 2, 3, …
and the assertion fails from the second iteration onward:

```bash
go test ./server/internal/jobs/jobworker/ -count=5
```

fails with observed count 5, expected 1.

This matters beyond the immediate failure: this repo's convention is that flaky
tests are bugs to be root-caused, and repeated runs (`-count=N -race`) are the
standard reproduction tool. Right now that tool is unusable for the job worker
package specifically.

Note the counter assertions in the same file already do this correctly — e.g.
[worker_metrics_test.go:231-242](server/internal/jobs/jobworker/worker_metrics_test.go:231)
and [:256-273](server/internal/jobs/jobworker/worker_metrics_test.go:256) read a
`before` value via `testutil.ToFloat64` and assert `before+1` with `r.InDelta`.
Only the histogram assertions are absolute. So the fix is to bring the histogram
assertions in line with the file's own existing convention.

Pre-existing; last touched in commit `73ff0471`. Unrelated to the current
batch's job-worker backoff work — found in passing during an audit on branch
`batch/2026-08-12`.

## Proposal

Make the histogram assertions **relative** rather than absolute, matching the
counter pattern already used in the file:

1. In both `TestProcessNextRecordsUnknownTypeFailure` and
   `TestRecordJobMetricsSchedulingDelay`, capture the histogram sample count
   *before* exercising the code under test, then assert the delta is exactly 1:

   ```go
   beforeDur := histogramSampleCount(r, prommetrics.JobDuration.WithLabelValues(jobType, outcomeFailed))
   // ... exercise ...
   r.Equal(beforeDur+1, histogramSampleCount(r, prommetrics.JobDuration.WithLabelValues(jobType, outcomeFailed)))
   ```

   Keep the assertion strict (`+1`, not "≥ before") so it still proves the code
   observes exactly once — a double-observation regression must still fail.

2. Sweep the rest of the package for the same shape while you're there: any
   other assertion that compares a process-global metric against a fixed
   absolute value has the same defect. Fix them the same way.

Alternatives considered — prefer whichever actually fits the repo's existing
metrics-test conventions, but the before/after delta is the default because the
file already uses it for counters:

- Reset/unregister the vectors between tests (`vec.Reset()` / `DeleteLabelValues`)
  — fragile under `t.Parallel()`, which every test here uses; two parallel tests
  sharing a vector could reset each other's observations.
- Inject a fresh `prometheus.Registry` per test — cleanest in principle, but only
  viable if `prommetrics` already supports registry injection; do not refactor the
  metrics plumbing just for this.

### Verification

- Repeated runs must be green:
  ```bash
  go test ./server/internal/jobs/jobworker/ -count=5 -race
  ```
- Confirm the fix actually addresses the reported failure by reproducing it
  first on the unmodified test (it should fail on the 2nd–5th iteration), then
  re-running after the change.
- `make build-backend lint-back` before committing.
- **Do not relax `.golangci.yml`** — fix any lint findings in the code.
