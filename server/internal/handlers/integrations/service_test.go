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

	svc := integrations.NewService(dbSvc, creds, nil, nil)

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

	svc := integrations.NewService(dbSvc, creds, nil, nil)

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

// TestCreateChannelEmitsEnrichedActivationEvent pins the "Recent activity"
// spec requirement: creating the first channel for an org must store
// channel_uid/channel_name/channel_type on the
// org.activation.first_notification_configured event payload, so the
// dashboard can render "First Notification Configured: <channel name>"
// linking to the integration.
func TestCreateChannelEmitsEnrichedActivationEvent(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("webhook-act-test", "Webhook Activation Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := integrations.NewService(dbSvc, creds, nil, nil)

	resp, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "webhook",
		Name: "Ops Webhook",
		Settings: map[string]any{
			"webhook_url": "https://example.com/hook",
		},
	})
	r.NoError(err)

	events, err := dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: org.UID,
		EventTypes: []models.EventType{
			models.EventTypeOrgActivationFirstNotificationConfigured,
		},
	})
	r.NoError(err)
	r.Len(events, 1, "channel creation must emit exactly one activation event")

	got := events[0]
	r.Equal(resp.UID, got.Payload["channel_uid"])
	r.Equal("Ops Webhook", got.Payload["channel_name"])
	r.Equal("webhook", got.Payload["channel_type"])
}
