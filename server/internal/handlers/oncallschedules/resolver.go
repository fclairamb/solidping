package oncallschedules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Resolve returns the user on call for the given schedule at time t. It
// honors overrides first, then walks the rotation. Soft-deleted users are
// skipped (rotation slot stays filled by the next non-deleted user).
//
// Implements the Resolver interface; the escalation-policies package
// depends on the interface, not this concrete implementation.
func (s *Service) Resolve(ctx context.Context, scheduleUID string, instant time.Time) (*models.User, error) {
	schedule, err := s.db.GetOnCallSchedule(ctx, "", scheduleUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}

	if err != nil {
		// The org-scoped fetch returns sql.ErrNoRows if scoping fails. Try
		// secret-style fetch as a no-org fallback.
		schedule, err = s.fetchScheduleAnyOrg(ctx, scheduleUID)
		if err != nil {
			return nil, err
		}
	}

	if schedule == nil {
		return nil, ErrScheduleNotFound
	}

	if instant.Before(schedule.StartAt) {
		return nil, ErrScheduleNotYetActive
	}

	if user, found, err := s.resolveOverride(ctx, scheduleUID, instant); err != nil {
		return nil, err
	} else if found {
		return user, nil
	}

	return s.resolveRotation(ctx, schedule, instant)
}

// fetchScheduleAnyOrg pulls a schedule by UID without an org scope, used
// internally when the resolver doesn't have one (e.g., escalation worker
// looking up by uid alone).
func (s *Service) fetchScheduleAnyOrg(ctx context.Context, scheduleUID string) (*models.OnCallSchedule, error) {
	overrides, err := s.db.ListOnCallScheduleOverrides(ctx, scheduleUID, nil, nil)
	if err != nil {
		return nil, err
	}

	if len(overrides) == 0 {
		return nil, ErrScheduleNotFound
	}

	return s.db.GetOnCallSchedule(ctx, "", scheduleUID)
}

func (s *Service) resolveOverride(
	ctx context.Context, scheduleUID string, instant time.Time,
) (*models.User, bool, error) {
	overrides, err := s.db.ListOnCallScheduleOverrides(ctx, scheduleUID, &instant, &instant)
	if err != nil {
		return nil, false, err
	}

	var winner *models.OnCallScheduleOverride

	for _, override := range overrides {
		if !covers(override, instant) {
			continue
		}

		if winner == nil || override.CreatedAt.After(winner.CreatedAt) {
			winner = override
		}
	}

	if winner == nil {
		return nil, false, nil
	}

	user, err := s.db.GetUser(ctx, winner.UserUID)
	if err != nil {
		return nil, false, fmt.Errorf("load override user: %w", err)
	}

	return user, true, nil
}

// covers tests whether an override applies at instant t. start is
// inclusive, end is exclusive.
func covers(override *models.OnCallScheduleOverride, instant time.Time) bool {
	return !instant.Before(override.StartAt) && instant.Before(override.EndAt)
}

func (s *Service) resolveRotation(
	ctx context.Context, schedule *models.OnCallSchedule, instant time.Time,
) (*models.User, error) {
	roster, err := s.db.ListOnCallScheduleUsers(ctx, schedule.UID)
	if err != nil {
		return nil, err
	}

	if len(roster) == 0 {
		return nil, ErrScheduleHasNoUsers
	}

	loc, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone: %w", err)
	}

	periodIndex, err := computePeriodIndex(schedule, instant, loc)
	if err != nil {
		return nil, err
	}

	// Walk the roster starting at the rotation slot, skipping soft-deleted
	// users until we find a live one. The loop is bounded by len(roster) so
	// an entirely-deleted rotation surfaces ErrScheduleHasNoUsers cleanly
	// instead of looping forever.
	pos := int(periodIndex % int64(len(roster)))

	for i := 0; i < len(roster); i++ {
		entry := roster[(pos+i)%len(roster)]

		user, err := s.db.GetUser(ctx, entry.UserUID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("load roster user: %w", err)
		}

		if user.DeletedAt != nil {
			continue
		}

		return user, nil
	}

	return nil, ErrScheduleHasNoUsers
}

