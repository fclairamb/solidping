package jobsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	// maxRetryCount is the maximum number of retries allowed for a job.
	maxRetryCount = 2
)

// JobOptions contains options for creating a job.
type JobOptions struct {
	ScheduledAt *time.Time     // If nil, the job will be scheduled at the current time.
	BounceDelay *time.Duration // If provided, the job will be bounced after the delay if already exists.
}

// Service provides job queue operations.
type Service interface {
	// CreateJob creates a new job and notifies waiting runners.
	// If bounceDelay is provided:
	// - If a duplicate exists, updates its scheduled_at to now + bounceDelay
	// - If no duplicate, schedules the new job at now + bounceDelay
	CreateJob(
		ctx context.Context,
		orgUID, jobType string,
		config json.RawMessage,
		jobOptions *JobOptions,
	) (*models.Job, error)

	// GetJob retrieves a job by UID
	GetJob(ctx context.Context, uid string) (*models.Job, error)

	// ListJobs lists jobs with optional filtering
	ListJobs(ctx context.Context, orgUID string, opts ListJobsOptions) ([]*models.Job, error)

	// CancelJob cancels a pending job (soft delete)
	CancelJob(ctx context.Context, uid string) error

	// GetJobWait waits for and claims the next available job
	// Uses PostgreSQL LISTEN/NOTIFY for efficient waiting
	GetJobWait(ctx context.Context) (*models.Job, error)

	// UpdateJobStatus updates job status to running/success/failed
	UpdateJobStatus(ctx context.Context, job *models.Job, status models.JobStatus, output json.RawMessage) error

	// RetryJob creates a new job from a failed job
	// Copies config, increments nb_tries, sets previous_job_uid
	RetryJob(ctx context.Context, job *models.Job) (*models.Job, error)
}

// ListJobsOptions contains filtering options for listing jobs.
type ListJobsOptions struct {
	Type   string
	Status string
	Limit  int
	Offset int
}

// serviceImpl implements the Service interface.
type serviceImpl struct {
	db *bun.DB
}

// NewService creates a new job service.
func NewService(db *bun.DB) Service {
	return &serviceImpl{db: db}
}

// CreateJob creates a new job and notifies waiting runners.
// Uses JobOptions to configure scheduling behavior:
// - If ScheduledAt is provided, job is scheduled at that time.
// - If BounceDelay is provided, job is scheduled at now + bounceDelay.
// - If both are nil, job is scheduled immediately.
// - ScheduledAt takes precedence over BounceDelay if both are provided.
// If a duplicate pending job exists, updates its scheduled_at to the new time.
func (s *serviceImpl) CreateJob(
	ctx context.Context,
	orgUID, jobType string,
	config json.RawMessage,
	jobOptions *JobOptions,
) (*models.Job, error) {
	scheduledAt := s.calculateScheduledTime(jobOptions)

	configMap, err := s.parseJobConfig(config)
	if err != nil {
		return nil, err
	}

	// Try to find and update existing job
	existing, found, err := s.findAndUpdateExistingJob(ctx, orgUID, jobType, configMap, scheduledAt)
	if err != nil {
		return nil, err
	}

	if found {
		return existing, nil
	}

	// Create new job
	return s.createNewJob(ctx, orgUID, jobType, configMap, scheduledAt)
}

func (s *serviceImpl) calculateScheduledTime(jobOptions *JobOptions) time.Time {
	scheduledAt := time.Now()

	switch {
	case jobOptions != nil && jobOptions.ScheduledAt != nil:
		scheduledAt = *jobOptions.ScheduledAt
	case jobOptions != nil && jobOptions.BounceDelay != nil:
		scheduledAt = scheduledAt.Add(*jobOptions.BounceDelay)
	}

	return scheduledAt
}

