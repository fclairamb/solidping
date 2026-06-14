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

// NotificationDetail is the single-notification DTO. It is NotificationRow plus
// the lifecycle/identifier fields that are stored but not surfaced in the list:
// failedAt, cancelledAt and jobUid.
type NotificationDetail struct {
	NotificationRow

	FailedAt    *time.Time `json:"failedAt,omitempty"`
	CancelledAt *time.Time `json:"cancelledAt,omitempty"`
	JobUID      *string    `json:"jobUid,omitempty"`

	// DeliveryDetails carries the structured per-attempt artifacts (HTTP status,
	// stripped URL, capped request/response bodies, duration). Omitted for rows
	// captured before the feature and for channels that produce no artifacts.
	DeliveryDetails *models.DeliveryDetails `json:"deliveryDetails,omitempty"`
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
//
//nolint:gocritic // filter is passed by value intentionally; it is modified (IncidentUID set) inside this call
func (s *Service) ListForIncident(
	ctx context.Context, orgUID, incidentUID string, filter ListFilter,
) ([]*NotificationRow, error) {
	_, err := s.db.GetIncident(ctx, orgUID, incidentUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrIncidentNotFound
	}

	if err != nil {
		return nil, err
	}

	filter.IncidentUID = incidentUID

	return s.list(ctx, orgUID, filter)
}

// ErrNotificationNotFound is returned when the notification does not exist
// within the given org (and, for the incident-scoped variant, the incident).
var ErrNotificationNotFound = errors.New("notification not found")

// GetForIncident returns a single notification by UID, scoped to the org and
// incident. The org/incident pair is validated first (an unknown incident
// yields ErrIncidentNotFound), then the single row is fetched (an unknown
// notification yields ErrNotificationNotFound). A notification belonging to a
// different org or incident is treated as not found — existence is never leaked
// across tenants.
func (s *Service) GetForIncident(
	ctx context.Context, orgUID, incidentUID, notifUID string,
) (*NotificationDetail, error) {
	_, err := s.db.GetIncident(ctx, orgUID, incidentUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrIncidentNotFound
	}

	if err != nil {
		return nil, err
	}

	row, err := s.db.GetIncidentNotification(ctx, orgUID, incidentUID, notifUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotificationNotFound
	}

	if err != nil {
		return nil, err
	}

	return toNotificationDetail(row), nil
}

// GetByOrg returns a single notification by UID, scoped only to the org (no
// incident required). A notification belonging to a different org is treated
// as not found — existence is never leaked across tenants.
func (s *Service) GetByOrg(
	ctx context.Context, orgUID, notifUID string,
) (*NotificationDetail, error) {
	row, err := s.db.GetOrgNotification(ctx, orgUID, notifUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotificationNotFound
	}

	if err != nil {
		return nil, err
	}

	return toNotificationDetail(row), nil
}

// ListByConnection returns notifications filtered by connection_uid. Requires
// a non-empty connectionUID (caller must validate).
func (s *Service) ListByConnection(
	ctx context.Context, orgUID, connectionUID string, limit int,
) ([]*NotificationRow, error) {
	filter := ListFilter{
		ConnectionUID: connectionUID,
		Limit:         limit,
	}

	return s.list(ctx, orgUID, filter)
}

// ListForUser returns notifications across all incidents for a specific user.
//
//nolint:gocritic // filter is passed by value intentionally; it is modified (UserUID set) inside this call
func (s *Service) ListForUser(
	ctx context.Context, orgUID, userUID string, filter ListFilter,
) ([]*NotificationRow, error) {
	filter.UserUID = userUID

	return s.list(ctx, orgUID, filter)
}

func (s *Service) list(
	ctx context.Context, orgUID string, filter ListFilter, //nolint:gocritic // passed by value intentionally
) ([]*NotificationRow, error) {
	dbFilter := db.ListIncidentNotificationsFilter{
		IncidentUID:   filter.IncidentUID,
		UserUID:       filter.UserUID,
		ConnectionUID: filter.ConnectionUID,
		Status:        filter.Status,
		Limit:         filter.Limit,
		Before:        filter.Before,
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

func toNotificationRow(src *models.IncidentNotificationRow) *NotificationRow {
	row := &NotificationRow{
		UID:         src.UID,
		IncidentUID: src.IncidentUID,
		EventType:   src.EventType,
		Source:      src.Source,
		StepUID:     src.StepUID,
		RepeatIndex: src.RepeatIndex,
		ChannelType: src.ChannelType,
		Status:      src.Status,
		SkipReason:  src.SkipReason,
		Error:       src.Error,
		MessageID:   src.MessageID,
		CreatedAt:   src.CreatedAt,
		SentAt:      src.SentAt,
	}

	if src.UserUID != nil && src.UserName != nil {
		row.User = &userSubObject{
			UID:  *src.UserUID,
			Name: *src.UserName,
		}
	}

	if src.ConnectionUID != nil && src.ConnectionName != nil && src.ConnectionType != nil {
		row.Connection = &connectionSubObject{
			UID:  *src.ConnectionUID,
			Name: *src.ConnectionName,
			Type: *src.ConnectionType,
		}
	}

	if src.IncidentTitle != nil && src.IncidentState != nil && src.IncidentStartedAt != nil {
		title := ""
		if src.IncidentTitle != nil {
			title = *src.IncidentTitle
		}

		state := "active"
		if src.IncidentState != nil && *src.IncidentState == int(models.IncidentStateResolved) {
			state = "resolved"
		}

		row.Incident = &incidentSubObject{
			UID:       src.IncidentUID,
			Title:     title,
			State:     state,
			StartedAt: *src.IncidentStartedAt,
		}
	}

	return row
}

func toNotificationDetail(src *models.IncidentNotificationRow) *NotificationDetail {
	return &NotificationDetail{
		NotificationRow: *toNotificationRow(src),
		FailedAt:        src.FailedAt,
		CancelledAt:     src.CanceledAt,
		JobUID:          src.JobUID,
		DeliveryDetails: src.DeliveryDetails,
	}
}
