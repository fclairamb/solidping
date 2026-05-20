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
func (s *Service) GetSlackChannelForOrg(ctx context.Context, orgUID string) (*models.Channel, error) {
	var channel models.Channel

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
