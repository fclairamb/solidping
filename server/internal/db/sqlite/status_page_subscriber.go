package sqlite

import (
	"context"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateSubscriber inserts a new status page subscriber row.
func (s *Service) CreateSubscriber(ctx context.Context, sub *models.StatusPageSubscriber) error {
	_, err := s.db.NewInsert().Model(sub).Exec(ctx)

	return err
}

// GetSubscriber retrieves a non-deleted subscriber by UID, scoped to the page.
func (s *Service) GetSubscriber(
	ctx context.Context, statusPageUID, uid string,
) (*models.StatusPageSubscriber, error) {
	sub := new(models.StatusPageSubscriber)

	err := s.db.NewSelect().
		Model(sub).
		Where("uid = ?", uid).
		Where("status_page_uid = ?", statusPageUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// GetSubscriberByConfirmToken retrieves a non-deleted subscriber by confirm token.
func (s *Service) GetSubscriberByConfirmToken(
	ctx context.Context, token string,
) (*models.StatusPageSubscriber, error) {
	sub := new(models.StatusPageSubscriber)

	err := s.db.NewSelect().
		Model(sub).
		Where("confirm_token = ?", token).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// GetSubscriberByUnsubToken retrieves a non-deleted subscriber by unsubscribe token.
func (s *Service) GetSubscriberByUnsubToken(
	ctx context.Context, token string,
) (*models.StatusPageSubscriber, error) {
	sub := new(models.StatusPageSubscriber)

	err := s.db.NewSelect().
		Model(sub).
		Where("unsubscribe_token = ?", token).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// FindLiveSubscriber returns the live (non-deleted) subscriber matching the
// unique tuple, or nil when none exists. incidentUID may be nil for page scope.
func (s *Service) FindLiveSubscriber(
	ctx context.Context, statusPageUID, email string, scope models.SubscriberScope, incidentUID *string,
) (*models.StatusPageSubscriber, error) {
	sub := new(models.StatusPageSubscriber)

	query := s.db.NewSelect().
		Model(sub).
		Where("status_page_uid = ?", statusPageUID).
		Where("email = ?", email).
		Where("scope = ?", string(scope)).
		Where("deleted_at IS NULL")

	if incidentUID != nil {
		query = query.Where("incident_uid = ?", *incidentUID)
	} else {
		query = query.Where("incident_uid IS NULL")
	}

	err := query.Scan(ctx)
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// FindAnySubscriber returns the most recent subscriber matching the unique
// tuple, including soft-deleted rows, or sql.ErrNoRows when none exists. Used
// to soft-undelete on re-subscribe instead of inserting a duplicate.
func (s *Service) FindAnySubscriber(
	ctx context.Context, statusPageUID, email string, scope models.SubscriberScope, incidentUID *string,
) (*models.StatusPageSubscriber, error) {
	sub := new(models.StatusPageSubscriber)

	query := s.db.NewSelect().
		Model(sub).
		Where("status_page_uid = ?", statusPageUID).
		Where("email = ?", email).
		Where("scope = ?", string(scope)).
		Order("created_at DESC").
		Limit(1)

	if incidentUID != nil {
		query = query.Where("incident_uid = ?", *incidentUID)
	} else {
		query = query.Where("incident_uid IS NULL")
	}

	if err := query.Scan(ctx); err != nil {
		return nil, err
	}

	return sub, nil
}

// ConfirmSubscriber sets confirmed_at and consumes the confirm token by
// replacing it with a non-reusable sentinel (the row UID prefixed with
// "consumed:") so the opaque token can no longer be looked up while preserving
// the NOT NULL + unique-index constraints. Returns sql.ErrNoRows when the row
// is already gone.
func (s *Service) ConfirmSubscriber(ctx context.Context, uid string, confirmedAt time.Time) error {
	_, err := s.db.NewUpdate().
		Model((*models.StatusPageSubscriber)(nil)).
		Set("confirmed_at = ?", confirmedAt).
		Set("confirm_token = ?", "consumed:"+uid).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Exec(ctx)

	return err
}

// ResubscribeSubscriber soft-undeletes an existing row and refreshes its tokens
// and confirmation state so a returning visitor must confirm again.
func (s *Service) ResubscribeSubscriber(
	ctx context.Context, uid, confirmToken, unsubscribeToken string,
) error {
	_, err := s.db.NewUpdate().
		Model((*models.StatusPageSubscriber)(nil)).
		Set("deleted_at = NULL").
		Set("confirmed_at = NULL").
		Set("confirm_token = ?", confirmToken).
		Set("unsubscribe_token = ?", unsubscribeToken).
		Where("uid = ?", uid).
		Exec(ctx)

	return err
}

// UpdateSubscriberDelivery records the outcome of a webhook/Slack delivery:
// the consecutive-failure counter and, when the circuit breaker trips, the
// disable timestamp. disabledAt nil CLEARS the column, so a re-enable goes
// through the same call.
func (s *Service) UpdateSubscriberDelivery(
	ctx context.Context, uid string, failureCount int, disabledAt *time.Time,
) error {
	_, err := s.db.NewUpdate().
		Model((*models.StatusPageSubscriber)(nil)).
		Set("failure_count = ?", failureCount).
		Set("disabled_at = ?", disabledAt).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Exec(ctx)

	return err
}

// SoftDeleteSubscriber sets deleted_at on a subscriber (unsubscribe).
func (s *Service) SoftDeleteSubscriber(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.StatusPageSubscriber)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Exec(ctx)

	return err
}

// ListConfirmedSubscribers returns confirmed, non-deleted subscribers for the
// page: all page-scoped subscribers plus incident-scoped subscribers matching
// incidentUID (when provided).
func (s *Service) ListConfirmedSubscribers(
	ctx context.Context, statusPageUID string, incidentUID *string,
) ([]*models.StatusPageSubscriber, error) {
	var subs []*models.StatusPageSubscriber

	query := s.db.NewSelect().
		Model(&subs).
		Where("status_page_uid = ?", statusPageUID).
		Where("confirmed_at IS NOT NULL").
		Where("deleted_at IS NULL")

	if incidentUID != nil {
		query = query.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				Where("scope = ?", string(models.SubscriberScopePage)).
				WhereOr("scope = ? AND incident_uid = ?", string(models.SubscriberScopeIncident), *incidentUID)
		})
	} else {
		query = query.Where("scope = ?", string(models.SubscriberScopePage))
	}

	if err := query.Scan(ctx); err != nil {
		return nil, err
	}

	return subs, nil
}

// ListSubscribers returns all non-deleted subscribers for a page (admin view).
func (s *Service) ListSubscribers(
	ctx context.Context, statusPageUID string,
) ([]*models.StatusPageSubscriber, error) {
	var subs []*models.StatusPageSubscriber

	err := s.db.NewSelect().
		Model(&subs).
		Where("status_page_uid = ?", statusPageUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return subs, nil
}
