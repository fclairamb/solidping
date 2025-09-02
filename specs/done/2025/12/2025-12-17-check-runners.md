# Check Runner Implementation

## Overview
A check runner is a worker process that continuously fetches and executes health check jobs from the database. Each runner is identified by a slug and operates in a specific region (or globally if no region is specified).

## Configuration Parameters

- `$checkjobs_fetch_nb`: Maximum number of check jobs to fetch in a single batch, default to 10
- `$checkjobs_fetch_max_ahead`: Maximum number of seconds ahead of current time to look for scheduled jobs, defaults to 300 seconds
- `$checkjobs_concurrency`: Maximum number of concurrent goroutines for executing check jobs, defaults to 5
- `$checkjobs_lease_duration`: Duration of the lease for a job, defaults to 60 seconds

## Check Runner Lifecycle

The check runner operates in a continuous loop with the following phases:

### 1. Job Fetching Phase

Fetch and claim up to `$checkjobs_fetch_nb` jobs from the database in a single atomic operation.

**Selection Criteria:**
- **Ordering**: Jobs with the earliest `scheduled_at` timestamp first
- **Region matching**: Jobs must match the runner's region OR have no specific region requirement
- **Scheduling window**: `scheduled_at` must be at most `$checkjobs_fetch_max_ahead` seconds in the future
- **Lease availability**: Job must have either:
  - `lease_expires_at` is NULL (no active lease), OR
  - `lease_expires_at` < now (lease has expired and job can be claimed)

**Atomic Lease Acquisition:**

During the fetch operation, atomically update the selected jobs to claim them:
- `lease_worker_id` → this runner's identifier
- `lease_expires_at` → now + `$checkjobs_lease_duration`
- `lease_starts` → `lease_starts` + 1 (increment to track incomplete executions)

**Purpose of `lease_starts`:** This field tracks the number of lease acquisitions that started but never completed properly. When a check job completes successfully (or fails), the lease is released and `lease_starts` is reset to 0. If `lease_starts` grows large, it indicates the job is getting stuck or workers are crashing before completing it.

This prevents race conditions where multiple runners try to claim the same job.

### 2. Job Execution Phase

Schedule each fetched job to run in a goroutine pool (size: `$checkjobs_concurrency`).

For each check job:

**a) Execute Check**
- Run the checker based on its type and configuration
- Collect the result (success/failure, latency, error details, etc.)

**b) Save Results**
- Insert check result into the `results` table
- Store metrics: status, latency, timestamp, error messages, etc.

**c) Release Lease & Reschedule**

Update the `check_jobs` table:
- Clear lease fields (job completed successfully or failed):
  - `lease_worker_id` → NULL
  - `lease_expires_at` → NULL
  - `lease_starts` → 0 (reset since this execution completed)
- Update metadata:
  - `updated_at` → current timestamp
- Calculate next scheduled time:
  - If `scheduled_at + period` > now: `scheduled_at` → `scheduled_at + period`
  - Otherwise (we're behind schedule): `scheduled_at` → `now + period`

### 3. Smart Sleep Phase

After distributing jobs to the execution pool, the runner calculates the optimal sleep duration based on when jobs need to execute next.

**Note:** The `GetNextScheduledTime` service method is not needed and should be removed if it exists. The smart sleep calculation is done directly in the runner by tracking the rescheduled jobs.

**Sleep Duration Calculation:**
1. Track the lowest `scheduled_at` value among all jobs that were just rescheduled in the current execution cycle
2. If jobs were rescheduled (lowest `scheduled_at` is available):
   - Calculate time until next job: `time_until_next = lowest_scheduled_at - now`
   - Sleep for half of that time: `sleep_duration = time_until_next / 2`
3. If no jobs were rescheduled in this cycle:
   - Sleep for half of `$checkjobs_fetch_max_ahead`: `sleep_duration = $checkjobs_fetch_max_ahead / 2`
4. Apply bounds to prevent extreme values:
   - Minimum sleep: 1 second
   - Maximum sleep: 60 seconds

After sleeping, return to **Job Fetching Phase**

**Rationale:** Sleeping for half the time until the next job ensures the runner wakes up before the job is due, while minimizing unnecessary wake-ups. The bounds prevent both excessive polling (too short) and delayed execution (too long).

## Lease Mechanism

The lease mechanism prevents multiple runners from executing the same job simultaneously:

**How It Works:**
- A runner claims a job by setting `lease_worker_id` and `lease_expires_at`
- Other runners skip jobs with active leases (where `lease_expires_at` > now)
- If a runner crashes, its lease eventually expires and the job becomes available again
- After successful execution (or failure), the lease is cleared and the job is rescheduled

**Tracking Incomplete Executions:**
- `lease_starts` increments each time a lease is acquired
- `lease_starts` is reset to 0 when the job completes (successfully or with failure)
- A high `lease_starts` value indicates the job is repeatedly failing to complete, possibly due to:
  - Workers crashing during execution
  - Check timeouts or hangs
  - Bugs in the checker implementation
  - Infrastructure issues
