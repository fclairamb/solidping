package prommetrics

// RecordExecution records a check execution's counter increment and duration observation.
// durationMs is provided in milliseconds and converted to seconds for the histogram.
func RecordExecution(checkType, status, region, org string, durationMs float64) {
	CheckExecutions.WithLabelValues(checkType, status, region, org).Inc()
	CheckDuration.WithLabelValues(checkType, status, region, org).Observe(durationMs / 1000)
}

// RecordSchedulingDelay records the delay between scheduled and actual execution time.
func RecordSchedulingDelay(region string, delaySeconds float64) {
	SchedulingDelay.WithLabelValues(region).Observe(delaySeconds)
}

// SetCheckStatus sets the up/down gauge for a specific check.
func SetCheckStatus(checkSlug, checkType, region, org string, up bool) {
	val := 0.0
	if up {
		val = 1.0
	}

	CheckUp.WithLabelValues(checkSlug, checkType, region, org).Set(val)
}

// SetCheckStatusStreak sets the consecutive status streak for a check.
func SetCheckStatusStreak(checkSlug, checkType, org string, streak float64) {
	CheckStatusStreak.WithLabelValues(checkSlug, checkType, org).Set(streak)
}

// SetChecksConfigured sets the number of configured checks for a given type/org/enabled combo.
func SetChecksConfigured(checkType, org, enabled string, count float64) {
	ChecksConfigured.WithLabelValues(checkType, org, enabled).Set(count)
}

// SetWorkersActive sets the number of active workers in a region.
func SetWorkersActive(region string, count float64) {
	WorkersActive.WithLabelValues(region).Set(count)
}

// SetWorkerFreeRunners sets the available runner slots for a worker.
func SetWorkerFreeRunners(workerUID, region string, count float64) {
	WorkerFreeRunners.WithLabelValues(workerUID, region).Set(count)
}

// SetCheckRunnerParked sets the number of runner slots for a worker currently
// occupied by a claimed job sleeping until its scheduled time (spec
// 2026-07-05-08 D5).
func SetCheckRunnerParked(workerUID, region string, count float64) {
	CheckRunnerParked.WithLabelValues(workerUID, region).Set(count)
}

// RecordWorkerJobClaimed increments the jobs claimed counter for a worker.
func RecordWorkerJobClaimed(workerUID, region string) {
	WorkerJobsClaimed.WithLabelValues(workerUID, region).Inc()
}

// SetIncidentsActive sets the number of currently open incidents for an organization.
func SetIncidentsActive(org string, count float64) {
	IncidentsActive.WithLabelValues(org).Set(count)
}

// RecordIncidentCreated increments the total incidents counter.
func RecordIncidentCreated(org, checkType string) {
	IncidentsTotal.WithLabelValues(org, checkType).Inc()
}

// RecordHTTPRequest records the duration and outcome of an HTTP request,
// keyed by the route pattern (not the raw path) to keep cardinality
// bounded. Pass an empty route to skip recording — useful when the
// request hit a 404 catch-all that doesn't correspond to a registered
// route.
func RecordHTTPRequest(method, route, status string, durationSeconds float64) {
	if route == "" {
		return
	}
	HTTPRequestDuration.WithLabelValues(method, route, status).Observe(durationSeconds)
	HTTPRequestsTotal.WithLabelValues(method, route, status).Inc()
}

// RecordDBQuery records a SQL query observation. backend is "sqlite" or
// "postgres"; operation is the bun-reported verb ("SELECT", "INSERT",
// "UPDATE", "DELETE", "BEGIN", "COMMIT", "ROLLBACK"); callsite is the bounded
// label from internal/db/sloghook.WithCallsite (or "unlabelled"); ok=false
// means the query returned a non-ErrNoRows error.
func RecordDBQuery(operation, backend, callsite string, durationSeconds float64, ok bool) {
	status := "ok"
	if !ok {
		status = "error"
	}
	DBQueryDuration.WithLabelValues(operation, backend, status, callsite).Observe(durationSeconds)
}

// SetResultsRowCount sets the results-row-count gauge for the given
// period_type. Called by the aggregation-job-cadence sampler, never per-request.
func SetResultsRowCount(periodType string, count float64) {
	ResultsRowCount.WithLabelValues(periodType).Set(count)
}