func (s *serviceImpl) parseJobConfig(config json.RawMessage) (models.JSONMap, error) {
	var configMap models.JSONMap
	if len(config) > 0 {
		if err := json.Unmarshal(config, &configMap); err != nil {
			return nil, fmt.Errorf("invalid config: %w", err)
		}
	} else {
		configMap = make(models.JSONMap)
	}

	return configMap, nil
}

func (s *serviceImpl) findAndUpdateExistingJob(
	ctx context.Context,
	orgUID, jobType string,
	configMap models.JSONMap,
	scheduledAt time.Time,
) (*models.Job, bool, error) {
	var existing models.Job

	query := s.db.NewSelect().
		Model(&existing).
		Where("type = ?", jobType).
		Where("config = ?", configMap).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL")

	if orgUID == "" {
		query = query.Where("organization_uid IS NULL")
	} else {
		query = query.Where("organization_uid = ?", orgUID)
	}

	err := query.Scan(ctx)
	if err == nil {
		// Found existing pending job, update its scheduled_at
		existing.ScheduledAt = scheduledAt
		existing.UpdatedAt = time.Now()

		_, err = s.db.NewUpdate().
			Model(&existing).
			Column("scheduled_at", "updated_at").
			Where("uid = ?", existing.UID).
			Exec(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("failed to update existing job: %w", err)
		}

		return &existing, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to check existing job: %w", err)
	}

	return nil, false, nil
}

func (s *serviceImpl) createNewJob(
	ctx context.Context,
	orgUID, jobType string,
	configMap models.JSONMap,
	scheduledAt time.Time,
) (*models.Job, error) {
	var orgUIDPtr *string
	if orgUID != "" {
		orgUIDPtr = &orgUID
	}

	job := models.NewJob(orgUIDPtr, jobType)
	job.Config = configMap
	job.ScheduledAt = scheduledAt

	_, err := s.db.NewInsert().
		Model(job).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create job: %w", err)
	}

	// Send NOTIFY signal to wake up job runners if job is scheduled within 15 minutes
	// This is PostgreSQL-specific and will be ignored on SQLite (polling fallback is used)
	if time.Until(job.ScheduledAt) <= 15*time.Minute {
		_, _ = s.db.ExecContext(ctx, "NOTIFY jobs")
		// Ignore errors - NOTIFY is not available on SQLite, which uses polling instead
	}

	return job, nil
}

// GetJob retrieves a job by UID.
func (s *serviceImpl) GetJob(ctx context.Context, uid string) (*models.Job, error) {
	var job models.Job

	err := s.db.NewSelect().
		Model(&job).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrJobNotFound, uid)
	} else if err != nil {
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	return &job, nil
}

// ListJobs lists jobs with optional filtering.
func (s *serviceImpl) ListJobs(
	ctx context.Context,
	orgUID string,
	opts ListJobsOptions,
) ([]*models.Job, error) {
	query := s.db.NewSelect().
		Model((*models.Job)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC")

	if opts.Type != "" {
		query = query.Where("type = ?", opts.Type)
	}

	if opts.Status != "" {
		query = query.Where("status = ?", opts.Status)
	}

	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}

	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}

	var jobs []*models.Job

	err := query.Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}

	return jobs, nil
}

// CancelJob cancels a pending job (soft delete).
func (s *serviceImpl) CancelJob(ctx context.Context, uid string) error {
	now := time.Now()

	result, err := s.db.NewUpdate().
		Model((*models.Job)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", uid).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to cancel job: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return ErrJobNotPending
	}

	return nil
}

// GetJobWait waits for and claims the next available job.
// Uses PostgreSQL LISTEN/NOTIFY for efficient waiting.
func (s *serviceImpl) GetJobWait(ctx context.Context) (*models.Job, error) {
	// Try to claim a job immediately
	job, err := s.claimNextJob(ctx)
	if err == nil {
		return job, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// TODO: Add notification mechanism:
	// - NOTIFY/LISTEN on postgresql
	// - checkrunner's notifier logic (made generic)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			job, err := s.claimNextJob(ctx)
			if err == nil {
				return job, nil
			}

			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
	}
}

