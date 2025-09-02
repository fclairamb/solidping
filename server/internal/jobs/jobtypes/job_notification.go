package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

var (
	// ErrConnectionNotFound is returned when the connection is not found.
	ErrConnectionNotFound = errors.New("connection not found")
	// ErrIncidentNotFound is returned when the incident is not found.
	ErrIncidentNotFound = errors.New("incident not found")
	// ErrCheckNotFound is returned when the check is not found.
	ErrCheckNotFound = errors.New("check not found")
	// ErrSenderNotFound is returned when no sender is found for the connection type.
	ErrSenderNotFound = errors.New("sender not found for connection type")
	// ErrMissingConnectionUID is returned when connectionUid is not provided.
	ErrMissingConnectionUID = errors.New("connectionUid is required")
	// ErrMissingIncidentUID is returned when incidentUid is not provided.
	ErrMissingIncidentUID = errors.New("incidentUid is required")
	// ErrMissingEventType is returned when eventType is not provided.
	ErrMissingEventType = errors.New("eventType is required")
)

// NotificationJobConfig configures notification parameters.
type NotificationJobConfig struct {
	ConnectionUID string `json:"connectionUid"`
	IncidentUID   string `json:"incidentUid"`
	EventType     string `json:"eventType"` // "incident.created", "incident.resolved", "incident.escalated"
}

// NotificationJobDefinition is the factory for notification jobs.
type NotificationJobDefinition struct{}

// Type returns the job type identifier.
func (d *NotificationJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeNotification
}

// CreateJobRun creates a new notification job run instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *NotificationJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg NotificationJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("parsing notification config: %w", err)
	}

	if cfg.ConnectionUID == "" {
		return nil, ErrMissingConnectionUID
	}
	if cfg.IncidentUID == "" {
		return nil, ErrMissingIncidentUID
	}
	if cfg.EventType == "" {
		return nil, ErrMissingEventType
	}

	return &NotificationJobRun{config: cfg}, nil
}

// NotificationJobRun is the executable instance of a notification job.
type NotificationJobRun struct {
	config NotificationJobConfig
}

// Run executes the notification job.
func (r *NotificationJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	// 1. Load connection
	connection, err := jctx.DBService.GetIntegrationConnection(ctx, r.config.ConnectionUID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectionNotFound, err)
	}

	// 2. Load incident
	incident, err := jctx.DBService.GetIncident(ctx, connection.OrganizationUID, r.config.IncidentUID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrIncidentNotFound, err)
	}

	// 3. Load check
	check, err := jctx.DBService.GetCheck(ctx, connection.OrganizationUID, incident.CheckUID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCheckNotFound, err)
	}

	// 4. Get sender for connection type
	sender, ok := notifications.GetSender(connection.Type)
	if !ok {
		return fmt.Errorf("%w: %s", ErrSenderNotFound, connection.Type)
	}

	// 5. Build notification payload
	payload := &notifications.Payload{
		EventType:  r.config.EventType,
		Incident:   incident,
		Check:      check,
		Connection: connection,
	}

	// 6. Send notification
	log.InfoContext(ctx, "Sending notification",
		"connectionType", connection.Type,
		"connectionUid", connection.UID,
		"incidentUid", incident.UID,
		"eventType", r.config.EventType,
	)

	if err := sender.Send(ctx, jctx, payload); err != nil {
		// Network errors should be retryable
		if notifications.IsNetworkError(err) {
			return jobdef.NewRetryableError(err)
		}
		return err
	}

	log.InfoContext(ctx, "Notification sent successfully")
	return nil
}
