package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// WorkerTaskStatus is the lifecycle state of a worker_tasks row (spec 2026-10-22-02).
type WorkerTaskStatus string

const (
	// WorkerTaskStatusPending is a task waiting for a worker.
	WorkerTaskStatusPending WorkerTaskStatus = "pending"
	// WorkerTaskStatusClaimed is a task leased by a worker.
	WorkerTaskStatusClaimed WorkerTaskStatus = "claimed"
	// WorkerTaskStatusDone is a finished task.
	WorkerTaskStatusDone WorkerTaskStatus = "done"
	// WorkerTaskStatusFailed is a task that failed for good.
	WorkerTaskStatusFailed WorkerTaskStatus = "failed"
	// WorkerTaskStatusCancelled is a task canceled before it finished. The
	// spelling is the stored value, CHECK-constrained by the migration.
	WorkerTaskStatusCancelled WorkerTaskStatus = "cancelled" //nolint:misspell // stored value
)

// WorkerTaskStaleAfter is how long a non-terminal task may go untouched
// before readers count it as failed (lazy expiry, there is no sweeper).
const WorkerTaskStaleAfter = 24 * time.Hour

// WorkerTaskErrorExpired is the error code a reader reports for a stale task.
const WorkerTaskErrorExpired = "expired"

// WorkerTask is one unit of work a worker claims, holds under a lease, may run
// in several slices and reports back. Exactly one of CheckUID and JobUID is set.
type WorkerTask struct {
	bun.BaseModel `bun:"table:worker_tasks,alias:worker_task"`

	UID             string           `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string           `bun:"organization_uid,notnull"`
	Kind            string           `bun:"kind,notnull"`
	Region          string           `bun:"region,notnull"`
	CheckUID        *string          `bun:"check_uid"`
	JobUID          *string          `bun:"job_uid"`
	Payload         JSONMap          `bun:"payload,type:jsonb"`
	Progress        JSONMap          `bun:"progress,type:jsonb"`
	Status          WorkerTaskStatus `bun:"status,notnull"`
	WorkerUID       *string          `bun:"worker_uid"`
	LeaseUntil      *time.Time       `bun:"lease_until"`
	Attempts        int              `bun:"attempts,notnull"`
	SliceCount      int              `bun:"slice_count,notnull"`
	SliceFailures   int              `bun:"slice_failures,notnull"`
	StateFileUID    *string          `bun:"state_file_uid"`
	UnitsGranted    int              `bun:"units_granted,notnull"`
	NotBefore       time.Time        `bun:"not_before,notnull"`
	RunStartedAt    *time.Time       `bun:"run_started_at"`
	ErrorCode       *string          `bun:"error_code"`
	CreatedAt       time.Time        `bun:"created_at,notnull"`
	UpdatedAt       time.Time        `bun:"updated_at,notnull"`
}

// NewWorkerTask builds a pending task of a check. Use JobUID instead of
// CheckUID for a task whose parent is a job.
func NewWorkerTask(orgUID, kind, region string) *WorkerTask {
	now := time.Now().UTC()

	return &WorkerTask{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Kind:            kind,
		Region:          region,
		Status:          WorkerTaskStatusPending,
		NotBefore:       now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// IsActive reports whether the task still waits for, or runs on, a worker.
func (t *WorkerTask) IsActive() bool {
	return t.Status == WorkerTaskStatusPending || t.Status == WorkerTaskStatusClaimed
}

// IsStale reports whether a non-terminal task went untouched for
// WorkerTaskStaleAfter: readers count it as failed.
func (t *WorkerTask) IsStale(now time.Time) bool {
	return t.IsActive() && now.Sub(t.UpdatedAt) > WorkerTaskStaleAfter
}