// claimNextJob attempts to claim the next available job.
// Uses SELECT FOR UPDATE SKIP LOCKED on PostgreSQL for efficiency.
// Uses optimistic locking on SQLite (no FOR UPDATE support).
func (s *serviceImpl) claimNextJob(ctx context.Context) (*models.Job, error) {
	var job models.Job

	// Check if database is PostgreSQL (supports FOR UPDATE SKIP LOCKED)
	_, isPostgres := s.db.Dialect().(*pgdialect.Dialect)

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, transaction bun.Tx) error {
		// Select next available job
		query := transaction.NewSelect().
			Model(&job).
			Where("status = ?", models.JobStatusPending).
			Where("scheduled_at <= ?", time.Now()).
			Where("deleted_at IS NULL").
			Order("scheduled_at ASC").
			Limit(1)

		// PostgreSQL: Use FOR UPDATE SKIP LOCKED for efficient row-level locking
		// SQLite: Will use optimistic locking instead
		if isPostgres {
			query = query.For("UPDATE SKIP LOCKED")
		}

		err := query.Scan(ctx)
		if err != nil {
			return err
		}

		// Update status to running (with optimistic locking check for SQLite)
		now := time.Now()

		result, err := transaction.NewUpdate().
			Model(&job).
			Set("status = ?", models.JobStatusRunning).
			Set("updated_at = ?", now).
			Where("uid = ?", job.UID).
			Where("status = ?", models.JobStatusPending). // Optimistic lock: only update if still pending
			Exec(ctx)
		if err != nil {
			return err
		}

		// For SQLite: Verify the update succeeded (optimistic locking)
		if !isPostgres {
			rows, err := result.RowsAffected()
			if err != nil {
				return err
			}

			if rows == 0 {
				// Job was claimed by another runner, return ErrNoRows to trigger retry
				return sql.ErrNoRows
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &job, nil
}

// UpdateJobStatus updates job status to running/success/failed.
func (s *serviceImpl) UpdateJobStatus(
	ctx context.Context,
	job *models.Job,
	status models.JobStatus,
	output json.RawMessage,
) error {
	now := time.Now()

	update := s.db.NewUpdate().
		Model(job).
		Set("status = ?", status).
		Set("updated_at = ?", now).
		Where("uid = ?", job.UID)

	if output != nil {
		var outputMap models.JSONMap
		if err := json.Unmarshal(output, &outputMap); err != nil {
			return fmt.Errorf("invalid output: %w", err)
		}

		update = update.Set("output = ?", outputMap)
	}

	_, err := update.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to update job status: %w", err)
	}

	// Update job object
	job.Status = status
	job.UpdatedAt = now

	return nil
}

// RetryJob creates a new job from a failed job.
// Copies config, increments retry_count, sets previous_job_uid.
func (s *serviceImpl) RetryJob(ctx context.Context, job *models.Job) (*models.Job, error) {
	if job.RetryCount >= maxRetryCount {
		return nil, ErrMaxRetriesReached
	}

	// Mark original job as retried
	err := s.UpdateJobStatus(ctx, job, models.JobStatusRetried, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to mark job as retried: %w", err)
	}

	// Create retry job
	retryJob := models.NewJob(job.OrganizationUID, job.Type)
	retryJob.Config = job.Config
	retryJob.RetryCount = job.RetryCount + 1
	retryJob.PreviousJobUID = &job.UID

	// Schedule with exponential backoff: 1min, 5min, 15min
	backoff := []time.Duration{1 * time.Minute, 5 * time.Minute, 15 * time.Minute}
	if job.RetryCount < len(backoff) {
		retryJob.ScheduledAt = time.Now().Add(backoff[job.RetryCount])
	}

	_, err = s.db.NewInsert().
		Model(retryJob).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create retry job: %w", err)
	}

	return retryJob, nil
}
