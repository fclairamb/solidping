package sqlite

import (
	"context"
	"fmt"
	"time"

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
// status=sent. Used by NotificationJobRun.Run.
func (s *Service) MarkIncidentNotificationSentByJob(
	ctx context.Context, jobUID string, sentAt time.Time, messageID string,
) error {
	_, err := s.db.NewUpdate().
		TableExpr("incident_notifications").
		Set("status = ?", models.IncidentNotificationStatusSent).
		Set("sent_at = ?", sentAt).
		Set("message_id = ?", messageID).
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
) error {
	if retryable {
		// Leave the row at pending so a retry can update it.
		return nil
	}

	_, err := s.db.NewUpdate().
		TableExpr("incident_notifications").
		Set("status = ?", models.IncidentNotificationStatusFailed).
		Set("failed_at = ?", failedAt).
		Set("error = ?", errMsg).
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
