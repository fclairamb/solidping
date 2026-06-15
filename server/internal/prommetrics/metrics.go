// Package prommetrics provides Prometheus metric definitions and recording helpers for SolidPing.
package prommetrics

import "github.com/prometheus/client_golang/prometheus"

// Prometheus metric label names used across multiple metrics.
const (
	labelCheckType    = "check_type"
	labelCheckSlug    = "check_slug"
	labelStatus       = "status"
	labelRegion       = "region"
	labelOrganization = "organization"
	labelEnabled      = "enabled"
	labelWorkerUID    = "worker_uid"
	labelBackend      = "backend"
	labelOperation    = "operation"
	labelStage        = "stage"
	labelMethod       = "method"
	labelRoute        = "route"
	labelOutcome      = "outcome"
	labelJobType      = "job_type"
)

//nolint:gochecknoglobals // Prometheus metrics are conventionally package-level vars
var (
	// CheckExecutions counts total check executions.
	CheckExecutions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_check_executions_total",
			Help: "Total number of check executions",
		},
		[]string{labelCheckType, labelStatus, labelRegion, labelOrganization},
	)

	// CheckDuration observes check execution duration in seconds.
	CheckDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "solidping_check_duration_seconds",
			Help:    "Check execution duration in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		},
		[]string{labelCheckType, labelStatus, labelRegion, labelOrganization},
	)

	// SchedulingDelay observes delay between scheduled and actual execution time.
	SchedulingDelay = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "solidping_check_scheduling_delay_seconds",
			Help:    "Delay between scheduled and actual execution time",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{labelRegion},
	)

	// CheckUp indicates whether a check is currently UP (1) or DOWN (0).
	CheckUp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_check_up",
			Help: "1 if check is currently UP, 0 otherwise",
		},
		[]string{labelCheckSlug, labelCheckType, labelRegion, labelOrganization},
	)

	// CheckStatusStreak tracks consecutive results with current status.
	CheckStatusStreak = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_check_status_streak",
			Help: "Consecutive results with current status",
		},
		[]string{labelCheckSlug, labelCheckType, labelOrganization},
	)

	// ChecksConfigured tracks the number of configured checks.
	ChecksConfigured = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_checks_configured",
			Help: "Number of configured checks",
		},
		[]string{labelCheckType, labelOrganization, "enabled"},
	)

	// WorkersActive tracks the number of active workers.
	WorkersActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_workers_active",
			Help: "Number of active workers",
		},
		[]string{labelRegion},
	)

	// WorkerFreeRunners tracks available runner slots per worker.
	WorkerFreeRunners = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_worker_free_runners",
			Help: "Available runner slots per worker",
		},
		[]string{"worker_uid", labelRegion},
	)

	// WorkerJobsClaimed counts total jobs claimed by each worker.
	WorkerJobsClaimed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_worker_jobs_claimed_total",
			Help: "Total jobs claimed by worker",
		},
		[]string{"worker_uid", labelRegion},
	)

	// IncidentsActive tracks currently open incidents.
	IncidentsActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_incidents_active",
			Help: "Currently open incidents",
		},
		[]string{labelOrganization},
	)

	// IncidentsTotal counts total incidents created.
	IncidentsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_incidents_total",
			Help: "Total incidents created",
		},
		[]string{labelOrganization, labelCheckType},
	)

	// ChecksRateLimited counts check executions skipped because the
	// org's MaxChecksPerMinute entitlement was already drained for the
	// current bucket. Skipped jobs are simply rescheduled for next
	// period — no result row is written.
	ChecksRateLimited = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_checks_rate_limited_total",
			Help: "Total check executions skipped due to MaxChecksPerMinute entitlement",
		},
		[]string{labelOrganization},
	)

	// HTTPRateLimited counts requests intercepted by the per-IP HTTP rate or
	// concurrency limiters. The reason label has four values:
	//   "rate"               — rejected with 429: token bucket empty and slow lane full / waited out.
	//   "rate_delayed"       — succeeded after waiting in the rate-limit slow lane.
	//   "concurrency"        — rejected with 429: no slot free and waiting room full / waited out.
	//   "concurrency_queued" — succeeded after waiting for a concurrency slot.
	HTTPRateLimited = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_http_rate_limited_total",
			Help: "Total requests rejected or queued by the per-IP HTTP rate or concurrency limiter",
		},
		[]string{"reason"},
	)

	// HTTPRequestDuration observes HTTP handler latency by route pattern.
	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "solidping_http_request_duration_seconds",
			Help:    "HTTP handler duration in seconds, keyed by route pattern (low cardinality)",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{labelMethod, labelRoute, labelStatus},
	)

	// HTTPRequestsTotal counts HTTP requests by route pattern and status.
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_http_requests_total",
			Help: "Total HTTP requests by route pattern and status",
		},
		[]string{labelMethod, labelRoute, labelStatus},
	)

	// DBQueryDuration observes SQL query latency by operation and backend.
	// Recorded from the bun sloghook on every query (SELECT, INSERT, UPDATE,
	// DELETE, BEGIN/COMMIT). Status is "ok" or "error".
	DBQueryDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "solidping_db_query_duration_seconds",
			Help:    "SQL query duration in seconds, by operation and backend",
			Buckets: []float64{0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		},
		[]string{labelOperation, labelBackend, labelStatus},
	)

	// DBBusyRetries counts SQLite SQLITE_BUSY and PostgreSQL
	// serialization-failure errors observed by the sloghook. A non-zero
	// rate indicates write contention; on SQLite it usually means the
	// 30s busy_timeout is being hit.
	DBBusyRetries = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_db_busy_retries_total",
			Help: "Database busy / serialization-failure errors (SQLite SQLITE_BUSY, PG 40001)",
		},
		[]string{labelBackend},
	)

	// CheckStageDuration breaks the per-check lifecycle into named stages
	// (claim, execute, save_result, process_incident, release_lease,
	// fetch). Lets us see where wall-clock time actually goes when
	// throughput plateaus.
	CheckStageDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "solidping_check_stage_duration_seconds",
			Help:    "Per-stage wall-clock duration inside the check execution lifecycle",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 30},
		},
		[]string{labelStage},
	)

	// ClaimJobsResult counts the outcome of each ClaimJobs call from
	// fetcherLoop. Distinguishes "jobs returned" from "no due jobs" and
	// "due jobs were locked by another worker / optimistic conflict".
	ClaimJobsResult = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_claim_jobs_result_total",
			Help: "Outcome of ClaimJobs calls (jobs, empty, lock_conflict, error)",
		},
		[]string{labelOutcome},
	)

	// JobsProcessed counts background-jobs processed by the job worker,
	// labeled by job_type and terminal outcome (success | retried | failed).
	// job_type is bounded by the jobdef enum; never use job_uid, org, or error
	// text as labels (cardinality).
	JobsProcessed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_jobs_processed_total",
			Help: "Total background jobs processed, by type and outcome",
		},
		[]string{labelJobType, labelOutcome},
	)

	// JobDuration observes background-job execution wall-clock duration in
	// seconds, labeled by job_type and outcome. Buckets span fast webhooks to
	// slow discovery fan-outs.
	JobDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "solidping_job_duration_seconds",
			Help:    "Background job execution duration in seconds, by type and outcome",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		},
		[]string{labelJobType, labelOutcome},
	)

	// JobSchedulingDelay observes the delay between a job's scheduled_at and the
	// time it actually started running (clamped >= 0) — the queue-lateness
	// signal, mirroring solidping_check_scheduling_delay_seconds.
	JobSchedulingDelay = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "solidping_job_scheduling_delay_seconds",
			Help:    "Delay between a job's scheduled time and when it started running",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 300},
		},
		[]string{labelJobType},
	)

	// JobsQueueDepth is the point-in-time backlog of background jobs by status
	// (pending | running). Set by a periodic sampler that zero-fills statuses
	// with no rows so a drained status drops to 0 rather than going stale.
	JobsQueueDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_jobs_queue_depth",
			Help: "Background-job queue depth by status (pending, running)",
		},
		[]string{labelStatus},
	)

	// JobsReaped counts jobs the stuck-job reaper recovered, by outcome
	// ("retried" = rescheduled via the retry chain, "failed" = retry cap
	// reached). A spike signals worker instability (orphaned jobs from
	// restarts/crashes/deploys), not normal operation.
	JobsReaped = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_jobs_reaped_total",
			Help: "Total background jobs recovered by the stuck-job reaper, by outcome (retried, failed)",
		},
		[]string{labelOutcome},
	)

	// JobsLeaseLost counts terminal job writes the worker discarded because the
	// reaper had already moved the row out of 'running' (the worker lost its
	// job to the reaper). Non-zero means the stuck-timeout is firing on jobs
	// that were still alive — consider raising SP_JOBS_STUCK_TIMEOUT.
	JobsLeaseLost = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_jobs_lease_lost_total",
			Help: "Worker terminal writes discarded because the reaper already transitioned the job, by job type",
		},
		[]string{labelJobType},
	)

	allCollectors = []prometheus.Collector{
		CheckExecutions, CheckDuration, SchedulingDelay,
		CheckUp, CheckStatusStreak, ChecksConfigured,
		WorkersActive, WorkerFreeRunners, WorkerJobsClaimed,
		IncidentsActive, IncidentsTotal,
		ChecksRateLimited,
		HTTPRateLimited,
		HTTPRequestDuration, HTTPRequestsTotal,
		DBQueryDuration, DBBusyRetries,
		CheckStageDuration, ClaimJobsResult,
		JobsProcessed, JobDuration, JobSchedulingDelay, JobsQueueDepth,
		JobsReaped, JobsLeaseLost,
	}
)

// Register registers all SolidPing metrics with the given registerer.
func Register(reg prometheus.Registerer) {
	for _, c := range allCollectors {
		reg.MustRegister(c)
	}
}
