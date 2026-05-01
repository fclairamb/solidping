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
)

//nolint:gochecknoglobals // Prometheus metrics are conventionally package-level vars
var (
	// CheckExecutions counts total check executions.
	CheckExecutions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_check_executions_total",
			Help: "Total number of check executions",
		},
		[]string{labelCheckType, "status", "region", "organization"},
	)

	// CheckDuration observes check execution duration in seconds.
	CheckDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "solidping_check_duration_seconds",
			Help:    "Check execution duration in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		},
		[]string{labelCheckType, "status", "region", "organization"},
	)

	// SchedulingDelay observes delay between scheduled and actual execution time.
	SchedulingDelay = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "solidping_check_scheduling_delay_seconds",
			Help:    "Delay between scheduled and actual execution time",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{"region"},
	)

	// CheckUp indicates whether a check is currently UP (1) or DOWN (0).
	CheckUp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_check_up",
			Help: "1 if check is currently UP, 0 otherwise",
		},
		[]string{"check_slug", labelCheckType, "region", "organization"},
	)

	// CheckStatusStreak tracks consecutive results with current status.
	CheckStatusStreak = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_check_status_streak",
			Help: "Consecutive results with current status",
		},
		[]string{"check_slug", labelCheckType, "organization"},
	)

	// ChecksConfigured tracks the number of configured checks.
	ChecksConfigured = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_checks_configured",
			Help: "Number of configured checks",
		},
		[]string{labelCheckType, "organization", "enabled"},
	)

	// WorkersActive tracks the number of active workers.
	WorkersActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_workers_active",
			Help: "Number of active workers",
		},
		[]string{"region"},
	)

	// WorkerFreeRunners tracks available runner slots per worker.
	WorkerFreeRunners = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_worker_free_runners",
			Help: "Available runner slots per worker",
		},
		[]string{"worker_uid", "region"},
	)

	// WorkerJobsClaimed counts total jobs claimed by each worker.
	WorkerJobsClaimed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_worker_jobs_claimed_total",
			Help: "Total jobs claimed by worker",
		},
		[]string{"worker_uid", "region"},
	)

	// IncidentsActive tracks currently open incidents.
	IncidentsActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "solidping_incidents_active",
			Help: "Currently open incidents",
		},
		[]string{"organization"},
	)

	// IncidentsTotal counts total incidents created.
	IncidentsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "solidping_incidents_total",
			Help: "Total incidents created",
		},
		[]string{"organization", labelCheckType},
	)

	allCollectors = []prometheus.Collector{
		CheckExecutions, CheckDuration, SchedulingDelay,
		CheckUp, CheckStatusStreak, ChecksConfigured,
		WorkersActive, WorkerFreeRunners, WorkerJobsClaimed,
		IncidentsActive, IncidentsTotal,
	}
)

// Register registers all SolidPing metrics with the given registerer.
func Register(reg prometheus.Registerer) {
	for _, c := range allCollectors {
		reg.MustRegister(c)
	}
}
