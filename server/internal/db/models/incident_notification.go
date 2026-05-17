package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Status constants for IncidentNotification.
const (
	IncidentNotificationStatusPending   = "pending"
	IncidentNotificationStatusSent      = "sent"
	IncidentNotificationStatusFailed    = "failed"
	IncidentNotificationStatusCancelled = "cancelled"
	IncidentNotificationStatusSkipped   = "skipped"
)

// Source constants for IncidentNotification.
const (
	IncidentNotificationSourceCheckConnection      = "check_connection"
	IncidentNotificationSourceEscalationUser       = "escalation_user"
	IncidentNotificationSourceEscalationSchedule   = "escalation_schedule"
	IncidentNotificationSourceEscalationAllAdmins  = "escalation_all_admins"
	IncidentNotificationSourceEscalationConnection = "escalation_connection"
)

// IncidentNotification records one dispatch target per event, with full
// lifecycle tracking (pending → sent | failed | cancelled | skipped).
type IncidentNotification struct {
	bun.BaseModel `bun:"table:incident_notifications"`

	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string     `bun:"organization_uid,notnull,type:varchar(36)"`
	IncidentUID     string     `bun:"incident_uid,notnull,type:varchar(36)"`
	EventType       string     `bun:"event_type,notnull"`
	StepUID         *string    `bun:"step_uid,type:varchar(36)"`
	RepeatIndex     *int       `bun:"repeat_index"`
	Source          string     `bun:"source,notnull"`
	UserUID         *string    `bun:"user_uid,type:varchar(36)"`
	ConnectionUID   *string    `bun:"connection_uid,type:varchar(36)"`
	ChannelType     string     `bun:"channel_type,notnull"`
	Status          string     `bun:"status,notnull"`
	SkipReason      *string    `bun:"skip_reason"`
	Error           *string    `bun:"error"`
	JobUID          *string    `bun:"job_uid,type:varchar(36)"`
	MessageID       *string    `bun:"message_id"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	SentAt          *time.Time `bun:"sent_at"`
	CancelledAt     *time.Time `bun:"cancelled_at"`
	FailedAt        *time.Time `bun:"failed_at"`
}

// NewIncidentNotificationForJob builds a pending audit row for a
// channel-based notification (check_connection or escalation_connection).
func NewIncidentNotificationForJob(
	orgUID, incidentUID, eventType, source, connectionUID, jobUID, channelType string,
	stepUID *string, repeatIndex *int,
) *IncidentNotification {
	return &IncidentNotification{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		IncidentUID:     incidentUID,
		EventType:       eventType,
		StepUID:         stepUID,
		RepeatIndex:     repeatIndex,
		Source:          source,
		ConnectionUID:   &connectionUID,
		JobUID:          &jobUID,
		ChannelType:     channelType,
		Status:          IncidentNotificationStatusPending,
		CreatedAt:       time.Now(),
	}
}

// NewIncidentNotificationForUser builds a pending audit row for a
// direct-email escalation target (user, schedule, all_admins paths).
func NewIncidentNotificationForUser(
	orgUID, incidentUID, eventType, source, userUID, channelType string,
	stepUID *string, repeatIndex *int,
) *IncidentNotification {
	return &IncidentNotification{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		IncidentUID:     incidentUID,
		EventType:       eventType,
		StepUID:         stepUID,
		RepeatIndex:     repeatIndex,
		Source:          source,
		UserUID:         &userUID,
		ChannelType:     channelType,
		Status:          IncidentNotificationStatusPending,
		CreatedAt:       time.Now(),
	}
}

// NewSkippedIncidentNotification builds a skipped audit row for paths where
// no notification is sent (empty schedule, no admins, etc.).
func NewSkippedIncidentNotification(
	orgUID, incidentUID, eventType, source, skipReason string,
	stepUID *string, repeatIndex *int,
) *IncidentNotification {
	return &IncidentNotification{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		IncidentUID:     incidentUID,
		EventType:       eventType,
		StepUID:         stepUID,
		RepeatIndex:     repeatIndex,
		Source:          source,
		ChannelType:     "none",
		Status:          IncidentNotificationStatusSkipped,
		SkipReason:      &skipReason,
		CreatedAt:       time.Now(),
	}
}
