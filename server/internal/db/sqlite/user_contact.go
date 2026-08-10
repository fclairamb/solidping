package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ListUserContactsWithRoutes returns the ordered notification routes for a user
// in an org, with the contact relation eagerly loaded.
func (s *Service) ListUserContactsWithRoutes(
	ctx context.Context, userUID, orgUID string,
) ([]*models.UserNotificationRoute, error) {
	var routes []*models.UserNotificationRoute

	err := s.db.NewSelect().
		Model(&routes).
		Relation("Contact").
		Where("user_notification_route.user_uid = ?", userUID).
		Where("user_notification_route.org_uid = ?", orgUID).
		// Exclude routes whose contact has been soft-deleted.
		Where("contact.deleted_at IS NULL").
		OrderExpr("user_notification_route.position ASC, user_notification_route.created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list user contacts with routes: %w", err)
	}

	return routes, nil
}

// EnsureDefaultEmailRoute idempotently seeds one email contact + route.
func (s *Service) EnsureDefaultEmailRoute(
	ctx context.Context, userUID, orgUID, email string,
) error {
	now := time.Now()

	contact := models.NewUserContact(userUID, orgUID, models.UserContactTypeEmail, email, "")
	contact.VerifiedAt = &now

	// Insert the contact; ignore conflict (same user+org+type+value).
	_, err := s.db.NewInsert().
		Model(contact).
		On("CONFLICT (user_uid, organization_uid, type, value) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("ensure default email contact: %w", err)
	}

	// Fetch the canonical UID (either just-inserted or the pre-existing one).
	var existing models.UserContact

	err = s.db.NewSelect().
		Model(&existing).
		Where("user_uid = ?", userUID).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", models.UserContactTypeEmail).
		Where("value = ?", email).
		Limit(1).
		Scan(ctx)
	if err != nil {
		return fmt.Errorf("load default email contact: %w", err)
	}

	// Insert the route; ignore conflict (unique on contact_uid).
	route := models.NewUserNotificationRoute(userUID, orgUID, existing.UID, 0)
	_, err = s.db.NewInsert().
		Model(route).
		On("CONFLICT (contact_uid) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("ensure default email route: %w", err)
	}

	return nil
}

// UpsertUserContact creates or restores a contact.
func (s *Service) UpsertUserContact(ctx context.Context, c *models.UserContact) error {
	_, err := s.db.NewInsert().
		Model(c).
		On("CONFLICT (user_uid, organization_uid, type, value) DO UPDATE").
		Set("label = EXCLUDED.label").
		Set("deleted_at = NULL").
		Set("updated_at = ?", time.Now()).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("upsert user contact: %w", err)
	}

	return nil
}

// DeleteUserContact soft-deletes a contact by UID.
func (s *Service) DeleteUserContact(ctx context.Context, uid string) error {
	now := time.Now()
	_, err := s.db.NewUpdate().
		Model((*models.UserContact)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete user contact: %w", err)
	}

	return nil
}

// GetUserContact returns a single non-deleted contact by UID.
func (s *Service) GetUserContact(ctx context.Context, uid string) (*models.UserContact, error) {
	var contact models.UserContact

	err := s.db.NewSelect().
		Model(&contact).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get user contact: %w", err)
	}

	return &contact, nil
}

// SetUserContactVerifyState writes the in-flight verification columns
// (code hash, expiry, attempt count) on a contact. Passing nil codeHash /
// expiresAt clears the pending code while preserving the attempt count.
func (s *Service) SetUserContactVerifyState(
	ctx context.Context, uid string, codeHash *string, expiresAt *time.Time, attempts int,
) error {
	_, err := s.db.NewUpdate().
		Model((*models.UserContact)(nil)).
		Where("uid = ?", uid).
		Set("verify_code_hash = ?", codeHash).
		Set("verify_expires_at = ?", expiresAt).
		Set("verify_attempts = ?", attempts).
		Set("updated_at = ?", time.Now()).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("set user contact verify state: %w", err)
	}

	return nil
}

