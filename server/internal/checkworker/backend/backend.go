// Package backend provides the WorkerBackend interface that abstracts how the
// check worker loop (internal/checkworker) talks to the master: DirectBackend
// calls the database and services in-process (the production server path),
// WSBackend (agent mode) speaks the WebSocket agent protocol to a remote
// server. The lease/lane loop, budgets, and express path in CheckWorker run
// identically on top of either.
package backend

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// SchedulingState carries the post-exec cost/lane write folded into the lease
// release (specs 2026-06-30-09 / 2026-07-01-03). Nil on paths without a fresh
// cost sample (rate-limit deferral, passive checks, error results), which use a
// plain release instead.
type SchedulingState struct {
	CostEWMAMs           float64   `json:"costEwmaMs"`
	DelayEWMAMs          float64   `json:"delayEwmaMs"`
	EffectiveScheduledAt time.Time `json:"effectiveScheduledAt"`
	Lane                 uint8     `json:"lane"`
}

// SubmitResultRequest is the terminal write for one executed job: the result
// row plus the lease release/reschedule, submitted as a single backend call so
// a remote transport can carry it in one frame.
type SubmitResultRequest struct {
	Status   int            `json:"status"`
	Duration float32        `json:"duration"` // milliseconds
	Metrics  map[string]any `json:"metrics,omitempty"`
	Output   map[string]any `json:"output,omitempty"`
	// Diagnostics carries the opt-in failure capture (spec 2026-08-20-01).
	// Deliberately separate from Output: Output is persisted on the raw result
	// row, this is persisted only if the result opens/reopens an incident.
	Diagnostics *checkerdef.Diagnostics `json:"diagnostics,omitempty"`
	// Region is the resolved region for the result row (job region, falling
	// back to the worker's own region).
	Region *string `json:"region,omitempty"`
	// NextScheduledAt is the worker-computed next tick (phase-locked).
	NextScheduledAt time.Time `json:"nextScheduledAt"`
	// ExecStart is when the outbound probe actually began. The in-process path
	// folds it into Sched itself; a remote transport ships it so the SERVER can
	// compute the same delay sample (spec 2026-07-27-01). Zero on paths with no
	// probe (error results, rate-limit deferral).
	ExecStart time.Time `json:"execStart,omitempty"`
	// Sched, when non-nil, releases the lease with the updated scheduling
	// state; nil uses the plain release.
	Sched *SchedulingState `json:"sched,omitempty"`
}

// WorkerBackend abstracts how a check worker communicates with the master.
// CheckWorker consumes exactly this interface, so the same loop runs in-process
// (DirectBackend) and inside a deported agent (WSBackend).
type WorkerBackend interface {
	// Register registers or updates the worker identity and returns the
	// persisted record.
	Register(ctx context.Context, worker *models.Worker) (*models.Worker, error)

	// Heartbeat updates the worker's last_active_at timestamp and refreshes the
	// self-reported egress families (spec 2026-08-15-11). Reporting on the
	// heartbeat rather than at process start is what lets a host that gains or
	// loses IPv6 converge within one beat instead of needing a restart. The
	// value is advertised as a hint only — it never gates execution.
	// Heartbeat refreshes liveness and, when the executor reported one, its
	// capability set. A nil set means "not reported" and leaves the stored set
	// untouched; an empty non-nil set is a real report of "none". version is
	// this worker's self-reported build version (spec 2026-08-19-07); an
	// empty string means "not reported" and leaves the stored value untouched
	// — a real version is never the empty string, so this sentinel is safe.
	Heartbeat(ctx context.Context, workerUID string, capabilities []string, version string) error

	// ClaimJobs claims due jobs with per-lane reservation (fastLimit is the
	// total capacity, slowLimit the slow-lane budget — see
	// checkjobsvc.Service.ClaimJobs). The second return is the next-eligible
	// hint: how long until the earliest still-unleased job in this worker's
	// scope becomes claimable (0 = none known). The fetcher sleeps on it
	// instead of its flat fallback poll, which is what keeps sub-minute
	// periods honest on an otherwise idle worker or deported agent.
	ClaimJobs(
		ctx context.Context,
		workerUID string,
		region *string,
		fastLimit int,
		slowLimit int,
		maxAhead time.Duration,
	) ([]*models.CheckJob, time.Duration, error)

	// ClaimJobsForCheck claims any due job rows for one check (the express
	// path for freshly created checks).
	ClaimJobsForCheck(
		ctx context.Context,
		workerUID string,
		region *string,
		checkUID string,
	) ([]*models.CheckJob, error)

	// SubmitResult persists a finished execution: saves the result row,
	// processes incidents (always server-side), and releases the lease with
	// the given schedule (and scheduling state when provided).
	SubmitResult(
		ctx context.Context,
		job *models.CheckJob,
		workerUID string,
		req *SubmitResultRequest,
	) error

	// DeferRateLimited releases a job's lease and reschedules it without writing
	// a result, for the one caller that needs it: the per-org
	// MaxChecksPerMinute gate turning a job away before its probe runs.
	//
	// The name is deliberate. The underlying write preserves the job's
	// effective_scheduled_at (the claim ordering key) so a deferred job keeps
	// accumulating overdue-ness and wins the next contended slot — the
	// anti-starvation rotation of spec 2026-08-26-02. A generic "release the
	// lease" here is what previously re-anchored that key and starved the same
	// checks forever.
	DeferRateLimited(
		ctx context.Context,
		job *models.CheckJob,
		workerUID string,
		nextScheduledAt time.Time,
	) error

	// LastResults returns the latest result per check, used by passive checks
	// (heartbeat/email) to inspect inbound signal recency. Remote backends may
	// not support it — passive checks are a server-side concern.
	LastResults(ctx context.Context, orgUID string, checkUIDs []string) (map[string]*models.Result, error)

	// Hints returns a fresh channel signaled when new jobs may be available
	// (check.created events in-process; jobs-available frames over WS). Each
	// call returns an independent subscription.
	Hints() <-chan string
}
