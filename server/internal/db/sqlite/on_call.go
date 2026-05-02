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

// CreateOnCallSchedule inserts a new schedule.
func (s *Service) CreateOnCallSchedule(ctx context.Context, schedule *models.OnCallSchedule) error {
	_, err := s.db.NewInsert().Model(schedule).Exec(ctx)
	if err != nil {
		return fmt.Errorf("create on-call schedule: %w", err)
	}

	return nil
}

// GetOnCallSchedule fetches a schedule by UID, scoped to its organization.
func (s *Service) GetOnCallSchedule(
	ctx context.Context, orgUID, scheduleUID string,
) (*models.OnCallSchedule, error) {
	var schedule models.OnCallSchedule

	err := s.db.NewSelect().
		Model(&schedule).
		Where("uid = ?", scheduleUID).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get on-call schedule: %w", err)
	}

	return &schedule, nil
}

// GetOnCallScheduleBySlug looks up by (org, slug).
func (s *Service) GetOnCallScheduleBySlug(
	ctx context.Context, orgUID, slug string,
) (*models.OnCallSchedule, error) {
	var schedule models.OnCallSchedule

	err := s.db.NewSelect().
		Model(&schedule).
		Where("organization_uid = ?", orgUID).
		Where("slug = ?", slug).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get on-call schedule by slug: %w", err)
	}

	return &schedule, nil
}

// GetOnCallScheduleByICalSecret resolves the unauthenticated iCal feed URL
// to a schedule. Returns sql.ErrNoRows if the secret has been disabled or
// rotated.
func (s *Service) GetOnCallScheduleByICalSecret(
	ctx context.Context, secret string,
) (*models.OnCallSchedule, error) {
	if secret == "" {
		return nil, sql.ErrNoRows
	}

	var schedule models.OnCallSchedule

	err := s.db.NewSelect().
		Model(&schedule).
		Where("ical_secret = ?", secret).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get on-call schedule by ical secret: %w", err)
	}

	return &schedule, nil
}

// ListOnCallSchedules returns all schedules for an org, ordered by name.
func (s *Service) ListOnCallSchedules(
	ctx context.Context, orgUID string,
) ([]*models.OnCallSchedule, error) {
	var schedules []*models.OnCallSchedule

	err := s.db.NewSelect().
		Model(&schedules).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("name ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list on-call schedules: %w", err)
	}

	return schedules, nil
}

// UpdateOnCallSchedule writes the supplied fields. Empty update is a no-op.
func (s *Service) UpdateOnCallSchedule(
	ctx context.Context, scheduleUID string, update *models.OnCallScheduleUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.OnCallSchedule)(nil)).
		Where("uid = ?", scheduleUID).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	query = applyOnCallScheduleSets(query, update)

	if update.ClearDescription {
		query = query.Set("description = NULL")
	}

	if update.ClearHandoffWeekday {
		query = query.Set("handoff_weekday = NULL")
	}

	if update.ClearICalSecret {
		query = query.Set("ical_secret = NULL")
	}

	_, err := query.Exec(ctx)
	if err != nil {
		return fmt.Errorf("update on-call schedule: %w", err)
	}

	return nil
}

// applyOnCallScheduleSets writes Set() calls for the non-nil pointer fields.
func applyOnCallScheduleSets(query *bun.UpdateQuery, update *models.OnCallScheduleUpdate) *bun.UpdateQuery {
	if update.Slug != nil {
		query = query.Set("slug = ?", *update.Slug)
	}
	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}
	if update.Description != nil {
		query = query.Set("description = ?", *update.Description)
	}
	if update.Timezone != nil {
		query = query.Set("timezone = ?", *update.Timezone)
	}
	if update.RotationType != nil {
		query = query.Set("rotation_type = ?", *update.RotationType)
	}
	if update.HandoffTime != nil {
		query = query.Set("handoff_time = ?", *update.HandoffTime)
	}
	if update.HandoffWeekday != nil {
		query = query.Set("handoff_weekday = ?", *update.HandoffWeekday)
	}
	if update.StartAt != nil {
		query = query.Set("start_at = ?", *update.StartAt)
	}
	if update.ICalSecret != nil {
		query = query.Set("ical_secret = ?", *update.ICalSecret)
	}

	return query
}

