package sqlite

import (
	"context"
	"fmt"

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

// OAuth refresh grants are user_tokens rows (type oauth_refresh) managed via
// the generic user-token methods in sqlite.go; authorization codes are
// state_entries records. Only the client registry above lives here.