// RecordDBBusyRetry increments the busy-retry counter for the given
// backend. Called when the bun hook spots a SQLITE_BUSY (SQLite) or a
// serialization-failure (Postgres).
func RecordDBBusyRetry(backend string) {
	DBBusyRetries.WithLabelValues(backend).Inc()
}

// RecordCheckStage observes wall-clock duration for one stage of the
// check-execution lifecycle. Stages: "fetch", "claim", "execute",
// "save_result", "process_incident", "release_lease".
func RecordCheckStage(stage string, durationSeconds float64) {
	CheckStageDuration.WithLabelValues(stage).Observe(durationSeconds)
}

// RecordClaimJobsOutcome increments the claim-jobs outcome counter.
// outcome ∈ {"jobs", "empty", "lock_conflict", "error"}.
func RecordClaimJobsOutcome(outcome string) {
	ClaimJobsResult.WithLabelValues(outcome).Inc()
}

// RecordLaneClaims adds n claimed jobs to the per-lane claim counter.
// lane ∈ {LaneLabelFast, LaneLabelSlow}. A zero n is a no-op so callers can
// pass raw per-batch counts without pre-filtering.
func RecordLaneClaims(lane string, n int) {
	if n <= 0 {
		return
	}
	CheckLaneClaims.WithLabelValues(lane).Add(float64(n))
}

// RecordJobProcessed increments the processed-jobs counter for the given
// job type and terminal outcome ("success" | "retried" | "failed").
func RecordJobProcessed(jobType, outcome string) {
	JobsProcessed.WithLabelValues(jobType, outcome).Inc()
}

// RecordJobDuration observes a background job's execution duration in seconds,
// labeled by job type and terminal outcome.
func RecordJobDuration(jobType, outcome string, seconds float64) {
	JobDuration.WithLabelValues(jobType, outcome).Observe(seconds)
}

// RecordJobSchedulingDelay observes the delay (in seconds, clamped >= 0)
// between a job's scheduled time and when it actually started running.
func RecordJobSchedulingDelay(jobType string, seconds float64) {
	JobSchedulingDelay.WithLabelValues(jobType).Observe(seconds)
}

// SetJobsQueueDepth sets the queue-depth gauge for the given status
// ("pending" | "running"). Callers must zero-fill statuses with no rows so a
// drained status reports 0 rather than going stale.
func SetJobsQueueDepth(status string, count float64) {
	JobsQueueDepth.WithLabelValues(status).Set(count)
}

// RecordJobReaped increments the reaped-jobs counter by n for the given outcome
// ("retried" | "failed"). n is the number of jobs reaped in one sweep with that
// outcome; a zero n is a no-op.
func RecordJobReaped(outcome string, n int) {
	if n <= 0 {
		return
	}
	JobsReaped.WithLabelValues(outcome).Add(float64(n))
}

// RecordJobLeaseLost increments the lease-lost counter for the given job type.
// Called when a worker's terminal write is discarded because the reaper already
// transitioned the job out of 'running'.
func RecordJobLeaseLost(jobType string) {
	JobsLeaseLost.WithLabelValues(jobType).Inc()
}

// RecordCheckRunnerAbandoned increments the abandoned-execution counter for
// the given check type. Called once per watchdog abandonment (spec
// 2026-07-05-05 D3).
func RecordCheckRunnerAbandoned(checkType string) {
	CheckRunnerAbandoned.WithLabelValues(checkType).Inc()
}

// IncCheckRunnerAbandonedActive marks one more checker goroutine as
// abandoned-but-still-running. Paired with DecCheckRunnerAbandonedActive when
// (if) the child goroutine ever returns.
func IncCheckRunnerAbandonedActive() {
	CheckRunnerAbandonedActive.Inc()
}

// DecCheckRunnerAbandonedActive marks a previously-abandoned checker
// goroutine as no longer outstanding (it finally returned, normally or via a
// recovered panic). Must only be called after a matching
// IncCheckRunnerAbandonedActive.
func DecCheckRunnerAbandonedActive() {
	CheckRunnerAbandonedActive.Dec()
}