// DeleteOnCallSchedule soft-deletes the schedule. Roster and overrides are
// cascade-deleted by FK on hard delete; for soft delete we leave them in
// place — they'll be cleaned up if the schedule is purged later.
func (s *Service) DeleteOnCallSchedule(ctx context.Context, scheduleUID string) error {
	now := time.Now()

	_, err := s.db.NewUpdate().
		Model((*models.OnCallSchedule)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", scheduleUID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete on-call schedule: %w", err)
	}

	return nil
}

// ListOnCallScheduleUsers returns the roster ordered by position.
func (s *Service) ListOnCallScheduleUsers(
	ctx context.Context, scheduleUID string,
) ([]*models.OnCallScheduleUser, error) {
	var users []*models.OnCallScheduleUser

	err := s.db.NewSelect().
		Model(&users).
		Where("schedule_uid = ?", scheduleUID).
		Order("position ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list on-call schedule users: %w", err)
	}

	return users, nil
}

// ReplaceOnCallScheduleUsers atomically rewrites the roster for a schedule.
// Replacing the whole list keeps positions consistent and avoids reorder
// bugs from partial diffs.
func (s *Service) ReplaceOnCallScheduleUsers(
	ctx context.Context, scheduleUID string, userUIDs []string,
) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewDelete().
			Model((*models.OnCallScheduleUser)(nil)).
			Where("schedule_uid = ?", scheduleUID).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("clear roster: %w", err)
		}

		if len(userUIDs) == 0 {
			return nil
		}

		rows := make([]*models.OnCallScheduleUser, 0, len(userUIDs))
		for i, userUID := range userUIDs {
			rows = append(rows, models.NewOnCallScheduleUser(scheduleUID, userUID, i))
		}

		_, err = tx.NewInsert().Model(&rows).Exec(ctx)
		if err != nil {
			return fmt.Errorf("insert roster: %w", err)
		}

		return nil
	})
}

// CreateOnCallScheduleOverride inserts a new override row.
func (s *Service) CreateOnCallScheduleOverride(
	ctx context.Context, override *models.OnCallScheduleOverride,
) error {
	_, err := s.db.NewInsert().Model(override).Exec(ctx)
	if err != nil {
		return fmt.Errorf("create on-call override: %w", err)
	}

	return nil
}

// ListOnCallScheduleOverrides returns overrides for a schedule, optionally
// bounded by a window.
func (s *Service) ListOnCallScheduleOverrides(
	ctx context.Context, scheduleUID string, from, until *time.Time,
) ([]*models.OnCallScheduleOverride, error) {
	var overrides []*models.OnCallScheduleOverride

	query := s.db.NewSelect().
		Model(&overrides).
		Where("schedule_uid = ?", scheduleUID).
		Order("start_at ASC")

	if until != nil {
		query = query.Where("start_at < ?", *until)
	}

	if from != nil {
		query = query.Where("end_at > ?", *from)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("list on-call overrides: %w", err)
	}

	return overrides, nil
}

// GetOnCallScheduleOverride fetches by override UID.
func (s *Service) GetOnCallScheduleOverride(
	ctx context.Context, overrideUID string,
) (*models.OnCallScheduleOverride, error) {
	var override models.OnCallScheduleOverride

	err := s.db.NewSelect().
		Model(&override).
		Where("uid = ?", overrideUID).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if err != nil {
		return nil, fmt.Errorf("get on-call override: %w", err)
	}

	return &override, nil
}

// DeleteOnCallScheduleOverride hard-deletes the row. Overrides are
// short-lived; soft-delete adds no value.
func (s *Service) DeleteOnCallScheduleOverride(ctx context.Context, overrideUID string) error {
	_, err := s.db.NewDelete().
		Model((*models.OnCallScheduleOverride)(nil)).
		Where("uid = ?", overrideUID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete on-call override: %w", err)
	}

	return nil
}
