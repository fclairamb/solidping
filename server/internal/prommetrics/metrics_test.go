package prommetrics_test

import (
	"runtime"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// TestMetrics tests all metric registration and recording in a single test
// because the metrics are package-level globals that share state.
func TestMetrics(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	reg := prometheus.NewRegistry()

	// Registration should not panic
	r.NotPanics(func() {
		prommetrics.Register(reg)
	})

	// Record an execution
	prommetrics.RecordExecution("http", "up", "us-east-1", "default", 150.5)

	families, err := reg.Gather()
	r.NoError(err)
	r.NotEmpty(families)

	// Verify execution counter exists
	names := make(map[string]bool, len(families))
	for _, f := range families {
		names[f.GetName()] = true
	}

	r.True(names["solidping_check_executions_total"], "missing solidping_check_executions_total")
	r.True(names["solidping_check_duration_seconds"], "missing solidping_check_duration_seconds")

	// Runtime + process collectors (A1) must be registered by Register so both
	// the API server and worker expose heap/RSS/goroutine series on /metrics.
	r.True(names["go_memstats_heap_inuse_bytes"], "missing go_memstats_heap_inuse_bytes (Go collector)")
	r.True(names["go_goroutines"], "missing go_goroutines (Go collector)")
	// process_* depends on the OS; assert on platforms where it is supported
	// (the process collector silently emits nothing on unsupported OSes).
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		r.True(names["process_resident_memory_bytes"], "missing process_resident_memory_bytes (process collector)")
	}

	// Record scheduling delay
	prommetrics.RecordSchedulingDelay("eu-west-1", 2.5)

	families, err = reg.Gather()
	r.NoError(err)

	found := false
	for _, f := range families {
		if f.GetName() == "solidping_check_scheduling_delay_seconds" {
			found = true
			r.Equal(uint64(1), f.GetMetric()[0].GetHistogram().GetSampleCount())
		}
	}

	r.True(found, "solidping_check_scheduling_delay_seconds metric not found")

	// Set check status to UP
	prommetrics.SetCheckStatus("my-check", "http", "us-east-1", "default", true)

	families, err = reg.Gather()
	r.NoError(err)

	for _, f := range families {
		if f.GetName() == "solidping_check_up" {
			r.InDelta(1.0, f.GetMetric()[0].GetGauge().GetValue(), 0.001)
		}
	}

	// Set check status to DOWN
	prommetrics.SetCheckStatus("my-check", "http", "us-east-1", "default", false)

	families, err = reg.Gather()
	r.NoError(err)

	for _, f := range families {
		if f.GetName() == "solidping_check_up" {
			r.InDelta(0.0, f.GetMetric()[0].GetGauge().GetValue(), 0.001)
		}
	}

	// Set checks configured
	prommetrics.SetChecksConfigured("http", "default", "true", 5)

	families, err = reg.Gather()
	r.NoError(err)

	for _, f := range families {
		if f.GetName() == "solidping_checks_configured" {
			r.InDelta(5.0, f.GetMetric()[0].GetGauge().GetValue(), 0.001)
		}
	}

	// Record incident
	prommetrics.RecordIncidentCreated("default", "http")

	families, err = reg.Gather()
	r.NoError(err)

	for _, f := range families {
		if f.GetName() == "solidping_incidents_total" {
			r.InDelta(1.0, f.GetMetric()[0].GetCounter().GetValue(), 0.001)
		}
	}

	// Record background-job metrics.
	prommetrics.RecordJobProcessed("email", "success")
	prommetrics.RecordJobDuration("email", "success", 0.42)
	prommetrics.RecordJobSchedulingDelay("email", 1.5)
	prommetrics.SetJobsQueueDepth("pending", 7)

	families, err = reg.Gather()
	r.NoError(err)

	names = make(map[string]bool, len(families))
	for _, f := range families {
		names[f.GetName()] = true
	}

	r.True(names["solidping_jobs_processed_total"], "missing solidping_jobs_processed_total")
	r.True(names["solidping_job_duration_seconds"], "missing solidping_job_duration_seconds")
	r.True(names["solidping_job_scheduling_delay_seconds"], "missing solidping_job_scheduling_delay_seconds")
	r.True(names["solidping_jobs_queue_depth"], "missing solidping_jobs_queue_depth")

	for _, f := range families {
		switch f.GetName() {
		case "solidping_jobs_processed_total":
			r.InDelta(1.0, f.GetMetric()[0].GetCounter().GetValue(), 0.001)
		case "solidping_job_duration_seconds":
			r.Equal(uint64(1), f.GetMetric()[0].GetHistogram().GetSampleCount())
		case "solidping_job_scheduling_delay_seconds":
			r.Equal(uint64(1), f.GetMetric()[0].GetHistogram().GetSampleCount())
		case "solidping_jobs_queue_depth":
			r.InDelta(7.0, f.GetMetric()[0].GetGauge().GetValue(), 0.001)
		}
	}
}
