// Package workertasks is the shared mechanism for work a worker claims, holds
// under a lease, may run in several slices with server-held state in between,
// and reports back (spec 2026-10-22-02). Every kind (crawl, discovery chunks)
// rides the same table, lease, fencing rules and agent protocol.
//
// The worker pulls, the server never pushes. Cloud workers call the Service
// in-process; agents call it over signed HTTP (handlers/agenttasks). The
// server owns the state: a worker receives it in the claim and sends the next
// one back with the step, and nothing but this service writes it.
package workertasks

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Timings and limits of the mechanism.
const (
	// LeaseDuration is how long a claim or a heartbeat holds a task.
	LeaseDuration = 2 * time.Minute
	// MaxAttempts is how many claims a slice gets: the claim that would be
	// the next one fails the task with ErrorMaxAttempts instead.
	MaxAttempts = 3
	// MaxSliceFailures is how many failed slices in a row fail the task.
	MaxSliceFailures = 3
	// SliceRetryDelay is how long a failed slice waits before its next claim.
	SliceRetryDelay = time.Minute
	// MaxProgressBytes caps the progress summary a worker sends with a slice.
	MaxProgressBytes = 16 * 1024
	// maxErrorCodeLength caps the error code a worker reports.
	maxErrorCodeLength = 64
	// maxClaimTries bounds how many rows one Claim call goes through when
	// rows fail or defer on the way.
	maxClaimTries = 8
)

// Error codes the mechanism writes on a failed task.
const (
	// ErrorMaxAttempts is a task whose slice was claimed MaxAttempts times
	// without a report.
	ErrorMaxAttempts = "max_attempts"
	// ErrorStateMissing is a task whose state file cannot be read back.
	ErrorStateMissing = "state_missing"
	// ErrorSliceFailed is the code used when a worker reports a failure
	// without a valid code of its own.
	ErrorSliceFailed = "slice_failed"
)

// Errors of the service.
var (
	// ErrTaskGone means the task is not (or no longer) held by this worker:
	// another worker holds it, its lease expired, or it was cancelled or
	// ended. The worker must stop: nothing was written.
	ErrTaskGone = errors.New("worker task gone")
	// ErrTooLarge means the state or the progress exceeds its cap. Nothing
	// was written.
	ErrTooLarge = errors.New("worker task state or progress too large")
	// ErrInvalidIdentity means the worker identity cannot claim anything.
	ErrInvalidIdentity = errors.New("invalid worker identity")
	// ErrInvalidTask means a task to create is malformed.
	ErrInvalidTask = errors.New("invalid worker task")
	// ErrInvalidState means the state sent with a step is not JSON.
	ErrInvalidState = errors.New("worker task state must be JSON")
	// ErrNoStateStore means the service was built without a state store.
	ErrNoStateStore = errors.New("worker task state store not configured")
	// ErrUnknownKind means no kind is registered under the task's name.
	ErrUnknownKind = errors.New("unknown worker task kind")
)

// WorkerIdentity is who claims. A cloud worker serves every org of its region;
// an agent only its own org (OrgUID) in its exact region.
type WorkerIdentity struct {
	UID     string
	Region  string
	OrgUID  string
	IsAgent bool
	Kinds   []string
	Slots   int
}

// Prepared is what a kind adds to a claim.
type Prepared struct {
	// Input is the kind's input for the worker. Nil sends the task payload.
	Input json.RawMessage
	// SliceBudget is how long the slice may run.
	SliceBudget time.Duration
	// Units is how much work the slice may do (pages, hosts...).
	Units int
	// Final asks the worker to finish in this slice.
	Final bool
	// DeferUntil, when set, releases the claim without counting an attempt
	// and moves the task's not_before to this time.
	DeferUntil *time.Time
}

// TaskKind is the server side of a kind.
type TaskKind struct {
	// Name is the kind, as stored in worker_tasks.kind.
	Name string
	// PrepareClaim builds what the worker needs from the task and its
	// decoded state (nil on the first slice). Optional.
	PrepareClaim func(ctx context.Context, task *models.WorkerTask, state json.RawMessage) (*Prepared, error)
	// Complete validates and persists a finished task's result. A non-empty
	// error code marks the task failed with it (the kind already recorded the
	// failure, Fail is not called). Optional: nil marks the task done.
	Complete func(ctx context.Context, task *models.WorkerTask, body json.RawMessage) (errorCode string, err error)
	// Fail records a terminal failure decided by the mechanism (attempts,
	// slice failures, missing state). Optional.
	Fail func(ctx context.Context, task *models.WorkerTask, errorCode string) error
}

// Claim is what a worker receives for one slice. It is also the wire body of
// POST /api/v1/agent/tasks/claim.
type Claim struct {
	UID             string          `json:"uid"`
	Kind            string          `json:"kind"`
	OrganizationUID string          `json:"organizationUid"`
	Region          string          `json:"region"`
	CheckUID        string          `json:"checkUid,omitempty"`
	JobUID          string          `json:"jobUid,omitempty"`
	Slice           int             `json:"slice"`
	Attempt         int             `json:"attempt"`
	LeaseUntil      time.Time       `json:"leaseUntil"`
	Input           json.RawMessage `json:"input,omitempty"`
	State           json.RawMessage `json:"state,omitempty"`
	SliceBudgetMs   int64           `json:"sliceBudgetMs,omitempty"`
	Units           int             `json:"units,omitempty"`
	Final           bool            `json:"final,omitempty"`
}

// ClaimRequest is the body of POST /api/v1/agent/tasks/claim.
type ClaimRequest struct {
	Kinds []string `json:"kinds"`
}

// StepRequest is a non-final slice's report.
type StepRequest struct {
	State    json.RawMessage `json:"state"`
	Progress models.JSONMap  `json:"progress,omitempty"`
	Units    int             `json:"units,omitempty"`
}

// FailRequest is a failed slice's report.
type FailRequest struct {
	ErrorCode string `json:"errorCode"`
}

// HeartbeatResponse is the body of a successful heartbeat.
type HeartbeatResponse struct {
	LeaseUntil time.Time `json:"leaseUntil"`
}

// CancelFilter selects the tasks to cancel: those of one check or one job.
type CancelFilter struct {
	CheckUID string
	JobUID   string
}
