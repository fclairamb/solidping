package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateOAuthClient inserts a new registered OAuth client.
func (s *Service) CreateOAuthClient(ctx context.Context, client *models.OAuthClient) error {
	if _, err := s.db.NewInsert().Model(client).Exec(ctx); err != nil {
		return fmt.Errorf("create oauth client: %w", err)
	}

	return nil
}

// GetOAuthClientByClientID looks up a client by its public client_id.
func (s *Service) GetOAuthClientByClientID(ctx context.Context, clientID string) (*models.OAuthClient, error) {
	client := new(models.OAuthClient)

	err := s.db.NewSelect().Model(client).Where("client_id = ?", clientID).Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get oauth client: %w", err)
	}

	return client, nil
}

// CreateOAuthAuthCode inserts a new single-use authorization code.
func (s *Service) CreateOAuthAuthCode(ctx context.Context, code *models.OAuthAuthCode) error {
	if _, err := s.db.NewInsert().Model(code).Exec(ctx); err != nil {
		return fmt.Errorf("create oauth auth code: %w", err)
	}

	return nil
}

// GetOAuthAuthCode fetches an authorization code by its value.
func (s *Service) GetOAuthAuthCode(ctx context.Context, code string) (*models.OAuthAuthCode, error) {
	row := new(models.OAuthAuthCode)

	err := s.db.NewSelect().Model(row).Where("code = ?", code).Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get oauth auth code: %w", err)
	}

	return row, nil
}

// ConsumeOAuthAuthCode atomically marks a code consumed only if it has not been
// consumed yet, returning whether this call won the race. This single-statement
// compare-and-set is the replay guard: a reused code updates zero rows.
func (s *Service) ConsumeOAuthAuthCode(ctx context.Context, code string, now time.Time) (bool, error) {
	res, err := s.db.NewUpdate().
		Model((*models.OAuthAuthCode)(nil)).
		Set("consumed_at = ?", now).
		Where("code = ?", code).
		Where("consumed_at IS NULL").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("consume oauth auth code: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("consume oauth auth code rows: %w", err)
	}

	return affected == 1, nil
}

// CreateOAuthRefreshToken inserts a new refresh grant.
func (s *Service) CreateOAuthRefreshToken(ctx context.Context, token *models.OAuthRefreshToken) error {
	if _, err := s.db.NewInsert().Model(token).Exec(ctx); err != nil {
		return fmt.Errorf("create oauth refresh token: %w", err)
	}

	return nil
}

// GetOAuthRefreshToken fetches a refresh grant by its value.
func (s *Service) GetOAuthRefreshToken(ctx context.Context, token string) (*models.OAuthRefreshToken, error) {
	row := new(models.OAuthRefreshToken)

	err := s.db.NewSelect().Model(row).Where("token = ?", token).Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get oauth refresh token: %w", err)
	}

	return row, nil
}

// RevokeOAuthRefreshToken marks a single refresh grant revoked only if still
// active, returning whether this call performed the revocation. Used for
// rotation: the caller issues a replacement only when it won this race.
func (s *Service) RevokeOAuthRefreshToken(ctx context.Context, token string, now time.Time) (bool, error) {
	res, err := s.db.NewUpdate().
		Model((*models.OAuthRefreshToken)(nil)).
		Set("revoked_at = ?", now).
		Where("token = ?", token).
		Where("revoked_at IS NULL").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("revoke oauth refresh token: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("revoke oauth refresh token rows: %w", err)
	}

	return affected == 1, nil
}

// RevokeOAuthRefreshTokensForUser revokes all active refresh grants for a user.
// Called on logout / PAT revoke so OAuth-issued sessions are torn down too.
func (s *Service) RevokeOAuthRefreshTokensForUser(ctx context.Context, userUID string, now time.Time) error {
	_, err := s.db.NewUpdate().
		Model((*models.OAuthRefreshToken)(nil)).
		Set("revoked_at = ?", now).
		Where("user_uid = ?", userUID).
		Where("revoked_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("revoke oauth refresh tokens for user: %w", err)
	}

	return nil
}
