// Package crawltask runs the website crawl on worker tasks (spec
// 2026-10-22-03): one crawl run is one worker_tasks row of kind "crawl",
// claimed once per slice by a cloud worker or by an agent of the check's
// private location.
//
// The check_jobs row stays the schedule (period, "run now", lanes for the
// billing projections). The server side (Server) materializes a due job into a
// task, leases the job to it, reserves and refunds the org's rate-limit units,
// diffs and stores the report, and writes the run's one result. The worker
// side (Executor) runs one StepChecker slice per claim.
package crawltask

import (
	"encoding/json"
	"time"
)

// Names of the kind.
const (
	// Kind is the worker_tasks.kind of a crawl run.
	Kind = "crawl"
	// Capability is what a worker running the executor reports.
	Capability = "crawl"
)

// Run limits (spec 2026-10-03-03, unchanged).
const (
	// defaultMaxRunDuration is how long a run may last before its next slice
	// is final, when the config does not say.
	defaultMaxRunDuration = 30 * time.Minute
	// maxMaxRunDuration caps any configured run duration.
	maxMaxRunDuration = 2 * time.Hour
	// runLeaseMargin is added to the run's deadline for the job's lease: an
	// old node's bulk claim skips a job leased to a task.
	runLeaseMargin = 5 * time.Minute
	// rateDeferral is how far a run in progress is pushed back when the org's
	// bucket granted no unit: the bucket refills per minute.
	rateDeferral = time.Minute
	// materializeBatch bounds the due jobs one claim turns into tasks.
	materializeBatch = 10
)

// OutputKeyReportFileUID names the stored report of a finished run in the
// result output.
const OutputKeyReportFileUID = "reportFileUid"

// payloadJobUID is the task payload key naming the check_jobs row the run
// belongs to.
const payloadJobUID = "checkJobUid"

// Error codes of a slice, reported by the executor.
const (
	codeInvalidInput  = "invalid_input"
	codeInvalidConfig = "invalid_config"
	codeUnsupported   = "unsupported_type"
	codeStepFailed    = "step_failed"
	codeInvalidResult = "invalid_result"
)

// Input is the claim input of a crawl slice: the job's type and config.
type Input struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

// Result is the finish body of a crawl run: the checker's result, its report
// and the units the last slice used.
type Result struct {
	Status     int             `json:"status"`
	DurationMs float64         `json:"durationMs"`
	Metrics    map[string]any  `json:"metrics,omitempty"`
	Output     map[string]any  `json:"output,omitempty"`
	Report     json.RawMessage `json:"report,omitempty"`
	Units      int             `json:"units"`
}
