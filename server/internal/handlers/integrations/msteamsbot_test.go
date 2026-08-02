package integrations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

// setupMSTeamsBot builds an integrations service over an in-memory database
// plus a linked msteams-bot connection that has captured one conversation.
func setupMSTeamsBot(
	t *testing.T, slug string,
) (context.Context, *integrations.Service, db.Service, *models.Organization, *models.Integration) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization(slug, "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	settings := &models.MSTeamsBotSettings{
		TenantID:   "tenant-owned-by-this-org",
		ServiceURL: "https://smba.trafficmanager.test/emea",
		BotID:      "28:app-id",
		Destinations: []models.MSTeamsDestination{
			{ID: "19:captured-channel", Name: "alerts", Type: "channel"},
		},
	}

	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)

	conn := models.NewIntegration(org.UID, models.ConnectionTypeMSTeamsBot, "Microsoft Teams")
	conn.Settings = settingsMap
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	return ctx, integrations.NewService(dbSvc, creds, nil, nil), dbSvc, org, conn
}

// TestCreateIntegrationRejectsMSTeamsBotType pins the creation guard.
//
// A msteams-bot connection carries a tenant id that decides whose Teams
// conversations it receives, so it must be established by the link-code flow
// (which proves the org and the tenant are the same actor), never by a caller
// asserting one through the generic create API.
func TestCreateIntegrationRejectsMSTeamsBotType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, org, _ := setupMSTeamsBot(t, "msteams-create")

	_, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "msteams-bot",
		Name:     "Sneaky Teams",
		Settings: models.JSONMap{"tenant_id": "somebody-elses-tenant"},
	})
	r.ErrorIs(err, integrations.ErrMSTeamsBotManualCreate)
}

// TestUpdateIntegrationRestoresMSTeamsBotServerFields is the regression test
// for the PATCH half of the tenant-hijack fix: the creation guard would be
// pointless if the same values could be written onto an existing row.
func TestUpdateIntegrationRestoresMSTeamsBotServerFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, dbSvc, org, conn := setupMSTeamsBot(t, "msteams-patch")

	name := "Renamed"
	_, err := svc.UpdateIntegration(ctx, org.Slug, conn.UID, integrations.UpdateIntegrationRequest{
		Name: &name,
		Settings: models.JSONMap{
			"tenant_id":   "attacker-supplied-tenant",
			"service_url": "https://attacker.example.com",
			"bot_id":      "28:attacker",
			"destinations": []any{
				map[string]any{"id": "19:attacker-channel", "name": "pwned"},
			},
			"uninstalled_at": "",
		},
	})
	r.NoError(err)

	stored, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(stored.Settings)
	r.NoError(err)

	// Every server-owned field kept its stored value.
	r.Equal("tenant-owned-by-this-org", settings.TenantID)
	r.Equal("https://smba.trafficmanager.test/emea", settings.ServiceURL)
	r.Equal("28:app-id", settings.BotID)
	r.Len(settings.Destinations, 1)
	r.Equal("19:captured-channel", settings.Destinations[0].ID)

	// The client-owned field (the display name) still updated, so the restore
	// is not just refusing the whole PATCH.
	r.Equal("Renamed", stored.Name)
}

// TestUpdateIntegrationAcceptsCapturedDestination is the positive control for
// the destination picker: selecting a conversation the bot really is in works.
func TestUpdateIntegrationAcceptsCapturedDestination(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, dbSvc, org, conn := setupMSTeamsBot(t, "msteams-dest-ok")

	_, err := svc.UpdateIntegration(ctx, org.Slug, conn.UID, integrations.UpdateIntegrationRequest{
		Settings: models.JSONMap{
			"channel_id":   "19:captured-channel",
			"channel_name": "alerts",
		},
	})
	r.NoError(err)

	stored, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(stored.Settings)
	r.NoError(err)
	r.Equal("19:captured-channel", settings.ChannelID)
}

// TestUpdateIntegrationRejectsUncapturedDestination is the security control.
//
// A Teams conversation id is discoverable — it appears in "Get link to
// channel" URLs — so accepting one on the caller's word would let an org point
// its incident cards at a channel in a different tenant, and the shared SaaS
// bot credential would deliver them. Selection must name a conversation the
// bot was actually added to.
func TestUpdateIntegrationRejectsUncapturedDestination(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, dbSvc, org, conn := setupMSTeamsBot(t, "msteams-dest-bad")

	_, err := svc.UpdateIntegration(ctx, org.Slug, conn.UID, integrations.UpdateIntegrationRequest{
		Settings: models.JSONMap{
			"channel_id":   "19:another-tenants-channel",
			"channel_name": "victim-alerts",
		},
	})
	r.ErrorIs(err, integrations.ErrMSTeamsBotUnknownDestination)

	// And nothing was persisted.
	stored, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(stored.Settings)
	r.NoError(err)
	r.Empty(settings.ChannelID)
}

// TestUpdateIntegrationCannotForgeDestinationThenSelectIt closes the obvious
// bypass: injecting the target conversation into `destinations` in the same
// PATCH that selects it. The restore runs first, so the forged entry is gone
// before validation looks at it.
func TestUpdateIntegrationCannotForgeDestinationThenSelectIt(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _, org, conn := setupMSTeamsBot(t, "msteams-dest-forge")

	_, err := svc.UpdateIntegration(ctx, org.Slug, conn.UID, integrations.UpdateIntegrationRequest{
		Settings: models.JSONMap{
			"destinations": []any{
				map[string]any{"id": "19:another-tenants-channel", "name": "victim-alerts"},
			},
			"channel_id": "19:another-tenants-channel",
		},
	})
	r.ErrorIs(err, integrations.ErrMSTeamsBotUnknownDestination)
}
