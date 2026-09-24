package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TouchCheckLastResult advances last_result_at and returns the live row
// (spec 2026-09-25-02). See db.Service for why the touch precedes the read.
func (s *Service) TouchCheckLastResult(
	ctx context.Context, checkUID string, at time.Time,
) (*models.CheckLiveState, error) {
	at = at.UTC()

	if _, err := s.db.NewUpdate().
		Model((*models.Check)(nil)).
		Set("last_result_at = ?", at).
		Where("uid = ?", checkUID).
		Where("deleted_at IS NULL").
		Where("(last_result_at IS NULL OR last_result_at < ?)", at).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("touch check last result: %w", err)
	}

	return s.readCheckLiveState(ctx, checkUID)
}

func (s *Service) readCheckLiveState(ctx context.Context, checkUID string) (*models.CheckLiveState, error) {
	var state models.CheckLiveState

	err := s.db.NewSelect().
		TableExpr("checks").
		ColumnExpr("status, status_streak, status_changed_at, first_failure_at, first_success_since_failure_at").
		Where("uid = ?", checkUID).
		Where("deleted_at IS NULL").
		Scan(ctx, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // documented "check is gone" signal
	}

	if err != nil {
		return nil, fmt.Errorf("read check live state: %w", err)
	}

	return &state, nil
}

// ListStaleCandidates returns the sweep's candidates: the indexed floor
// predicate only (idx_checks_freshness). SQLite stores `period` as HH:MM:SS
// text with no interval arithmetic, so the exact max(3 × period, 5 min)
// threshold is applied by the caller — which it does on both engines anyway.
func (s *Service) ListStaleCandidates(ctx context.Context, now time.Time, limit int) ([]*models.Check, error) {
	now = now.UTC()
	floor := now.Add(-models.StaleThreshold(0))

	var checks []*models.Check

	err := s.db.NewSelect().
		Model(&checks).
		Where("deleted_at IS NULL").
		Where("enabled = ?", true).
		Where("internal = ?", false).
		Where("status <> ?", models.CheckStatusStale).
		Where("coalesce(last_result_at, created_at) < ?", floor).
		OrderExpr("coalesce(last_result_at, created_at) ASC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list stale candidates: %w", err)
	}

	return checks, nil
}

// MarkCheckStale is the guarded compare-and-set into CheckStatusStale.
func (s *Service) MarkCheckStale(
	ctx context.Context, checkUID string, oldStatus models.CheckStatus, cutoff, now time.Time,
) (bool, error) {
	res, err := s.db.NewUpdate().
		Model((*models.Check)(nil)).
		Set("status = ?", models.CheckStatusStale).
		Set("status_changed_at = ?", now.UTC()).
		Set("first_failure_at = NULL").
		Set("first_success_since_failure_at = NULL").
		Set("updated_at = ?", now.UTC()).
		Where("uid = ?", checkUID).
		Where("deleted_at IS NULL").
		Where("enabled = ?", true).
		Where("status = ?", oldStatus).
		Where("coalesce(last_result_at, created_at) < ?", cutoff.UTC()).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("mark check stale: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark check stale rows affected: %w", err)
	}

	return affected == 1, nil
}

// ListStaleCheckPlacements joins every stale check to its placement regions.
// LEFT JOIN: a stale check with no job row still counts, under region "".
func (s *Service) ListStaleCheckPlacements(ctx context.Context) ([]models.StaleCheckPlacement, error) {
	var rows []models.StaleCheckPlacement

	err := s.db.NewSelect().
		TableExpr("checks AS c").
		Join("LEFT JOIN check_jobs AS cj ON cj.check_uid = c.uid").
		ColumnExpr("c.uid AS check_uid, c.organization_uid, c.slug AS check_slug, c.name AS check_name").
		ColumnExpr("coalesce(cj.region, '') AS region, c.last_result_at, c.created_at").
		Where("c.status = ?", models.CheckStatusStale).
		Where("c.deleted_at IS NULL").
		Where("c.enabled = ?", true).
		OrderExpr("c.uid, region").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("list stale check placements: %w", err)
	}

	return rows, nil
}

// ListLastRealResultPerRegion returns the newest real raw result per region.
func (s *Service) ListLastRealResultPerRegion(
	ctx context.Context, orgUID, checkUID string,
) ([]models.RegionLastResult, error) {
	var rows []models.RegionLastResult

	err := s.db.NewSelect().
		TableExpr("results").
		ColumnExpr("coalesce(region, '') AS region, MAX(period_start) AS last_at").
		Where("organization_uid = ?", orgUID).
		Where("check_uid = ?", checkUID).
		Where("period_type = ?", models.PeriodTypeRaw).
		Where("status IN (?)", bun.List(models.RealResultStatuses())).
		GroupExpr("coalesce(region, '')").
		OrderExpr("region").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("last real result per region: %w", err)
	}

	return rows, nil
}