// MarkUserContactVerified stamps verified_at and clears the pending
// verification columns.
func (s *Service) MarkUserContactVerified(ctx context.Context, uid string, verifiedAt time.Time) error {
	_, err := s.db.NewUpdate().
		Model((*models.UserContact)(nil)).
		Where("uid = ?", uid).
		Set("verified_at = ?", verifiedAt).
		Set("verify_code_hash = NULL").
		Set("verify_expires_at = NULL").
		Set("verify_attempts = 0").
		Set("updated_at = ?", verifiedAt).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("mark user contact verified: %w", err)
	}

	return nil
}

// ClearUserContactVerified removes the verified_at stamp from a contact.
func (s *Service) ClearUserContactVerified(ctx context.Context, uid string) error {
	now := time.Now()

	_, err := s.db.NewUpdate().
		Model((*models.UserContact)(nil)).
		Where("uid = ?", uid).
		Set("verified_at = NULL").
		Set("updated_at = ?", now).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("clear user contact verified: %w", err)
	}

	return nil
}

// ListUserContactsByTypeValue returns every live contact with the given type
// and value, across all users and organizations.
func (s *Service) ListUserContactsByTypeValue(
	ctx context.Context, contactType, value string,
) ([]*models.UserContact, error) {
	var contacts []*models.UserContact

	err := s.db.NewSelect().
		Model(&contacts).
		Where("type = ?", contactType).
		Where("value = ?", value).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list user contacts by type/value: %w", err)
	}

	return contacts, nil
}

// EnsureUserNotificationRoute idempotently creates an enabled route for an
// existing contact, appended after the user's current routes.
func (s *Service) EnsureUserNotificationRoute(ctx context.Context, userUID, orgUID, contactUID string) error {
	count, err := s.db.NewSelect().
		Model((*models.UserNotificationRoute)(nil)).
		Where("user_uid = ?", userUID).
		Where("org_uid = ?", orgUID).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("count user notification routes: %w", err)
	}

	route := models.NewUserNotificationRoute(userUID, orgUID, contactUID, count)

	if _, err := s.db.NewInsert().
		Model(route).
		On("CONFLICT (contact_uid) DO NOTHING").
		Exec(ctx); err != nil {
		return fmt.Errorf("ensure user notification route: %w", err)
	}

	return nil
}

// SetRouteEnabled toggles the enabled flag on a route.
func (s *Service) SetRouteEnabled(ctx context.Context, routeUID string, enabled bool) error {
	_, err := s.db.NewUpdate().
		Model((*models.UserNotificationRoute)(nil)).
		Where("uid = ?", routeUID).
		Set("enabled = ?", enabled).
		Set("updated_at = ?", time.Now()).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("set route enabled: %w", err)
	}

	return nil
}

// ReorderRoutes sets the position of each route to its index in routeUIDs.
func (s *Service) ReorderRoutes(ctx context.Context, userUID, orgUID string, routeUIDs []string) error {
	now := time.Now()

	for i, uid := range routeUIDs {
		_, err := s.db.NewUpdate().
			Model((*models.UserNotificationRoute)(nil)).
			Where("uid = ?", uid).
			Where("user_uid = ?", userUID).
			Where("org_uid = ?", orgUID).
			Set("position = ?", i).
			Set("updated_at = ?", now).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("reorder route %s: %w", uid, err)
		}
	}

	return nil
}

// GetSlackChannelForOrg returns the first enabled Slack channel for the org.
func (s *Service) GetSlackChannelForOrg(ctx context.Context, orgUID string) (*models.Integration, error) {
	var channel models.Integration

	err := s.db.NewSelect().
		Model(&channel).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", models.ConnectionTypeSlack).
		Where("enabled = 1").
		Where("deleted_at IS NULL").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get slack channel for org: %w", err)
	}

	return &channel, nil
}
