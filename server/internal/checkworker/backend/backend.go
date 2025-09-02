// Package backend provides the WorkerBackend interface for check worker
// operations, abstracting direct DB access from HTTP-based remote access.
package backend

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// SubmitResultRequest contains the data sent when a worker submits a
// check result.
type SubmitResultRequest struct {
	Status   int            `json:"status"`
	Duration float32        `json:"duration"`
	Metrics  map[string]any `json:"metrics,omitempty"`
	Output   map[string]any `json:"output,omitempty"`
}

// SubmitResultResponse is returned after a result is submitted.
type SubmitResultResponse struct {
	NextScheduledAt time.Time `json:"nextScheduledAt"`
}

// WorkerBackend abstracts how a check worker communicates with the
// master server.  The DirectBackend calls the database directly, while
// HTTPBackend calls the master API over HTTP.
type WorkerBackend interface {
	// Register registers or updates a worker and returns the persisted
	// record (possibly with a generated token on first registration).
	Register(
		ctx context.Context, worker *models.Worker,
	) (*models.Worker, error)

	// Heartbeat updates the worker's last_active_at timestamp.
	Heartbeat(ctx context.Context, workerUID string) error

	// ClaimJobs claims up to limit jobs for the given worker.
	ClaimJobs(
		ctx context.Context,
		workerUID string,
		region *string,
		limit int,
		maxAhead time.Duration,
	) ([]*models.CheckJob, error)

	// SubmitResult saves a check result and processes incidents.
	// Returns the next scheduled time for the job.
	SubmitResult(
		ctx context.Context,
		jobUID, workerUID string,
		req *SubmitResultRequest,
	) (*SubmitResultResponse, error)
}
