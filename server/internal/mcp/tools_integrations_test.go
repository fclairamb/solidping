package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

// TestNewHandler_ThreadsConfigForTwilioTestModeBypass proves NewHandler wires
// the app config into integrationsSvc, the same way server.go's HTTP
// integrations handler does. Without that wiring, a Twilio connection
// created through the MCP tool surface would always attempt a live Twilio
// credential check, even under SP_RUNMODE=test, unlike the HTTP API path.
//
// This deliberately drives the real NewHandler constructor (not a stubbed
// Service) and installs no network stub for the Twilio credential-check
// seam: if RunMode=="test" isn't reaching integrationsSvc, this call would
// try to reach the real Twilio API and fail (or hang) instead of succeeding.
func TestNewHandler_ThreadsConfigForTwilioTestModeBypass(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	org := models.NewOrganization("mcp-twilio-test", "MCP Twilio Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	cfg := &config.Config{RunMode: "test"}

	handler := NewHandler(dbSvc, nil, nil, nil, creds, nil, nil, cfg)

	resp, err := handler.integrationsSvc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "twilio",
		Name: "mcp-twilio",
		Settings: map[string]any{
			"account_sid": "AC00000000000000000000000000000001",
			"auth_token":  "placeholder-token",
			"from_number": "+15551234567",
		},
	})
	r.NoError(err, "RunMode==test must skip the live Twilio credential check on the MCP surface too")
	r.NotNil(resp)
	r.Equal("twilio", resp.Type)
}
