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
	// StepRunUID, when set, marks the result as the end of a multi-step run
	// (spec 2026-10-03-03): the release clears the job's step_* columns,
	// fenced on the run the claim saw, instead of a plain release.
	StepRunUID *string `json:"stepRunUid,omitempty"`
}

// PrivateLocationReader is the extra read the private-location liveness
// monitor needs (spec 2026-09-25-05). It is deliberately NOT part of
// WorkerBackend: only a backend with database access implements it
// (DirectBackend, i.e. the jobs node), so a deported agent — which could never
// see its own absence anyway — cannot evaluate the monitor even if a job
// reached it.
type PrivateLocationReader interface {
	// PrivateLocationAgents returns every non-deleted agent of the org bound to
	// exactly this private region (`@<slug>`), active and revoked.
	PrivateLocationAgents(ctx context.Context, orgUID, region string) ([]*models.Agent, error)
	// LastAgentDisconnect returns the newest `agent.disconnected` event of an
	// agent of this private region, or nil when there is none.
	LastAgentDisconnect(ctx context.Context, orgUID, region string) (*models.Event, error)
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
	// total capacity, slowLimit the slow-lane budget, bulkLimit the bulk-lane
	// budget before the slow claims are deducted — see
	// checkjobsvc.Service.ClaimJobs). A remote backend ignores bulkLimit: no
	// multi-step job ever reaches an agent (spec 2026-10-03-03). The second return is the next-eligible
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
		bulkLimit int,
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

	// LastSignals returns, per check, the newest raw row that was written by
	// an INBOUND SIGNAL — a heartbeat POST or an incoming email — never a
	// worker-written evaluation row. Passive checks (heartbeat/email) use it
	// to measure how long ago the last signal actually landed.
	//
	// The distinction is load-bearing (spec 2026-09-02-03): the passive
	// evaluator writes a raw row of its own every period, so a "newest row of
	// any origin" lookup makes it re-anchor on its own predecessor from the
	// second tick after a beat onwards — overdue detection degrades into a
	// coin flip on claim jitter, lastSignalAt reports the previous
	// evaluation's timestamp instead of the beat's, and the stale-run
	// (2×period) branch becomes unreachable.
	//
	// Remote backends may not support it — passive checks are a server-side
	// concern.
	LastSignals(ctx context.Context, orgUID string, checkUIDs []string) (map[string]*models.Result, error)

	// Hints returns a fresh channel signaled when new jobs may be available
	// (check.created events in-process; jobs-available frames over WS). Each
	// call returns an independent subscription.
	Hints() <-chan string
}

// SubmitStepRequest is the write of one non-final multi-step slice (spec
// 2026-10-03-03). The slice's progress travels with the state file (its
// details bag), not here.
type SubmitStepRequest struct {
	RunUID       string
	RunStartedAt time.Time
	// StateFileUID is the state file the slice just wrote; nil keeps the
	// previous one (a failed slice).
	StateFileUID *string
	// Failed counts the slice as failed.
	Failed bool
	// NextAt is when the next slice is due.
	NextAt time.Time
}

// SaveStepStateRequest is one state file to write.
type SaveStepStateRequest struct {
	RunUID   string
	Step     int
	Payload  []byte // the checker's state (JSON)
	Progress map[string]any
}

// StepBackend is the optional backend surface multi-step checks need (spec
// 2026-10-03-03). Only DirectBackend implements it: multi-step checks run on
// the server's own workers, never on an agent, so a worker whose backend
// lacks it refuses the job with an error result.
type StepBackend interface {
	// LoadStepState returns the checker payload of the job's current state
	// file. A missing or corrupt file (or one of another run) is an error:
	// the worker then restarts the run from a nil state.
	LoadStepState(ctx context.Context, job *models.CheckJob) ([]byte, error)
	// SaveStepState writes a new state file (the previous one is kept) and
	// returns its uid. ErrStepStateTooLarge when the envelope is over the cap.
	SaveStepState(ctx context.Context, job *models.CheckJob, req *SaveStepStateRequest) (string, error)
	// SubmitStep releases the lease after a non-final slice, fenced on the
	// lease and on the run the claim saw.
	SubmitStep(ctx context.Context, job *models.CheckJob, workerUID string, req *SubmitStepRequest) error
	// SaveStepReport stores a finished run's report and returns its file uid.
	SaveStepReport(ctx context.Context, job *models.CheckJob, runUID string, report []byte) (string, error)
	// PreviousStepReport returns the newest stored report of the check (nil
	// when none), read before the new one is stored.
	PreviousStepReport(ctx context.Context, job *models.CheckJob) ([]byte, error)
}
