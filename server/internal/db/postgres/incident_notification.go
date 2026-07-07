package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateIncidentNotification inserts a new audit row.
func (s *Service) CreateIncidentNotification(ctx context.Context, n *models.IncidentNotification) error {
	_, err := s.db.NewInsert().Model(n).Exec(ctx)
	if err != nil {
		return fmt.Errorf("create incident notification: %w", err)
	}

	return nil
}

// MarkIncidentNotificationSentByUID updates the audit row identified by UID to
// status=sent. Used by direct-email paths (no job_uid).
func (s *Service) MarkIncidentNotificationSentByUID(
	ctx context.Context, uid string, sentAt time.Time, messageID string,
) error {
	_, err := s.db.NewUpdate().
		TableExpr("incident_notifications").
		Set("status = ?", models.IncidentNotificationStatusSent).
		Set("sent_at = ?", sentAt).
		Set("message_id = ?", messageID).
		Where("uid = ? AND status = ?", uid, models.IncidentNotificationStatusPending).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("mark incident notification sent by uid: %w", err)
	}

	return nil
}

// MarkIncidentNotificationFailedByUID updates the audit row identified by UID
// to status=failed. Used by direct-email paths (no job_uid).
func (s *Service) MarkIncidentNotificationFailedByUID(
	ctx context.Context, uid string, failedAt time.Time, errMsg string,
) error {
	_, err := s.db.NewUpdate().
		TableExpr("incident_notifications").
		Set("status = ?", models.IncidentNotificationStatusFailed).
		Set("failed_at = ?", failedAt).
		Set("error = ?", errMsg).
		Where("uid = ? AND status = ?", uid, models.IncidentNotificationStatusPending).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("mark incident notification failed by uid: %w", err)
	}

	return nil
}

// MarkIncidentNotificationSentByJob updates the audit row matching job_uid to
// status=sent. Used by NotificationJobRun.Run. When details is non-nil the
// captured delivery artifacts are persisted alongside the status transition.
func (s *Service) MarkIncidentNotificationSentByJob(
	ctx context.Context, jobUID string, sentAt time.Time, messageID string, details *models.DeliveryDetails,
) error {
	query := s.db.NewUpdate().
		TableExpr("incident_notifications").
		Set("status = ?", models.IncidentNotificationStatusSent).
		Set("sent_at = ?", sentAt).
		Set("message_id = ?", messageID)

	if details != nil {
		query = query.Set("delivery_details = ?", details)
	}

	_, err := query.
		Where("job_uid = ? AND status = ?", jobUID, models.IncidentNotificationStatusPending).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("mark incident notification sent by job: %w", err)
	}

	return nil
}

// MarkIncidentNotificationFailedByJob updates the audit row matching job_uid.
// When retryable is true the row stays at pending so a retry can update it;
// when false the row transitions to failed.
func (s *Service) MarkIncidentNotificationFailedByJob(
	ctx context.Context, jobUID string, failedAt time.Time, errMsg string, retryable bool,
	details *models.DeliveryDetails,
) error {
	if retryable {
		// Leave the row at pending so a retry can update it.
		return nil
	}

	query := s.db.NewUpdate().
		TableExpr("incident_notifications").
		Set("status = ?", models.IncidentNotificationStatusFailed).
		Set("failed_at = ?", failedAt).
		Set("error = ?", errMsg)

	if details != nil {
		query = query.Set("delivery_details = ?", details)
	}

	_, err := query.
		Where("job_uid = ? AND status = ?", jobUID, models.IncidentNotificationStatusPending).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("mark incident notification failed by job: %w", err)
	}

	return nil
}

