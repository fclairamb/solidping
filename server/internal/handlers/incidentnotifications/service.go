// Package incidentnotifications provides the read API for the
// incident_notifications audit table written by the notification fan-out paths.
package incidentnotifications

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Sentinel errors.
var (
	ErrOrgNotFound      = errors.New("organization not found")
	ErrIncidentNotFound = errors.New("incident not found")
	ErrForbidden        = errors.New("forbidden")
)

// Service implements the incident-notifications business logic.
type Service struct {
	db db.Service
}

// NewService builds a service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// ResolveOrgUID maps an org slug to its UID. Returns ErrOrgNotFound on miss.
func (s *Service) ResolveOrgUID(ctx context.Context, orgSlug string) (string, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return "", ErrOrgNotFound
	}

	return org.UID, nil
}

// NotificationRow is the handler-layer DTO for a single notification entry,
// flattening user and connection info so callers need no extra round-trips.
type NotificationRow struct {
	UID         string     `json:"uid"`
	IncidentUID string     `json:"incidentUid"`
	EventType   string     `json:"eventType"`
	Source      string     `json:"source"`
	StepUID     *string    `json:"stepUid,omitempty"`
	RepeatIndex *int       `json:"repeatIndex,omitempty"`
	ChannelType string     `json:"channelType"`
	Status      string     `json:"status"`
	SkipReason  *string    `json:"skipReason,omitempty"`
	Error       *string    `json:"error,omitempty"`
	MessageID   *string    `json:"messageId,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	SentAt      *time.Time `json:"sentAt,omitempty"`

	// Nullable sub-objects (nil → null in JSON output).
	User       *userSubObject       `json:"user"`
	Connection *connectionSubObject `json:"connection"`

	// Incident context — populated only on user-scoped queries.
	Incident *incidentSubObject `json:"incident,omitempty"`
}

type userSubObject struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
}

type connectionSubObject struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type incidentSubObject struct {
	UID       string    `json:"uid"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"startedAt"`
}

// ListFilter is passed by the handler to scope the DB query.
type ListFilter struct {
	IncidentUID   string
	UserUID       string
	ConnectionUID string
	Status        string
	Limit         int
	Before        time.Time
}

// ListForIncident returns notifications for a specific incident.
func (s *Service) ListForIncident(
	ctx context.Context, orgUID, incidentUID string, f ListFilter,
) ([]*NotificationRow, error) {
	_, err := s.db.GetIncident(ctx, orgUID, incidentUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrIncidentNotFound
	}

	if err != nil {
		return nil, err
	}

	f.IncidentUID = incidentUID

	return s.list(ctx, orgUID, f)
}

// ListForUser returns notifications across all incidents for a specific user.
func (s *Service) ListForUser(
	ctx context.Context, orgUID, userUID string, f ListFilter,
) ([]*NotificationRow, error) {
	f.UserUID = userUID

	return s.list(ctx, orgUID, f)
}

func (s *Service) list(
	ctx context.Context, orgUID string, f ListFilter,
) ([]*NotificationRow, error) {
	dbFilter := db.ListIncidentNotificationsFilter{
		IncidentUID:   f.IncidentUID,
		UserUID:       f.UserUID,
		ConnectionUID: f.ConnectionUID,
		Status:        f.Status,
		Limit:         f.Limit,
		Before:        f.Before,
	}

	rows, err := s.db.ListIncidentNotifications(ctx, orgUID, dbFilter)
	if err != nil {
		return nil, err
	}

	out := make([]*NotificationRow, 0, len(rows))

	for _, r := range rows {
		out = append(out, toNotificationRow(r))
	}

	return out, nil
}

func toNotificationRow(r *models.IncidentNotificationRow) *NotificationRow {
	row := &NotificationRow{
		UID:         r.UID,
		IncidentUID: r.IncidentUID,
		EventType:   r.EventType,
		Source:      r.Source,
		StepUID:     r.StepUID,
		RepeatIndex: r.RepeatIndex,
		ChannelType: r.ChannelType,
		Status:      r.Status,
		SkipReason:  r.SkipReason,
		Error:       r.Error,
		MessageID:   r.MessageID,
		CreatedAt:   r.CreatedAt,
		SentAt:      r.SentAt,
	}

	if r.UserUID != nil && r.UserName != nil {
		row.User = &userSubObject{
			UID:  *r.UserUID,
			Name: *r.UserName,
		}
	}

	if r.ConnectionUID != nil && r.ConnectionName != nil && r.ConnectionType != nil {
		row.Connection = &connectionSubObject{
			UID:  *r.ConnectionUID,
			Name: *r.ConnectionName,
			Type: *r.ConnectionType,
		}
	}

	if r.IncidentTitle != nil && r.IncidentState != nil && r.IncidentStartedAt != nil {
		title := ""
		if r.IncidentTitle != nil {
			title = *r.IncidentTitle
		}

		state := "active"
		if r.IncidentState != nil && *r.IncidentState == int(models.IncidentStateResolved) {
			state = "resolved"
		}

		row.Incident = &incidentSubObject{
			UID:       r.IncidentUID,
			Title:     title,
			State:     state,
			StartedAt: *r.IncidentStartedAt,
		}
	}

	return row
}
