package models

import (
	"time"

	"github.com/google/uuid"
)

// RotationType is the cadence at which a schedule rotates between users.
type RotationType string

const (
	// RotationTypeDaily rotates every day at handoff_time.
	RotationTypeDaily RotationType = "daily"
	// RotationTypeWeekly rotates once a week, at handoff_time on handoff_weekday.
	RotationTypeWeekly RotationType = "weekly"
)

// OnCallSchedule is a rotation: a list of users plus a cadence and timezone
// that, together with a moment in time, resolves to one user (the "currently
// on call"). The schedule itself does not page anyone — escalation policies
// (separate spec) consume schedules at fan-out time.
type OnCallSchedule struct {
	UID             string       `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string       `bun:"organization_uid,notnull"`
	Slug            string       `bun:"slug,notnull"`
	Name            string       `bun:"name,notnull"`
	Description     *string      `bun:"description"`
	Timezone        string       `bun:"timezone,notnull"`
	RotationType    RotationType `bun:"rotation_type,notnull"`
	HandoffTime     string       `bun:"handoff_time,notnull"` // HH:MM in schedule timezone
	HandoffWeekday  *int         `bun:"handoff_weekday"`      // 0–6 (Mon=0); required for weekly
	StartAt         time.Time    `bun:"start_at,notnull"`     // First handoff in the rotation cycle
	ICalSecret      *string      `bun:"ical_secret"`          // NULL = feed disabled
	CreatedAt       time.Time    `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time    `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time   `bun:"deleted_at"`
}

// NewOnCallSchedule builds a schedule with a fresh UID; caller fills the
// remaining fields.
func NewOnCallSchedule(orgUID, slug, name, timezone string, rotation RotationType) *OnCallSchedule {
	now := time.Now()

	return &OnCallSchedule{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Slug:            slug,
		Name:            name,
		Timezone:        timezone,
		RotationType:    rotation,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// OnCallScheduleUpdate captures the writable fields of a schedule. Pointer
// semantics: nil = unchanged, non-nil = set.
type OnCallScheduleUpdate struct {
	Slug           *string
	Name           *string
	Description    *string
	Timezone       *string
	RotationType   *RotationType
	HandoffTime    *string
	HandoffWeekday *int
	StartAt        *time.Time
	ICalSecret     *string

	ClearDescription    bool
	ClearHandoffWeekday bool
	ClearICalSecret     bool
}

// OnCallScheduleUser is one row in a schedule's ordered roster.
type OnCallScheduleUser struct {
	UID         string    `bun:"uid,pk,type:varchar(36)"`
	ScheduleUID string    `bun:"schedule_uid,notnull"`
	UserUID     string    `bun:"user_uid,notnull"`
	Position    int       `bun:"position,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt   time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewOnCallScheduleUser builds a roster entry with a fresh UID.
func NewOnCallScheduleUser(scheduleUID, userUID string, position int) *OnCallScheduleUser {
	now := time.Now()

	return &OnCallScheduleUser{
		UID:         uuid.New().String(),
		ScheduleUID: scheduleUID,
		UserUID:     userUID,
		Position:    position,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// OnCallScheduleOverride is a time-bounded replacement of the rotation's
// next on-call user. Overlapping overrides resolve to the most recently
// created (documented behavior, not validated at write time).
type OnCallScheduleOverride struct {
	UID          string    `bun:"uid,pk,type:varchar(36)"`
	ScheduleUID  string    `bun:"schedule_uid,notnull"`
	UserUID      string    `bun:"user_uid,notnull"`
	StartAt      time.Time `bun:"start_at,notnull"` // inclusive
	EndAt        time.Time `bun:"end_at,notnull"`   // exclusive
	Reason       *string   `bun:"reason"`
	CreatedByUID *string   `bun:"created_by_uid"`
	CreatedAt    time.Time `bun:"created_at,notnull,default:current_timestamp"`
}

// NewOnCallScheduleOverride builds an override row with a fresh UID.
func NewOnCallScheduleOverride(
	scheduleUID, userUID string, startAt, endAt time.Time,
) *OnCallScheduleOverride {
	return &OnCallScheduleOverride{
		UID:         uuid.New().String(),
		ScheduleUID: scheduleUID,
		UserUID:     userUID,
		StartAt:     startAt,
		EndAt:       endAt,
		CreatedAt:   time.Now(),
	}
}
