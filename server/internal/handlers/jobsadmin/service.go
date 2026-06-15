// Package jobsadmin provides admin/super-admin read-only views over the
// background jobs queue, including a system-wide view across all orgs (and
// system jobs where organization_uid IS NULL) and retry-chain traversal.
package jobsadmin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrJobNotFound is returned when a job cannot be found.
var ErrJobNotFound = errors.New("job not found")

// activeStatusSortValue keeps pending/running jobs at the top of the default
// ordering; everything else sorts after, by most-recent updated_at.
const activeStatusSortExpr = `CASE WHEN status IN ('pending','running') THEN 0 ELSE 1 END`

// Service provides admin read-only job queries.
type Service interface {
	// ListJobs lists background jobs. When orgUID is empty, all orgs are
	// returned (system mode, including organization_uid IS NULL system jobs).
	ListJobs(ctx context.Context, orgUID string, opts ListOptions) ([]*models.Job, error)
	// GetJob returns a single job. When orgUID is non-empty the job must
	// belong to that org or ErrJobNotFound is returned.
	GetJob(ctx context.Context, orgUID, uid string) (*models.Job, error)
	// RetryChain returns the ordered chain of jobs linked via previous_job_uid,
	// from the earliest ancestor to the latest descendant, that the given job
	// belongs to. The result always includes the job itself.
	RetryChain(ctx context.Context, orgUID, uid string) ([]*models.Job, error)
}

// ListOptions holds filtering and pagination options.
type ListOptions struct {
	Status string
	Type   string
	Limit  int
	Offset int
}

type serviceImpl struct {
	db *bun.DB
}

// NewService creates a new admin jobs service.
func NewService(db *bun.DB) Service {
	return &serviceImpl{db: db}
}

func (s *serviceImpl) ListJobs(ctx context.Context, orgUID string, opts ListOptions) ([]*models.Job, error) {
	var jobs []*models.Job

	query := s.db.NewSelect().
		Model(&jobs).
		Where("deleted_at IS NULL").
		OrderExpr(activeStatusSortExpr).
		Order("updated_at DESC")

	if orgUID != "" {
		query = query.Where("organization_uid = ?", orgUID)
	}

	if opts.Status != "" {
		query = query.Where("status = ?", opts.Status)
	}

	if opts.Type != "" {
		query = query.Where("type = ?", opts.Type)
	}

	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}

	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}

	return jobs, nil
}

func (s *serviceImpl) GetJob(ctx context.Context, orgUID, uid string) (*models.Job, error) {
	job, err := s.getJobInternal(ctx, orgUID, uid)
	if err != nil {
		return nil, err
	}

	return job, nil
}

func (s *serviceImpl) getJobInternal(ctx context.Context, orgUID, uid string) (*models.Job, error) {
	var job models.Job

	query := s.db.NewSelect().
		Model(&job).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL")

	if orgUID != "" {
		query = query.Where("organization_uid = ?", orgUID)
	}

	err := query.Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrJobNotFound, uid)
	} else if err != nil {
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	return &job, nil
}

func (s *serviceImpl) RetryChain(ctx context.Context, orgUID, uid string) ([]*models.Job, error) {
	root, err := s.getJobInternal(ctx, orgUID, uid)
	if err != nil {
		return nil, err
	}

	// Walk backward to the earliest ancestor.
	ancestors := []*models.Job{}
	cursor := root

	for cursor.PreviousJobUID != nil {
		prev, prevErr := s.getJobInternal(ctx, orgUID, *cursor.PreviousJobUID)
		if prevErr != nil {
			// Chain broken (e.g. ancestor deleted) — stop walking.
			break
		}

		ancestors = append([]*models.Job{prev}, ancestors...)
		cursor = prev
	}

	chain := make([]*models.Job, 0, len(ancestors)+1)
	chain = append(chain, ancestors...)
	chain = append(chain, root)

	// Walk forward following jobs that reference the current tail as previous.
	tail := root
	for {
		next, nextErr := s.nextJob(ctx, orgUID, tail.UID)
		if nextErr != nil {
			return nil, nextErr
		}

		if next == nil {
			break
		}

		chain = append(chain, next)
		tail = next
	}

	return chain, nil
}

// nextJob returns the job whose previous_job_uid equals prevUID, or nil if none.
func (s *serviceImpl) nextJob(ctx context.Context, orgUID, prevUID string) (*models.Job, error) {
	var job models.Job

	query := s.db.NewSelect().
		Model(&job).
		Where("previous_job_uid = ?", prevUID).
		Where("deleted_at IS NULL").
		Order("created_at ASC").
		Limit(1)

	if orgUID != "" {
		query = query.Where("organization_uid = ?", orgUID)
	}

	err := query.Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // sentinel for "no further job in chain"
	} else if err != nil {
		return nil, fmt.Errorf("failed to find next job in chain: %w", err)
	}

	return &job, nil
}
