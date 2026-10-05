// Package checkruns serves the multi-step run endpoints of a check (spec
// 2026-10-03-03): the run in progress, its cancellation, and the stored crawl
// reports.
package checkruns

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
)

// Errors returned by the service.
var (
	ErrOrganizationNotFound = errors.New("organization not found")
	ErrCheckNotFound        = errors.New("check not found")
	ErrNotMultiStep         = errors.New("this check type does not run in steps")
)

// Store is the attachment side the endpoints need (attachments.Service).
type Store interface {
	CheckFileDetails(ctx context.Context, orgUID, checkUID, kind, fileUID string) (models.JSONMap, error)
	ListCrawlReports(ctx context.Context, orgUID, checkUID string) ([]attachments.Response, error)
	PurgeStepState(ctx context.Context, orgUID, checkUID string) error
}

// Service implements the endpoints.
type Service struct {
	db    db.Service
	store Store
}

// NewService builds the service.
func NewService(dbSvc db.Service, store Store) *Service {
	return &Service{db: dbSvc, store: store}
}

// Run is the run in progress of a multi-step check, or {"running": false}.
type Run struct {
	Running   bool           `json:"running"`
	RunUID    string         `json:"runUid,omitempty"`
	StartedAt *time.Time     `json:"startedAt,omitempty"`
	Steps     int            `json:"steps,omitempty"`
	Progress  map[string]any `json:"progress,omitempty"`
}

func (s *Service) resolveCheck(ctx context.Context, orgSlug, identifier string) (*models.Check, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return nil, ErrOrganizationNotFound
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return nil, ErrCheckNotFound
	}

	return check, nil
}

// GetRun returns the check's run in progress. Its progress is read from the
// current state file's details bag.
func (s *Service) GetRun(ctx context.Context, orgSlug, identifier string) (*Run, error) {
	check, err := s.resolveCheck(ctx, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	jobs, err := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
	if err != nil {
		return nil, fmt.Errorf("list check jobs: %w", err)
	}

	for _, job := range jobs {
		if job.StepRunUID == nil {
			continue
		}

		run := &Run{Running: true, RunUID: *job.StepRunUID, StartedAt: job.StepRunStartedAt, Steps: job.StepCount}

		if job.StepStateFileUID != nil {
			details, detailsErr := s.store.CheckFileDetails(
				ctx, check.OrganizationUID, check.UID, attachments.KindStepState, *job.StepStateFileUID,
			)
			if detailsErr == nil {
				if progress, ok := details[attachments.DetailKeyProgress].(map[string]any); ok {
					run.Progress = progress
				}
			}
		}

		return run, nil
	}

	return &Run{Running: false}, nil
}

// CancelRun drops the check's run in progress: the step_* columns are cleared,
// the state files purged and the job scheduled for its next regular tick. A
// slice still running is fenced out when it tries to save.
func (s *Service) CancelRun(ctx context.Context, orgSlug, identifier string) error {
	check, err := s.resolveCheck(ctx, orgSlug, identifier)
	if err != nil {
		return err
	}

	if !checkerdef.CheckType(check.Type).IsMultiStep() {
		return ErrNotMultiStep
	}

	jobs, err := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
	if err != nil {
		return fmt.Errorf("list check jobs: %w", err)
	}

	basePeriod := time.Duration(check.Period)
	spread := scheduling.RegionSpread(basePeriod, len(check.Regions), check.RegionSpreadDuration())

	for _, job := range jobs {
		if job.StepRunUID == nil {
			continue
		}

		next := scheduling.NextAligned(
			time.Now(), basePeriod, time.Duration(job.Period), check.UID, job.Region, check.Regions, spread,
		)

		_, updateErr := s.db.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("step_run_uid = NULL").
			Set("step_run_started_at = NULL").
			Set("step_count = 0").
			Set("step_state_file_uid = NULL").
			Set("step_failures = 0").
			Set("scheduled_at = ?", next).
			Set("effective_scheduled_at = ?", next).
			Set("updated_at = ?", time.Now()).
			Where("uid = ?", job.UID).
			Exec(ctx)
		if updateErr != nil {
			return fmt.Errorf("cancel run: %w", updateErr)
		}
	}

	return s.store.PurgeStepState(ctx, check.OrganizationUID, check.UID)
}

// ListCrawlReports returns the check's last crawl reports with signed URLs.
func (s *Service) ListCrawlReports(ctx context.Context, orgSlug, identifier string) ([]attachments.Response, error) {
	check, err := s.resolveCheck(ctx, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	return s.store.ListCrawlReports(ctx, check.OrganizationUID, check.UID)
}