// computePeriodIndex returns how many handoffs have happened between
// schedule.StartAt and t, in the schedule's local timezone. Walking
// handoff-by-handoff (rather than a single division) is DST-safe — adding
// one local week across a spring-forward keeps the handoff at HH:MM local.
func computePeriodIndex(schedule *models.OnCallSchedule, instant time.Time, loc *time.Location) (int64, error) {
	hour, minute, err := parseHandoffTime(schedule.HandoffTime)
	if err != nil {
		return 0, err
	}

	startLocal := schedule.StartAt.In(loc)
	atLocal := instant.In(loc)

	current := startLocal

	var idx int64

	guard := 0

	const maxIterations = 200_000 // ~547 years of daily, ~3,800 years of weekly

	for {
		next := nextHandoff(current, schedule.RotationType, schedule.HandoffWeekday, hour, minute, loc)
		if !next.Before(atLocal) && !next.Equal(atLocal) {
			return idx, nil
		}

		current = next
		idx++

		guard++
		if guard > maxIterations {
			return 0, ErrRotationOutOfBounds
		}
	}
}

// nextHandoff returns the next handoff strictly after current. Computed in
// local time so DST transitions don't drift the wall-clock handoff.
func nextHandoff(
	current time.Time,
	rotation models.RotationType,
	weekday *int,
	hour, minute int,
	loc *time.Location,
) time.Time {
	switch rotation {
	case models.RotationTypeDaily:
		next := time.Date(current.Year(), current.Month(), current.Day(), hour, minute, 0, 0, loc)
		for !next.After(current) {
			next = next.AddDate(0, 0, 1)
		}

		return next
	case models.RotationTypeWeekly:
		target := time.Monday
		if weekday != nil {
			target = mondayBased(*weekday)
		}

		next := time.Date(current.Year(), current.Month(), current.Day(), hour, minute, 0, 0, loc)
		for next.Weekday() != target || !next.After(current) {
			next = next.AddDate(0, 0, 1)
		}

		return next
	}

	return current
}

// mondayBased converts a Mon=0..Sun=6 index to time.Weekday (Sun=0..Sat=6).
func mondayBased(value int) time.Weekday {
	value %= 7
	if value < 0 {
		value += 7
	}

	return time.Weekday((value + 1) % 7)
}

// PreviewSlot is a single rotation slot in a preview window.
type PreviewSlot struct {
	UserUID string
	From    time.Time
	To      time.Time
}

// Preview returns the rotation slots intersecting [from, from+window]. The
// dashboard renders these as a calendar strip; the iCal feed turns each one
// into a VEVENT.
func (s *Service) Preview(
	ctx context.Context, scheduleUID string, from time.Time, window time.Duration,
) ([]PreviewSlot, error) {
	schedule, err := s.db.GetOnCallSchedule(ctx, "", scheduleUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}

	if err != nil {
		return nil, err
	}

	roster, err := s.db.ListOnCallScheduleUsers(ctx, scheduleUID)
	if err != nil {
		return nil, err
	}

	if len(roster) == 0 {
		return nil, ErrScheduleHasNoUsers
	}

	loc, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone: %w", err)
	}

	hour, minute, err := parseHandoffTime(schedule.HandoffTime)
	if err != nil {
		return nil, err
	}

	if from.Before(schedule.StartAt) {
		from = schedule.StartAt
	}

	until := from.Add(window)
	cursor := from

	periodIndex, err := computePeriodIndex(schedule, cursor, loc)
	if err != nil {
		return nil, err
	}

	slots := make([]PreviewSlot, 0, 16)

	for cursor.Before(until) {
		next := nextHandoff(cursor.In(loc), schedule.RotationType, schedule.HandoffWeekday, hour, minute, loc)
		end := next
		if end.After(until) {
			end = until
		}

		userUID := roster[int(periodIndex%int64(len(roster)))].UserUID
		slots = append(slots, PreviewSlot{
			UserUID: userUID,
			From:    cursor,
			To:      end,
		})

		cursor = next
		periodIndex++
	}

	return slots, nil
}
