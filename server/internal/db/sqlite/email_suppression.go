package sqlite

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateEmailSuppression inserts a new suppression row.
func (s *Service) CreateEmailSuppression(ctx context.Context, sup *models.EmailSuppression) error {
	_, err := s.db.NewInsert().Model(sup).Exec(ctx)
	if err != nil {
		return fmt.Errorf("create email suppression: %w", err)
	}

	return nil
}

// ListEmailSuppressions returns every suppression row for an org, newest
// first — backs the dashboard suppression list (D4).
func (s *Service) ListEmailSuppressions(ctx context.Context, orgUID string) ([]*models.EmailSuppression, error) {
	var rows []*models.EmailSuppression

	err := s.db.NewSelect().
		Model(&rows).
		Where("organization_uid = ?", orgUID).
		OrderExpr("created_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list email suppressions: %w", err)
	}

	return rows, nil
}

// GetEmailSuppression returns a single suppression row scoped to an org.
// Returns sql.ErrNoRows when it does not exist within the given org — the
// handler uses this to 404 rather than deleting a row it hasn't confirmed
// belongs to the caller's org.
func (s *Service) GetEmailSuppression(ctx context.Context, orgUID, uid string) (*models.EmailSuppression, error) {
	sup := new(models.EmailSuppression)

	err := s.db.NewSelect().
		Model(sup).
		Where("organization_uid = ?", orgUID).
		Where("uid = ?", uid).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return sup, nil
}

// DeleteEmailSuppression hard-deletes a suppression row by UID — this is the
// "re-subscribe" action (GET confirmation-page undo, or the dashboard
// delete). Hard delete rather than soft: there is no audit value in keeping
// a tombstone, and re-subscribing should be indistinguishable from never
// having unsubscribed. Callers scope by org first via GetEmailSuppression.
func (s *Service) DeleteEmailSuppression(ctx context.Context, uid string) error {
	_, err := s.db.NewDelete().
		Model((*models.EmailSuppression)(nil)).
		Where("uid = ?", uid).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete email suppression: %w", err)
	}

	return nil
}

// IsEmailSuppressed reports whether (org, email) is currently suppressed for
// checkUID — either by a check-specific row (check_uid = checkUID) or an
// org-wide row (check_uid IS NULL). checkUID may be empty for a
// suppression-scope check that only cares about the org-wide row (e.g. a
// non-incident context); in that case only the org-wide row is consulted.
func (s *Service) IsEmailSuppressed(ctx context.Context, orgUID, email, checkUID string) (bool, error) {
	query := s.db.NewSelect().
		Model((*models.EmailSuppression)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("email = ?", email)

	if checkUID != "" {
		query = query.Where("(check_uid = ? OR check_uid IS NULL)", checkUID)
	} else {
		query = query.Where("check_uid IS NULL")
	}

	exists, err := query.Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("check email suppression: %w", err)
	}

	return exists, nil
}
