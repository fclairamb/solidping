// Package checkjobsvc provides check job queue operations for the distributed check runner system.
package checkjobsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrJobClaimedByAnother is returned when a job has been claimed by another worker.
var ErrJobClaimedByAnother = errors.New("job may have been claimed by another worker")

// Service provides check job queue operations.
type Service interface {
	// ClaimJobs atomically claims up to limit check jobs for the given worker.
	// Lease duration is calculated per job as scheduled_at + period + 30s.
	// Returns claimed jobs or nil if none available.
	ClaimJobs(
		ctx context.Context,
		workerUID string,
		region *string,
		limit int,
		maxAhead time.Duration,
	) ([]*models.CheckJob, error)

	// ReleaseLease releases the lease and reschedules the job for next execution.
	ReleaseLease(ctx context.Context, jobUID string, workerUID string, nextScheduledAt time.Time) error
}

// serviceImpl implements the Service interface.
type serviceImpl struct {
	db *bun.DB
}

// NewService creates a new check job service.
func NewService(db *bun.DB) Service {
	return &serviceImpl{db: db}
}

// ClaimJobs atomically claims check jobs using lease mechanism.
// Lease duration is calculated per job as scheduled_at + period + 30s.
// Uses SELECT FOR UPDATE SKIP LOCKED on PostgreSQL for efficient row-level locking.
// Uses optimistic locking on SQLite.
func (s *serviceImpl) ClaimJobs(
	ctx context.Context,
	workerUID string,
	region *string,
	limit int,
	maxAhead time.Duration,
) ([]*models.CheckJob, error) {
	var jobs []*models.CheckJob
	now := time.Now()

	// Check if database is PostgreSQL
	_, isPostgres := s.db.Dialect().(*pgdialect.Dialect)

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Build and execute selection query
		if err := s.selectAvailableJobs(ctx, tx, &jobs, region, limit, maxAhead, now, isPostgres); err != nil {
			return err
		}

		if len(jobs) == 0 {
			return nil // No jobs available
		}

		// Update each job with lease info
		return s.updateJobsWithLease(ctx, tx, jobs, workerUID, now, isPostgres)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // No jobs available
		}
		return nil, err
	}

	return jobs, nil
}

// selectAvailableJobs builds and executes the query to select available jobs.
func (s *serviceImpl) selectAvailableJobs(
	ctx context.Context,
	tx bun.Tx,
	jobs *[]*models.CheckJob,
	region *string,
	limit int,
	maxAhead time.Duration,
	now time.Time,
	isPostgres bool,
) error {
	query := tx.NewSelect().
		Model(jobs).
		Where("scheduled_at <= ?", now.Add(maxAhead)).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				WhereOr("lease_expires_at IS NULL").
				WhereOr("lease_expires_at < ?", now)
		}).
		Order("scheduled_at ASC").
		Limit(limit)

	// Region matching: NULL region or prefix matching
	// A worker with SP_REGION=eu-fr-paris claims jobs where region=eu-fr
	if region != nil {
		query = query.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				WhereOr("region IS NULL").
				WhereOr("? LIKE region || '%'", *region)
		})
	}

	// PostgreSQL: Use FOR UPDATE SKIP LOCKED for efficient row-level locking
	if isPostgres {
		query = query.For("UPDATE SKIP LOCKED")
	}

	if err := query.Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // No jobs available
		}
		return err
	}

	return nil
}

// updateJobsWithLease updates each job with lease information.
func (s *serviceImpl) updateJobsWithLease(
	ctx context.Context,
	tx bun.Tx,
	jobs []*models.CheckJob,
	workerUID string,
	now time.Time,
	isPostgres bool,
) error {
	for _, job := range jobs {
		if err := s.updateSingleJobLease(ctx, tx, job, workerUID, now, isPostgres); err != nil {
			return err
		}
	}

	return nil
}

// updateSingleJobLease updates a single job with lease information.
func (s *serviceImpl) updateSingleJobLease(
	ctx context.Context,
	tx bun.Tx,
	job *models.CheckJob,
	workerUID string,
	now time.Time,
	isPostgres bool,
) error {
	// Convert Period to time.Duration
	period := time.Duration(job.Period)

	// Calculate lease expiration: Use scheduled_at + period + 30s
	// This ensures the lease expires at a predictable time regardless of when the job is claimed
	latest := *job.ScheduledAt
	if now.After(latest) {
		latest = now
	}
	leaseExpiresAt := latest.Add(period + 30*time.Second)

	// Update the job
	result, err := tx.NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = ?", workerUID).
		Set("lease_expires_at = ?", leaseExpiresAt).
		Set("lease_starts = lease_starts + 1").
		Set("updated_at = ?", now).
		Where("uid = ?", job.UID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to update check job %s: %w", job.UID, err)
	}

	// For SQLite: Verify the update succeeded (optimistic locking)
	if !isPostgres {
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}

		if rows == 0 {
			// Job was claimed by another runner, return error to trigger retry
			return sql.ErrNoRows
		}
	}

	// Update the job object with new lease info for return
	job.LeaseWorkerUID = &workerUID
	job.LeaseExpiresAt = &leaseExpiresAt
	job.LeaseStarts++
	job.UpdatedAt = now

	return nil
}

// ReleaseLease releases the lease and reschedules the job.
// Resets lease_starts to 0 since the job completed successfully.
func (s *serviceImpl) ReleaseLease(
	ctx context.Context,
	jobUID string,
	workerUID string,
	nextScheduledAt time.Time,
) error {
	result, err := s.db.NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = NULL").
		Set("lease_expires_at = NULL").
		Set("lease_starts = 0"). // Reset since job completed
		Set("scheduled_at = ?", nextScheduledAt).
		Set("updated_at = ?", time.Now()).
		Where("uid = ?", jobUID).
		Where("lease_worker_uid = ?", workerUID). // Safety: only release if we own the lease
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to release lease: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("no rows updated: %w", ErrJobClaimedByAnother)
	}

	return nil
}
