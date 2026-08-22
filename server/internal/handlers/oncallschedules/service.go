// Package oncallschedules implements the on-call schedule domain: CRUD on
// schedules, rosters, and overrides, plus the rotation resolver that
// answers "who is on call right now". Notifications never call this package
// directly — they go through the escalation policies layer (separate spec)
// which uses the Resolver interface here.
package oncallschedules

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Service errors.
var (
	ErrScheduleNotFound       = errors.New("on-call schedule not found")
	ErrScheduleHasNoUsers     = errors.New("on-call schedule has no users in the rotation")
	ErrScheduleNotYetActive   = errors.New("on-call schedule has not started yet")
	ErrInvalidTimezone        = errors.New("invalid IANA timezone")
	ErrInvalidHandoffTime     = errors.New("invalid handoff time, expected HH:MM")
	ErrWeekdayRequired        = errors.New("handoffWeekday is required for weekly rotation")
	ErrWeekdayUnused          = errors.New("handoffWeekday is only allowed for weekly rotation")
	ErrInvalidRotationType    = errors.New("rotationType must be 'daily' or 'weekly'")
	ErrInvalidWeekday         = errors.New("handoffWeekday must be between 0 (Mon) and 6 (Sun)")
	ErrOverrideEndBeforeStart = errors.New("override end must be after start")
	ErrRotationOutOfBounds    = errors.New("rotation index exceeded safety bound")
)

// Resolver answers "who is on call for this schedule at time t". Exposed as
// an interface so the escalation-policies package can depend on the
// abstraction and mock it in tests.
type Resolver interface {
	Resolve(ctx context.Context, scheduleUID string, t time.Time) (*models.User, error)
}

// Service provides on-call schedule management.
type Service struct {
	db db.Service
}

// NewService builds a service with the given database backend.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// CreateScheduleInput captures all the fields needed to create a schedule
// plus its initial roster.
type CreateScheduleInput struct {
	OrganizationUID string
	Name            string
	Description     string
	Timezone        string
	RotationType    models.RotationType
	HandoffTime     string
	HandoffWeekday  *int
	StartAt         time.Time
	UserUIDs        []string
}

// CreateSchedule validates the input, persists the schedule, and writes the
// initial roster.
func (s *Service) CreateSchedule(ctx context.Context, input *CreateScheduleInput) (*models.OnCallSchedule, error) {
	err := validateScheduleParams(
		input.Timezone, input.RotationType, input.HandoffTime, input.HandoffWeekday,
	)
	if err != nil {
		return nil, err
	}

	schedule := models.NewOnCallSchedule(
		input.OrganizationUID, input.Name, input.Timezone, input.RotationType,
	)
	schedule.HandoffTime = input.HandoffTime
	schedule.HandoffWeekday = input.HandoffWeekday
	schedule.StartAt = input.StartAt

	if input.Description != "" {
		schedule.Description = &input.Description
	}

	if err := s.db.CreateOnCallSchedule(ctx, schedule); err != nil {
		return nil, err
	}

	if len(input.UserUIDs) > 0 {
		if err := s.db.ReplaceOnCallScheduleUsers(ctx, schedule.UID, input.UserUIDs); err != nil {
			return nil, err
		}
	}

	audit.Record(ctx, s.db, input.OrganizationUID, models.EventTypeOnCallScheduleCreated,
		auditTarget(schedule), models.JSONMap{
			"rotation_type": string(schedule.RotationType),
			"timezone":      schedule.Timezone,
			"user_count":    len(input.UserUIDs),
		})

	return schedule, nil
}

// auditTarget names an on-call schedule for the audit trail.
func auditTarget(schedule *models.OnCallSchedule) audit.Target {
	return audit.Target{Type: "oncall_schedule", UID: schedule.UID, Name: schedule.Name}
}

// auditSnapshot is the scalar shape the audit trail diffs an update against.
// The roster is a list, so it is reported as a changed field name (see
// UpdateSchedule) rather than as a from→to pair.
func auditSnapshot(schedule *models.OnCallSchedule) map[string]any {
	description := ""
	if schedule.Description != nil {
		description = *schedule.Description
	}

	weekday := -1
	if schedule.HandoffWeekday != nil {
		weekday = *schedule.HandoffWeekday
	}

	return map[string]any{
		"name":            schedule.Name,
		"description":     description,
		"timezone":        schedule.Timezone,
		"rotation_type":   string(schedule.RotationType),
		"handoff_time":    schedule.HandoffTime,
		"handoff_weekday": weekday,
		"start_at":        schedule.StartAt.UTC().Format(time.RFC3339),
	}
}