// CancelIncidentNotificationsForIncident bulk-cancels all pending audit rows for
// an incident. Filters on status=pending to avoid clobbering completed rows.
// Returns the number of rows updated.
func (s *Service) CancelIncidentNotificationsForIncident(
	ctx context.Context, incidentUID string, canceledAt time.Time,
) (int64, error) {
	result, err := s.db.NewUpdate().
		TableExpr("incident_notifications").
		Set("status = ?", models.IncidentNotificationStatusCanceled).
		Set("cancelled_at = ?", canceledAt). //nolint:misspell // DB column uses British English
		Where("incident_uid = ? AND status = ?", incidentUID, models.IncidentNotificationStatusPending).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("cancel incident notifications for incident: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("cancel incident notifications rows affected: %w", err)
	}

	return rows, nil
}

// incidentNotificationRawRow is used for scanning the JOIN query result.
type incidentNotificationRawRow struct {
	models.IncidentNotification `bun:",extend"`
	UserName                    *string    `bun:"user_name"`
	ConnectionName              *string    `bun:"connection_name"`
	ConnectionType              *string    `bun:"connection_type"`
	IncidentTitle               *string    `bun:"incident_title"`
	IncidentState               *int       `bun:"incident_state"`
	IncidentStartedAt           *time.Time `bun:"incident_started_at"`
	CheckName                   *string    `bun:"check_name"`
}

// ListIncidentNotifications returns notification rows for an org, optionally
// filtered by incident, user, connection, status, and a before-cursor.
// Results are ordered newest first. User and connection names are joined inline.
//
//nolint:gocritic // filter is a value type passed by value intentionally
func (s *Service) ListIncidentNotifications(
	ctx context.Context, orgUID string, filter db.ListIncidentNotificationsFilter,
) ([]*models.IncidentNotificationRow, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}

	if limit > 500 {
		limit = 500
	}

	query := s.db.NewSelect().
		TableExpr("incident_notifications AS n").
		ColumnExpr("n.*").
		ColumnExpr("u.name AS user_name").
		ColumnExpr("ic.name AS connection_name").
		ColumnExpr("ic.type AS connection_type").
		ColumnExpr("i.title AS incident_title").
		ColumnExpr("i.state AS incident_state").
		ColumnExpr("i.started_at AS incident_started_at").
		ColumnExpr("c.name AS check_name").
		Join("LEFT JOIN users u ON u.uid = n.user_uid").
		Join("LEFT JOIN integrations ic ON ic.uid = n.connection_uid").
		Join("LEFT JOIN incidents i ON i.uid = n.incident_uid").
		Join("LEFT JOIN checks c ON c.uid = i.check_uid").
		Where("n.organization_uid = ?", orgUID)

	if filter.IncidentUID != "" {
		query = query.Where("n.incident_uid = ?", filter.IncidentUID)
	}

	if filter.UserUID != "" {
		query = query.Where("n.user_uid = ?", filter.UserUID)
	}

	if filter.ConnectionUID != "" {
		query = query.Where("n.connection_uid = ?", filter.ConnectionUID)
	}

	if filter.Status != "" {
		query = query.Where("n.status = ?", filter.Status)
	}

	if !filter.Before.IsZero() {
		query = query.Where("n.created_at < ?", filter.Before)
	}

	query = query.OrderExpr("n.created_at DESC").Limit(limit)

	var raw []incidentNotificationRawRow

	if err := query.Scan(ctx, &raw); err != nil {
		return nil, fmt.Errorf("list incident notifications: %w", err)
	}

	out := make([]*models.IncidentNotificationRow, len(raw))
	for i := range raw {
		out[i] = &models.IncidentNotificationRow{
			IncidentNotification: raw[i].IncidentNotification,
			UserName:             raw[i].UserName,
			ConnectionName:       raw[i].ConnectionName,
			ConnectionType:       raw[i].ConnectionType,
			IncidentTitle:        raw[i].IncidentTitle,
			IncidentState:        raw[i].IncidentState,
			IncidentStartedAt:    raw[i].IncidentStartedAt,
			CheckName:            raw[i].CheckName,
		}
	}

	return out, nil
}

