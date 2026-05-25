package channels_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/channels"
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

	svc := channels.NewService(dbSvc, creds)

	_, err = svc.CreateChannel(ctx, org.Slug, channels.CreateChannelRequest{
		Type: "slack",
		Name: "x",
	})
	r.ErrorIs(err, channels.ErrSlackManualCreate)
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

	svc := channels.NewService(dbSvc, creds)

	resp, err := svc.CreateChannel(ctx, org.Slug, channels.CreateChannelRequest{
		Type: "webhook",
		Name: "hook",
		Settings: map[string]any{
			"webhook_url": "https://example.com/hook",
		},
	})
	r.NoError(err)
	r.Equal("webhook", resp.Type)
}
