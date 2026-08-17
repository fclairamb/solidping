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
	labelCallsite     = "callsite"
	labelStage        = "stage"
	labelMethod       = "method"
	labelRoute        = "route"
	labelOutcome      = "outcome"
	labelJobType      = "job_type"
	labelLane         = "lane"
	labelMessageType  = "type"
	labelListener     = "listener"
)

// Lane label values for CheckLaneClaims (spec 2026-07-01-03).
const (
	// LaneLabelFast is the lane label for fast-lane (lane 0) claims.
	LaneLabelFast = "fast"
	// LaneLabelSlow is the lane label for slow-lane (lane 1) claims.
	LaneLabelSlow = "slow"
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
		[]string{labelWorkerUID, labelRegion},
	)

	// CheckRunnerParked tracks runner slots per worker currently occupied by
	// a claimed job sleeping until its scheduled_at — claimed but not yet
	// due (spec 2026-07-05-08 D5). Visibility into how much of the pool the
	// bounded claim-ahead window (D3) is occupying; alongside
	// WorkerFreeRunners this distinguishes "idle" from "parked" instead of
	// both looking like "not free".
	CheckRunnerParked = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_check_runner_parked",
			Help: "Runner slots currently occupied by a claimed job sleeping until its scheduled time",
		},
		[]string{labelWorkerUID, labelRegion},
	)

	// WorkerJobsClaimed counts total jobs claimed by each worker.
	WorkerJobsClaimed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_worker_jobs_claimed_total",
			Help: "Total jobs claimed by worker",
		},
		[]string{labelWorkerUID, labelRegion},
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

	// DBQueryDuration observes SQL query latency by operation, backend and
	// callsite. Recorded from the bun sloghook on every query (SELECT, INSERT,
	// UPDATE, DELETE, BEGIN/COMMIT). Status is "ok" or "error". callsite is a
	// low-cardinality label threaded through ctx by the calling package (see
	// internal/db/sloghook.WithCallsite) — bounded by construction to the
	// handful of packages that annotate their context, plus "unlabelled" for
	// everything else, so cardinality stays proportional to the number of
	// annotated call paths rather than to traffic or argument values.
	DBQueryDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "solidping_db_query_duration_seconds",
			Help:    "SQL query duration in seconds, by operation, backend and callsite",
			Buckets: []float64{0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		},
		[]string{labelOperation, labelBackend, labelStatus, labelCallsite},
	)

	// ResultsRowCount gauges the total row count in the results table by
	// period_type (raw/hour/day/month), across all organizations. Refreshed on
	// the aggregation job's cadence (internal/jobs/jobtypes/job_aggregation.go),
	// never per-request — a table-wide COUNT(*) is exactly what this table
	// cannot afford on every page load. Makes ingest growth visible before it
	// crosses a shared_buffers cache cliff (spec 2026-08-17-04).
	ResultsRowCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			// Deliberately not "..._row_count": promlinter reserves the
			// "_count" suffix for histograms/summaries.
			Name: "solidping_results_rows",
			Help: "Total rows in the results table, by period_type",
		},
		[]string{"period_type"},
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

	// CheckLaneClaims counts claimed check jobs by lane (fast | slow), the
	// per-lane companion to ClaimJobsResult (spec 2026-07-01-03 D6). A slow
	// lane pinned at zero while slow work is due means the reservation budget
	// is saturated (busySlow == pool − fast_lane_reserved) — the intended,
	// contained failure mode where slow checks degrade to best-effort while
	// fast checks stay on time.
	CheckLaneClaims = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_check_lane_claims_total",
			Help: "Check jobs claimed by the pool fetcher, by lane (fast, slow)",
		},
		[]string{labelLane},
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

	// RealtimeConnections tracks currently open realtime hint WebSocket
	// connections. Global gauge — no per-org label so cardinality stays bounded.
	RealtimeConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "solidping_realtime_connections",
			Help: "Currently open realtime hint stream connections",
		},
	)

	// RealtimeHintsPublished counts org hint events published to the notifier
	// bus (after coalescing).
	RealtimeHintsPublished = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "solidping_realtime_hints_published_total",
			Help: "Total org hint events published to the notifier bus",
		},
	)

	// RealtimeHintsCoalesced counts hint publications absorbed by the
	// leading-edge coalescer (merged into a pending per-org dirty set instead
	// of producing an immediate bus publish).
	RealtimeHintsCoalesced = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "solidping_realtime_hints_coalesced_total",
			Help: "Total hint publications merged by the coalescer instead of published immediately",
		},
	)

	// RealtimeHintsDelivered counts hint deliveries to local stream
	// subscribers (one increment per subscriber that received a hint).
	RealtimeHintsDelivered = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "solidping_realtime_hints_delivered_total",
			Help: "Total hint events delivered to local realtime stream subscribers",
		},
	)

	// RealtimeSubscriptions tracks currently active per-connection scope
	// subscriptions (sum across every open connection). Global gauge — no
	// per-org label so cardinality stays bounded.
	RealtimeSubscriptions = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "solidping_realtime_subscriptions",
			Help: "Currently active realtime scope subscriptions across all connections",
		},
	)

	// RealtimeMessagesReceived counts client->server WebSocket messages
	// processed by the realtime handler, labeled by message type (auth,
	// subscribe, unsubscribe, and unknown/malformed).
	RealtimeMessagesReceived = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_realtime_messages_received_total",
			Help: "Total client->server realtime WebSocket messages processed, by type",
		},
		[]string{labelMessageType},
	)

	// CheckRunnerAbandoned counts checker executions the watchdog gave up on
	// because the checker did not honor its context deadline within
	// execTimeout + abandonGrace (spec 2026-07-05-05 D1/D3). A lost runner
	// goroutine is otherwise silent and cumulative; this is the loud signal.
	CheckRunnerAbandoned = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_check_runner_abandoned_total",
			Help: "Total checker executions abandoned by the watchdog because the checker ignored its context",
		},
		[]string{labelCheckType},
	)

	// CheckRunnerAbandonedActive gauges checker goroutines currently
	// abandoned-but-still-running: incremented when the watchdog gives up on
	// an execution, decremented by the child goroutine's own deferred
	// cleanup if it ever returns (normally or via a late panic). Non-zero
	// for longer than a few executions means leaked goroutines are
	// accumulating (the exact failure mode from the 2026-07-04/05
	// incident) — this is the direct "leaked goroutines right now" signal
	// that was invisible then. No labels: a single fleet-wide count per D3.
	CheckRunnerAbandonedActive = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "solidping_check_runner_abandoned_active",
			Help: "Checker goroutines currently abandoned by the watchdog but still running (leaked)",
		},
	)

	// TLSEdgeConnections counts connections classified by the TLS edge's
	// fallback splitter, per listener ("http"/"https") and outcome
	// ("local", "forwarded", "refused", "dial_failed"). A chained deployment
	// is otherwise silent: without this, "the downstream instance stopped
	// getting traffic" and "the next hop is unreachable" look identical.
	TLSEdgeConnections = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_tlsedge_connections_total",
			Help: "Connections classified by the TLS edge fallback splitter",
		},
		[]string{labelListener, labelOutcome},
	)

	allCollectors = []prometheus.Collector{
		CheckExecutions, CheckDuration, SchedulingDelay,
		CheckUp, CheckStatusStreak, ChecksConfigured,
		WorkersActive, WorkerFreeRunners, CheckRunnerParked, WorkerJobsClaimed,
		IncidentsActive, IncidentsTotal,
		ChecksRateLimited,
		HTTPRateLimited,
		HTTPRequestDuration, HTTPRequestsTotal,
		DBQueryDuration, DBBusyRetries, ResultsRowCount,
		CheckStageDuration, ClaimJobsResult, CheckLaneClaims,
		JobsProcessed, JobDuration, JobSchedulingDelay, JobsQueueDepth,
		JobsReaped, JobsLeaseLost,
		RealtimeConnections, RealtimeHintsPublished,
		RealtimeHintsCoalesced, RealtimeHintsDelivered,
		RealtimeSubscriptions, RealtimeMessagesReceived,
		CheckRunnerAbandoned, CheckRunnerAbandonedActive,
		TLSEdgeConnections,
	}
)

// Register registers all SolidPing metrics with the given registerer, plus the
// Go runtime and process collectors (heap/RSS/goroutine/GC time series) that
// memory leak detection depends on. Called for every node role so the API
// server and worker expose the same /metrics surface.
func Register(reg prometheus.Registerer) {
	for _, c := range allCollectors {
		reg.MustRegister(c)
	}
	registerRuntimeCollectors(reg)
}