// GetScheduleByUID returns a schedule by UID within an organization.
func (s *Service) GetScheduleByUID(
	ctx context.Context, orgUID, scheduleUID string,
) (*models.OnCallSchedule, error) {
	schedule, err := s.db.GetOnCallSchedule(ctx, orgUID, scheduleUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}

	if err != nil {
		return nil, err
	}

	return schedule, nil
}

// ListSchedules returns all schedules for an organization.
func (s *Service) ListSchedules(ctx context.Context, orgUID string) ([]*models.OnCallSchedule, error) {
	return s.db.ListOnCallSchedules(ctx, orgUID)
}

// ListUsers returns the roster ordered by position.
func (s *Service) ListUsers(
	ctx context.Context, scheduleUID string,
) ([]*models.OnCallScheduleUser, error) {
	return s.db.ListOnCallScheduleUsers(ctx, scheduleUID)
}

// ReplaceUsers replaces the entire roster.
func (s *Service) ReplaceUsers(
	ctx context.Context, scheduleUID string, userUIDs []string,
) error {
	return s.db.ReplaceOnCallScheduleUsers(ctx, scheduleUID, userUIDs)
}

// UpdateScheduleInput holds optional updates. Use pointers so an explicit
// "set to value" is distinguishable from "leave unchanged".
type UpdateScheduleInput struct {
	Name           *string
	Description    *string
	Timezone       *string
	RotationType   *models.RotationType
	HandoffTime    *string
	HandoffWeekday *int
	StartAt        *time.Time
	UserUIDs       *[]string

	ClearDescription    bool
	ClearHandoffWeekday bool
}

// UpdateSchedule applies a partial update. If UserUIDs is non-nil the
// roster is rewritten; positions reset to the new order.
func (s *Service) UpdateSchedule(
	ctx context.Context, orgUID, scheduleUID string, input *UpdateScheduleInput,
) (*models.OnCallSchedule, error) {
	schedule, err := s.GetScheduleByUID(ctx, orgUID, scheduleUID)
	if err != nil {
		return nil, err
	}

	timezone := schedule.Timezone
	if input.Timezone != nil {
		timezone = *input.Timezone
	}

	rotation := schedule.RotationType
	if input.RotationType != nil {
		rotation = *input.RotationType
	}

	handoffTime := schedule.HandoffTime
	if input.HandoffTime != nil {
		handoffTime = *input.HandoffTime
	}

	weekday := schedule.HandoffWeekday
	if input.HandoffWeekday != nil {
		weekday = input.HandoffWeekday
	}

	if input.ClearHandoffWeekday {
		weekday = nil
	}

	if validateErr := validateScheduleParams(timezone, rotation, handoffTime, weekday); validateErr != nil {
		return nil, validateErr
	}

	update := &models.OnCallScheduleUpdate{
		Name:                input.Name,
		Description:         input.Description,
		Timezone:            input.Timezone,
		RotationType:        input.RotationType,
		HandoffTime:         input.HandoffTime,
		HandoffWeekday:      input.HandoffWeekday,
		StartAt:             input.StartAt,
		ClearDescription:    input.ClearDescription,
		ClearHandoffWeekday: input.ClearHandoffWeekday,
	}

	if updateErr := s.db.UpdateOnCallSchedule(ctx, schedule.UID, update); updateErr != nil {
		return nil, updateErr
	}

	if input.UserUIDs != nil {
		if replaceErr := s.db.ReplaceOnCallScheduleUsers(ctx, schedule.UID, *input.UserUIDs); replaceErr != nil {
			return nil, replaceErr
		}
	}

	refreshed, err := s.GetScheduleByUID(ctx, orgUID, scheduleUID)
	if err != nil {
		return nil, err
	}

	changed, safe := audit.Changes(auditSnapshot(schedule), auditSnapshot(refreshed))

	var extraFields []string
	if input.UserUIDs != nil {
		extraFields = append(extraFields, "users")
	}

	if len(changed) > 0 || len(extraFields) > 0 {
		audit.Record(ctx, s.db, orgUID, models.EventTypeOnCallScheduleUpdated,
			auditTarget(refreshed), audit.ChangePayload(changed, safe, extraFields, nil))
	}

	return refreshed, nil
}

// DeleteSchedule soft-deletes the schedule.
func (s *Service) DeleteSchedule(ctx context.Context, orgUID, scheduleUID string) error {
	schedule, err := s.GetScheduleByUID(ctx, orgUID, scheduleUID)
	if err != nil {
		return err
	}

	if deleteErr := s.db.DeleteOnCallSchedule(ctx, scheduleUID); deleteErr != nil {
		return deleteErr
	}

	audit.Record(ctx, s.db, orgUID, models.EventTypeOnCallScheduleDeleted, auditTarget(schedule), nil)

	return nil
}

