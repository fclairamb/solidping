package models

import (
	"time"

	"github.com/google/uuid"
)

// JobStatus represents the status of a job.
type JobStatus string

const (
	// JobStatusPending indicates the job is waiting to be executed.
	JobStatusPending JobStatus = "pending"
	// JobStatusRunning indicates the job is currently executing.
	JobStatusRunning JobStatus = "running"
	// JobStatusSuccess indicates the job completed successfully.
	JobStatusSuccess JobStatus = "success"
	// JobStatusRetried indicates the job failed and a retry was created.
	JobStatusRetried JobStatus = "retried"
	// JobStatusFailed indicates the job failed permanently (no more retries).
	JobStatusFailed JobStatus = "failed"
)

// Job represents a background task that can be scheduled and executed.
type Job struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID *string    `bun:"organization_uid"`
	Type            string     `bun:"type,notnull"`
	Config          JSONMap    `bun:"config,type:jsonb,nullzero"`
	RetryCount      int        `bun:"retry_count,notnull,default:0"`
	ScheduledAt     time.Time  `bun:"scheduled_at,notnull,default:current_timestamp"`
	Status          JobStatus  `bun:"status,notnull,default:'pending'"`
	Output          JSONMap    `bun:"output,type:jsonb,nullzero"`
	PreviousJobUID  *string    `bun:"previous_job_uid"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewJob creates a new job with generated UID.
func NewJob(orgUID *string, jobType string) *Job {
	now := time.Now()

	return &Job{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Type:            jobType,
		Config:          make(JSONMap),
		RetryCount:      0,
		ScheduledAt:     now,
		Status:          JobStatusPending,
		Output:          make(JSONMap),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// JobUpdate represents fields that can be updated.
type JobUpdate struct {
	Config         *JSONMap
	RetryCount     *int
	ScheduledAt    *time.Time
	Status         *JobStatus
	Output         *JSONMap
	PreviousJobUID *string
}
