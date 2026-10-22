// Package checkruns serves the multi-step run endpoints of a check (spec
// 2026-10-03-03): the run in progress, its cancellation, and the stored crawl
// reports.
package checkruns

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// Errors returned by the service.
var (
	ErrOrganizationNotFound = errors.New("organization not found")
	ErrCheckNotFound        = errors.New("check not found")
	ErrNotMultiStep         = errors.New("this check type does not run in steps")
)

// Store is the attachment side the endpoints need (attachments.Service).
type Store interface {
	ListCrawlReports(ctx context.Context, orgUID, checkUID string) ([]attachments.Response, error)
}

// TaskCanceler cancels a check's worker tasks (workertasks.Service).
type TaskCanceler interface {
	Cancel(ctx context.Context, filter workertasks.CancelFilter) (int, error)
}

// Service implements the endpoints.
type Service struct {
	db    db.Service
	store Store
	tasks TaskCanceler
}

// NewService builds the service.
func NewService(dbSvc db.Service, store Store, tasks TaskCanceler) *Service {
	return &Service{db: dbSvc, store: store, tasks: tasks}
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

// GetRun returns the check's run in progress: its active worker task (spec
// 2026-10-22-03), with the slices done and the progress of the last one.
func (s *Service) GetRun(ctx context.Context, orgSlug, identifier string) (*Run, error) {
	check, err := s.resolveCheck(ctx, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	task := new(models.WorkerTask)

	err = s.db.DB().NewSelect().Model(task).
		Where("check_uid = ?", check.UID).
		Where("status IN (?)", bun.List([]models.WorkerTaskStatus{
			models.WorkerTaskStatusPending, models.WorkerTaskStatusClaimed,
		})).
		Where("updated_at >= ?", time.Now().Add(-models.WorkerTaskStaleAfter)).
		OrderExpr("created_at DESC").
		Limit(1).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return &Run{Running: false}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("load the run in progress: %w", err)
	}

	return &Run{
		Running:   true,
		RunUID:    task.UID,
		StartedAt: task.RunStartedAt,
		Steps:     task.SliceCount,
		Progress:  task.Progress,
	}, nil
}

// CancelRun cancels the check's run in progress: its task is canceled (its
// state purged, a slice still running is fenced out at its next report) and
// the job is due at its next regular tick.
func (s *Service) CancelRun(ctx context.Context, orgSlug, identifier string) error {
	check, err := s.resolveCheck(ctx, orgSlug, identifier)
	if err != nil {
		return err
	}

	if !checkerdef.CheckType(check.Type).IsMultiStep() {
		return ErrNotMultiStep
	}

	if _, err := s.tasks.Cancel(ctx, workertasks.CancelFilter{CheckUID: check.UID}); err != nil {
		return fmt.Errorf("cancel run: %w", err)
	}

	return nil
}

// ListCrawlReports returns the check's last crawl reports with signed URLs.
func (s *Service) ListCrawlReports(ctx context.Context, orgSlug, identifier string) ([]attachments.Response, error) {
	check, err := s.resolveCheck(ctx, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	return s.store.ListCrawlReports(ctx, check.OrganizationUID, check.UID)
}
