package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// IncidentNotificationChannelTypeNone is used for skipped rows where no channel is involved.
const IncidentNotificationChannelTypeNone = "none"

// Status constants for IncidentNotification.
const (
	IncidentNotificationStatusPending  = "pending"
	IncidentNotificationStatusSent     = "sent"
	IncidentNotificationStatusFailed   = "failed"
	IncidentNotificationStatusCanceled = "cancelled" //nolint:misspell // DB column uses British English
	IncidentNotificationStatusSkipped  = "skipped"
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
// lifecycle tracking (pending → sent | failed | canceled | skipped).
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
	CanceledAt      *time.Time `bun:"cancelled_at"` //nolint:misspell // DB column uses British English
	FailedAt        *time.Time `bun:"failed_at"`
}

// IncidentNotificationRow is the read-side DTO returned by ListIncidentNotifications.
// It embeds the base notification row plus joined fields from users and
// integration_connections that are populated when the respective FK is non-NULL.
type IncidentNotificationRow struct {
	IncidentNotification

	// Joined from users (non-nil when user_uid IS NOT NULL)
	UserName *string

	// Joined from integration_connections (non-nil when connection_uid IS NOT NULL)
	ConnectionName *string
	ConnectionType *string

	// Joined from incidents for user-scoped queries
	IncidentTitle     *string
	IncidentState     *int
	IncidentStartedAt *time.Time

	// Joined from checks for user-scoped queries
	CheckName *string
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
		ChannelType:     IncidentNotificationChannelTypeNone,
		Status:          IncidentNotificationStatusSkipped,
		SkipReason:      &skipReason,
		CreatedAt:       time.Now(),
	}
}
