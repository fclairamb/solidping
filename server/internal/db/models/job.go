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

// FinishedJobStatuses returns the terminal job statuses eligible for retention
// cleanup (jobs_cleanup stage 1 soft-delete). pending/running are deliberately
// excluded — recovering those is the stuck-job reaper's mandate, not cleanup's.
// A fresh slice each call keeps callers free to pass it straight into a query.
func FinishedJobStatuses() []JobStatus {
	return []JobStatus{JobStatusSuccess, JobStatusRetried, JobStatusFailed}
}

// Job represents a background task that can be scheduled and executed.
type Job struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"                        json:"uid"`
	OrganizationUID *string    `bun:"organization_uid"                               json:"organizationUid"`
	Type            string     `bun:"type,notnull"                                   json:"type"`
	Config          JSONMap    `bun:"config,type:jsonb,nullzero"                     json:"config"`
	RetryCount      int        `bun:"retry_count,notnull"                            json:"retryCount"`
	ScheduledAt     time.Time  `bun:"scheduled_at,notnull,default:current_timestamp" json:"scheduledAt"`
	Status          JobStatus  `bun:"status,notnull"                                 json:"status"`
	Output          JSONMap    `bun:"output,type:jsonb,nullzero"                     json:"output"`
	PreviousJobUID  *string    `bun:"previous_job_uid"                               json:"previousJobUid"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"   json:"createdAt"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"   json:"updatedAt"`
	DeletedAt       *time.Time `bun:"deleted_at"                                     json:"deletedAt,omitempty"`
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
