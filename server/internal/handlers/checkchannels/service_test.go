package checkchannels_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checkchannels"
)

// testSetup spins up an in-memory SQLite DB with one org, one check, and the
// supplied integration connections, returning the service and the org/check.
func testSetup(t *testing.T, conns ...*models.Integration) (
	context.Context, *checkchannels.Service, *models.Organization, *models.Check,
) {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("cc-test", "Check-Connection Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "my-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	for _, conn := range conns {
		conn.OrganizationUID = org.UID
		r.NoError(dbSvc.CreateChannel(ctx, conn))
	}

	return ctx, checkchannels.NewService(dbSvc), org, check
}

// TestSetConnectionsRejectsNonNotifyIntegration verifies binding a Freebox
// (CanNotify == false) integration as a notify target is rejected. This is the
// silent-no-op bug fix.
func TestSetConnectionsRejectsNonNotifyIntegration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	freebox := models.NewIntegration("", models.ConnectionTypeFreebox, "Home Freebox")
	ctx, svc, org, check := testSetup(t, freebox)

	err := svc.SetChannels(ctx, org.Slug, check.UID, checkchannels.SetConnectionsRequest{
		ConnectionUIDs: []string{freebox.UID},
	})
	r.ErrorIs(err, checkchannels.ErrNotNotifyCapable)
}

// TestAddConnectionRejectsNonNotifyIntegration is the single-add variant of
// the rejection.
func TestAddConnectionRejectsNonNotifyIntegration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	freebox := models.NewIntegration("", models.ConnectionTypeFreebox, "Home Freebox")
	ctx, svc, org, check := testSetup(t, freebox)

	err := svc.AddChannel(ctx, org.Slug, check.UID, freebox.UID)
	r.ErrorIs(err, checkchannels.ErrNotNotifyCapable)
}

// TestSetConnectionsAllowsNotifyIntegration is the positive guard: a
// notify-capable integration (webhook) binds without error.
func TestSetConnectionsAllowsNotifyIntegration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	webhook := models.NewIntegration("", models.ConnectionTypeWebhook, "Ops webhook")
	ctx, svc, org, check := testSetup(t, webhook)

	err := svc.SetChannels(ctx, org.Slug, check.UID, checkchannels.SetConnectionsRequest{
		ConnectionUIDs: []string{webhook.UID},
	})
	r.NoError(err)

	resp, err := svc.ListChannels(ctx, org.Slug, check.UID)
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Equal(webhook.UID, resp.Data[0].UID)
}

// TestAddConnectionAllowsNotifyIntegration is the positive single-add guard.
func TestAddConnectionAllowsNotifyIntegration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	webhook := models.NewIntegration("", models.ConnectionTypeWebhook, "Ops webhook")
	ctx, svc, org, check := testSetup(t, webhook)

	r.NoError(svc.AddChannel(ctx, org.Slug, check.UID, webhook.UID))
}

// TestSetConnectionsRejectsMixedWhenOneIsNonNotify verifies that a batch with
// even one non-notify integration is rejected wholesale.
func TestSetConnectionsRejectsMixedWhenOneIsNonNotify(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	webhook := models.NewIntegration("", models.ConnectionTypeWebhook, "Ops webhook")
	freebox := models.NewIntegration("", models.ConnectionTypeFreebox, "Home Freebox")
	ctx, svc, org, check := testSetup(t, webhook, freebox)

	err := svc.SetChannels(ctx, org.Slug, check.UID, checkchannels.SetConnectionsRequest{
		ConnectionUIDs: []string{webhook.UID, freebox.UID},
	})
	r.ErrorIs(err, checkchannels.ErrNotNotifyCapable)
}

// msTeamsBotConn builds a linked msteams-bot connection that has captured one
// conversation reference.
func msTeamsBotConn(t *testing.T) *models.Integration {
	t.Helper()

	settings := &models.MSTeamsBotSettings{
		TenantID: "tenant-1",
		Destinations: []models.MSTeamsDestination{
			{ID: "19:captured-channel", Name: "alerts", Type: "channel"},
		},
	}

	settingsMap, err := settings.ToJSONMap()
	require.NoError(t, err)

	conn := models.NewIntegration("", models.ConnectionTypeMSTeamsBot, "Microsoft Teams")
	conn.Settings = settingsMap

	return conn
}

// TestUpdateConnectionSettingsRejectsUncapturedMSTeamsOverride is the security
// control for the per-check Teams destination override.
//
// A Teams conversation id is discoverable ("Get link to channel" URLs contain
// it), so an override naming a conversation the bot was never added to would
// otherwise let this org redirect its incident cards into a different tenant's
// channel via the shared bot credential.
func TestUpdateConnectionSettingsRejectsUncapturedMSTeamsOverride(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	conn := msTeamsBotConn(t)
	ctx, svc, org, check := testSetup(t, conn)

	r.NoError(svc.AddChannel(ctx, org.Slug, *check.Slug, conn.UID))

	err := svc.UpdateConnectionSettings(ctx, org.Slug, *check.Slug, conn.UID,
		checkchannels.UpdateConnectionSettingsRequest{
			Settings: models.JSONMap{"conversation_id": "19:another-tenants-channel"},
		})
	r.ErrorIs(err, checkchannels.ErrMSTeamsBotUnknownDestination)
}

// TestUpdateConnectionSettingsAcceptsCapturedMSTeamsOverride is the positive
// control — a legitimate per-check redirect must still work.
func TestUpdateConnectionSettingsAcceptsCapturedMSTeamsOverride(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	conn := msTeamsBotConn(t)
	ctx, svc, org, check := testSetup(t, conn)

	r.NoError(svc.AddChannel(ctx, org.Slug, *check.Slug, conn.UID))

	r.NoError(svc.UpdateConnectionSettings(ctx, org.Slug, *check.Slug, conn.UID,
		checkchannels.UpdateConnectionSettingsRequest{
			Settings: models.JSONMap{"conversation_id": "19:captured-channel"},
		}))
}
