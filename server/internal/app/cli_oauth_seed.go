package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/oauth"
)

// SeedCLIOAuthClient idempotently registers the first-party `solidping-cli`
// OAuth client so `sp auth login` can drive the browser authorization-code
// flow without dynamic client registration. It is a public client (no secret,
// PKCE + loopback redirect); the loopback redirect is registered once with no
// port because RedirectURIAllowed matches loopback URIs ignoring the port.
//
// The seed runs on every boot and is a no-op when the client already exists, so
// a fresh install gets the client and an existing install is untouched. A lost
// insert race (two boots at once) is caught by the unique index on client_id
// and treated as success.
func (s *Server) SeedCLIOAuthClient(ctx context.Context) error {
	existing, err := s.dbService.GetOAuthClientByClientID(ctx, oauth.CLIClientID)
	if err == nil && existing != nil {
		slog.DebugContext(ctx, "CLI OAuth client already registered", "clientID", oauth.CLIClientID)

		return nil
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("look up CLI OAuth client: %w", err)
	}

	now := time.Now()
	client := &models.OAuthClient{
		UID:          uuid.New().String(),
		ClientID:     oauth.CLIClientID,
		ClientName:   oauth.CLIClientName,
		RedirectURIs: []string{oauth.CLIRedirectURI},
		GrantTypes:   []string{oauth.GrantAuthorizationCode, oauth.GrantRefreshToken},
		Scopes:       []string{oauth.ScopeMCP},
		IsPublic:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if createErr := s.dbService.CreateOAuthClient(ctx, client); createErr != nil {
		// A concurrent boot may have inserted the row between our lookup and
		// insert; the unique index on client_id rejects the duplicate. Confirm
		// the client now exists rather than failing the boot.
		confirm, lookupErr := s.dbService.GetOAuthClientByClientID(ctx, oauth.CLIClientID)
		if lookupErr == nil && confirm != nil {
			return nil
		}

		return fmt.Errorf("seed CLI OAuth client: %w", createErr)
	}

	slog.InfoContext(ctx, "Seeded CLI OAuth client", "clientID", oauth.CLIClientID)

	return nil
}
