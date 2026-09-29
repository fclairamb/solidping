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

// TestIntegrationToolDefinitions locks annotations and outputSchemas on the
// integration tools (Glama usage scoring requires both on every tool).
func TestIntegrationToolDefinitions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		def         ToolDefinition
		title       string
		readOnly    bool
		destructive bool
	}{
		{listIntegrationsDef(), "List integrations", true, false},
		{createIntegrationDef(), "Create integration", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.def.Name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			def := tc.def
			r.NotEmpty(def.Description)

			r.NotNil(def.Annotations, "%s must declare annotations", def.Name)
			r.Equal(tc.title, def.Annotations.Title, "sentence-case title")
			r.Equal(tc.readOnly, def.Annotations.ReadOnlyHint, def.Name)
			r.Equal(tc.destructive, def.Annotations.DestructiveHint, def.Name)
			r.False(def.Annotations.OpenWorldHint, def.Name)

			if tc.readOnly {
				r.Contains(def.Description, "Read-only: works with mcp:read tokens.")
			} else {
				r.Contains(def.Description,
					"Requires the mcp scope (mcp:read tokens are refused) and at least the "+
						"user role in the organization.")
			}

			r.NotNil(def.OutputSchema, "%s must declare an outputSchema", def.Name)
			schema, ok := def.OutputSchema.(map[string]any)
			r.True(ok, "%s outputSchema must be an object schema", def.Name)
			r.Equal(schemaTypeObject, schema[schemaKeyType])
			props, hasProps := schema[schemaKeyProperties].(map[string]any)
			r.True(hasProps, "%s outputSchema must declare properties", def.Name)
			r.NotEmpty(props)
			if def.Name == "list_integrations" {
				r.Contains(props, schemaKeyData, "list_integrations must return {data: [...]}")
			} else {
				r.Contains(props, propUID, "create_integration must return the created integration")
			}
		})
	}
}

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