// GetIncidentNotification returns a single notification row for an org and
// incident, with user/connection/incident/check joined inline (mirroring the
// join used by ListIncidentNotifications). Returns sql.ErrNoRows when the
// notification does not exist within the given org and incident.
func (s *Service) GetIncidentNotification(
	ctx context.Context, orgUID, incidentUID, notifUID string,
) (*models.IncidentNotificationRow, error) {
	var raw incidentNotificationRawRow

	err := s.db.NewSelect().
		TableExpr("incident_notifications AS n").
		ColumnExpr("n.*").
		ColumnExpr("u.name AS user_name").
		ColumnExpr("ic.name AS connection_name").
		ColumnExpr("ic.type AS connection_type").
		ColumnExpr("i.title AS incident_title").
		ColumnExpr("i.state AS incident_state").
		ColumnExpr("i.started_at AS incident_started_at").
		ColumnExpr("c.name AS check_name").
		Join("LEFT JOIN users u ON u.uid = n.user_uid").
		Join("LEFT JOIN integrations ic ON ic.uid = n.connection_uid").
		Join("LEFT JOIN incidents i ON i.uid = n.incident_uid").
		Join("LEFT JOIN checks c ON c.uid = i.check_uid").
		Where("n.organization_uid = ?", orgUID).
		Where("n.incident_uid = ?", incidentUID).
		Where("n.uid = ?", notifUID).
		Scan(ctx, &raw)
	if err != nil {
		return nil, err
	}

	return &models.IncidentNotificationRow{
		IncidentNotification: raw.IncidentNotification,
		UserName:             raw.UserName,
		ConnectionName:       raw.ConnectionName,
		ConnectionType:       raw.ConnectionType,
		IncidentTitle:        raw.IncidentTitle,
		IncidentState:        raw.IncidentState,
		IncidentStartedAt:    raw.IncidentStartedAt,
		CheckName:            raw.CheckName,
	}, nil
}

// GetOrgNotification returns a single notification row scoped only by org UID
// (no incident required). Returns sql.ErrNoRows when not found.
func (s *Service) GetOrgNotification(
	ctx context.Context, orgUID, notifUID string,
) (*models.IncidentNotificationRow, error) {
	// uid is a uuid-typed column; a non-UUID value errors on the implicit
	// cast instead of matching zero rows, so short-circuit to ErrNoRows.
	if _, err := uuid.Parse(notifUID); err != nil {
		return nil, sql.ErrNoRows
	}

	var raw incidentNotificationRawRow

	err := s.db.NewSelect().
		TableExpr("incident_notifications AS n").
		ColumnExpr("n.*").
		ColumnExpr("u.name AS user_name").
		ColumnExpr("ic.name AS connection_name").
		ColumnExpr("ic.type AS connection_type").
		ColumnExpr("i.title AS incident_title").
		ColumnExpr("i.state AS incident_state").
		ColumnExpr("i.started_at AS incident_started_at").
		ColumnExpr("c.name AS check_name").
		Join("LEFT JOIN users u ON u.uid = n.user_uid").
		Join("LEFT JOIN integrations ic ON ic.uid = n.connection_uid").
		Join("LEFT JOIN incidents i ON i.uid = n.incident_uid").
		Join("LEFT JOIN checks c ON c.uid = i.check_uid").
		Where("n.organization_uid = ?", orgUID).
		Where("n.uid = ?", notifUID).
		Scan(ctx, &raw)
	if err != nil {
		return nil, err
	}

	return &models.IncidentNotificationRow{
		IncidentNotification: raw.IncidentNotification,
		UserName:             raw.UserName,
		ConnectionName:       raw.ConnectionName,
		ConnectionType:       raw.ConnectionType,
		IncidentTitle:        raw.IncidentTitle,
		IncidentState:        raw.IncidentState,
		IncidentStartedAt:    raw.IncidentStartedAt,
		CheckName:            raw.CheckName,
	}, nil
}
