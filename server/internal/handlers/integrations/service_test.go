package integrations_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

// TestCreateChannelRejectsSlackType verifies that the manual create endpoint
// refuses Slack channels: they can only originate from the OAuth install flow.
func TestCreateChannelRejectsSlackType(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("slack-create-test", "Slack Create Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := integrations.NewService(dbSvc, creds)

	_, err = svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "slack",
		Name: "x",
	})
	r.ErrorIs(err, integrations.ErrSlackManualCreate)
}

// TestCreateChannelAllowsNonSlackType is a guard so the Slack rejection does
// not accidentally block other channel types.
func TestCreateChannelAllowsNonSlackType(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("webhook-create-test", "Webhook Create Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := integrations.NewService(dbSvc, creds)

	resp, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "webhook",
		Name: "hook",
		Settings: map[string]any{
			"webhook_url": "https://example.com/hook",
		},
	})
	r.NoError(err)
	r.Equal("webhook", resp.Type)
}
