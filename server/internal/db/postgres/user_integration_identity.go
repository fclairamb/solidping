package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ListUserIntegrationIdentities returns every identity row mapped on one
// integration, ordered by display name so the admin UI and the mention
// renderer both see a stable order.
func (s *Service) ListUserIntegrationIdentities(
	ctx context.Context, integrationUID string,
) ([]*models.UserIntegrationIdentity, error) {
	var identities []*models.UserIntegrationIdentity

	err := s.db.NewSelect().
		Model(&identities).
		Where("integration_uid::text = ?", integrationUID).
		OrderExpr("display_name ASC, external_id ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list user integration identities: %w", err)
	}

	return identities, nil
}

// GetUserIntegrationIdentity returns one member's identity on an integration,
// or nil (no error) when the member has none.
func (s *Service) GetUserIntegrationIdentity(
	ctx context.Context, integrationUID, userUID string,
) (*models.UserIntegrationIdentity, error) {
	identity := new(models.UserIntegrationIdentity)

	err := s.db.NewSelect().
		Model(identity).
		Where("integration_uid::text = ?", integrationUID).
		Where("user_uid::text = ?", userUID).
		Limit(1).
		Scan(ctx)

	// "Unmapped" is the normal state for most members, not a failure — the
	// caller degrades to a plain-text name rather than treating it as an error.
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // "no identity" is a normal, non-error outcome
	}

	if err != nil {
		return nil, fmt.Errorf("get user integration identity: %w", err)
	}

	return identity, nil
}

// UpsertUserIntegrationIdentity writes an identity, keyed on
// (integration_uid, user_uid). A member who moves to a different Slack account
// keeps one row rather than accumulating stale ones.
func (s *Service) UpsertUserIntegrationIdentity(
	ctx context.Context, identity *models.UserIntegrationIdentity,
) error {
	identity.UpdatedAt = time.Now()

	_, err := s.db.NewInsert().
		Model(identity).
		On("CONFLICT (integration_uid, user_uid) DO UPDATE").
		Set("external_id = EXCLUDED.external_id").
		Set("display_name = EXCLUDED.display_name").
		Set("source = EXCLUDED.source").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("upsert user integration identity: %w", err)
	}

	return nil
}

// DeleteUserIntegrationIdentity removes a member's identity on an integration.
// Hard delete: the row carries no history worth keeping and the unique index on
// (integration_uid, external_id) must be freed immediately so the external id
// can be reassigned.
func (s *Service) DeleteUserIntegrationIdentity(
	ctx context.Context, integrationUID, userUID string,
) error {
	_, err := s.db.NewDelete().
		Model((*models.UserIntegrationIdentity)(nil)).
		Where("integration_uid::text = ?", integrationUID).
		Where("user_uid::text = ?", userUID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete user integration identity: %w", err)
	}

	return nil
}