// CreateOverrideInput captures the fields for an override.
type CreateOverrideInput struct {
	ScheduleUID  string
	UserUID      string
	StartAt      time.Time
	EndAt        time.Time
	Reason       string
	CreatedByUID string
}

// CreateOverride inserts an override.
func (s *Service) CreateOverride(
	ctx context.Context, input *CreateOverrideInput,
) (*models.OnCallScheduleOverride, error) {
	if !input.EndAt.After(input.StartAt) {
		return nil, ErrOverrideEndBeforeStart
	}

	override := models.NewOnCallScheduleOverride(input.ScheduleUID, input.UserUID, input.StartAt, input.EndAt)

	if input.Reason != "" {
		override.Reason = &input.Reason
	}

	if input.CreatedByUID != "" {
		override.CreatedByUID = &input.CreatedByUID
	}

	if err := s.db.CreateOnCallScheduleOverride(ctx, override); err != nil {
		return nil, err
	}

	return override, nil
}

// ListOverrides returns the overrides on a schedule, optionally bounded.
func (s *Service) ListOverrides(
	ctx context.Context, scheduleUID string, from, until *time.Time,
) ([]*models.OnCallScheduleOverride, error) {
	return s.db.ListOnCallScheduleOverrides(ctx, scheduleUID, from, until)
}

// DeleteOverride hard-deletes an override.
func (s *Service) DeleteOverride(ctx context.Context, overrideUID string) error {
	return s.db.DeleteOnCallScheduleOverride(ctx, overrideUID)
}

// EnableICalFeed creates a fresh secret and stores it. Returns the secret
// for inclusion in the feed URL.
func (s *Service) EnableICalFeed(ctx context.Context, orgUID, scheduleUID string) (string, error) {
	schedule, err := s.GetScheduleByUID(ctx, orgUID, scheduleUID)
	if err != nil {
		return "", err
	}

	secret, err := generateICalSecret()
	if err != nil {
		return "", err
	}

	if err := s.db.UpdateOnCallSchedule(ctx, schedule.UID, &models.OnCallScheduleUpdate{
		ICalSecret: &secret,
	}); err != nil {
		return "", err
	}

	return secret, nil
}

// DisableICalFeed clears the secret. Subscribed clients start returning 410.
func (s *Service) DisableICalFeed(ctx context.Context, orgUID, scheduleUID string) error {
	schedule, err := s.GetScheduleByUID(ctx, orgUID, scheduleUID)
	if err != nil {
		return err
	}

	return s.db.UpdateOnCallSchedule(ctx, schedule.UID, &models.OnCallScheduleUpdate{
		ClearICalSecret: true,
	})
}

// RotateICalFeed replaces the secret with a fresh one. The previous URL
// stops working; clients must re-subscribe with the returned new URL.
func (s *Service) RotateICalFeed(ctx context.Context, orgUID, scheduleUID string) (string, error) {
	// Same shape as Enable — the only semantic difference is "this overwrites
	// any existing secret". Caller decides whether that's what was intended.
	return s.EnableICalFeed(ctx, orgUID, scheduleUID)
}

// LookupScheduleByICalSecret resolves the public feed URL secret to a
// schedule. Used only by the unauthenticated feed handler. Returns
// ErrScheduleNotFound when the secret is empty, unknown, or the schedule
// has been soft-deleted.
func (s *Service) LookupScheduleByICalSecret(ctx context.Context, secret string) (*models.OnCallSchedule, error) {
	schedule, err := s.db.GetOnCallScheduleByICalSecret(ctx, secret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}

	if err != nil {
		return nil, err
	}

	return schedule, nil
}

func generateICalSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate ical secret: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func validateScheduleParams(timezone string, rotation models.RotationType, handoffTime string, weekday *int) error {
	if _, err := time.LoadLocation(timezone); err != nil {
		return ErrInvalidTimezone
	}

	if _, _, err := parseHandoffTime(handoffTime); err != nil {
		return err
	}

	switch rotation {
	case models.RotationTypeDaily:
		if weekday != nil {
			return ErrWeekdayUnused
		}
	case models.RotationTypeWeekly:
		if weekday == nil {
			return ErrWeekdayRequired
		}

		if *weekday < 0 || *weekday > 6 {
			return ErrInvalidWeekday
		}
	default:
		return ErrInvalidRotationType
	}

	return nil
}

// parseHandoffTime accepts HH:MM in 24h form.
func parseHandoffTime(value string) (int, int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, ErrInvalidHandoffTime
	}

	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, ErrInvalidHandoffTime
	}

	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, ErrInvalidHandoffTime
	}

	return hour, minute, nil
}
