package msteams

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestHandleUninstall_ClearsRoutingStateForTheTenant covers the uninstall
// acceptance criterion: removing the app must stop routing for the tenant's
// connection and leave an unrelated tenant untouched.
//
// Unlike Slack the row is kept (the credentials are instance-level, so a
// reinstall restores service) — it is marked uninstalled and disabled.
func TestHandleUninstall_ClearsRoutingStateForTheTenant(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	conn := newConnection(ctx, t, svc, "teams-uninst-a", testTenantID)
	connOther := newConnection(ctx, t, svc, "teams-uninst-oth", "other-tenant")

	_, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)

	r.NoError(svc.HandleUninstall(ctx, testTenantID))

	stored, err := svc.db.GetChannel(ctx, conn.UID)
	r.NoError(err)
	r.False(stored.Enabled, "uninstalled connection must stop routing")

	settings, err := models.MSTeamsBotSettingsFromJSONMap(stored.Settings)
	r.NoError(err)
	r.NotEmpty(settings.UninstalledAt)
	r.Empty(settings.Destinations)
	r.Empty(settings.ChannelID)

	other, err := svc.db.GetChannel(ctx, connOther.UID)
	r.NoError(err)
	r.True(other.Enabled, "an unrelated tenant must be untouched")
}

// TestHandleUninstall_HonorsSingleTenantPin keeps the uninstall path on the
// same choke point as every other tenant-scoped operation.
func TestHandleUninstall_HonorsSingleTenantPin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	svc.cfg.MSTeams.TenantID = "the-only-allowed-tenant"

	r.ErrorIs(svc.HandleUninstall(ctx, "some-other-tenant"), ErrTenantNotAllowed)
}

// TestHandleUninstall_NoConnectionsIsNoop covers a duplicate/retried
// uninstall delivery.
func TestHandleUninstall_NoConnectionsIsNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	r.NoError(svc.HandleUninstall(ctx, "never-connected-tenant"))
}

// TestHandleUninstall_MissingTenantIsRefused guards against a malformed
// activity nuking nothing in particular.
func TestHandleUninstall_MissingTenantIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	r.ErrorIs(svc.HandleUninstall(ctx, ""), ErrNoTenantID)
}

// TestDispatchActivity_RemoveActionUninstalls covers the wiring from the
// activity type down to the service.
func TestDispatchActivity_RemoveActionUninstalls(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	conn := newConnection(ctx, t, svc, "teams-uninst-disp", testTenantID)

	r.NoError(DispatchActivity(ctx, svc, installActivity(testTenantID, "19:channel-a", InstallActionRemove)))

	stored, err := svc.db.GetChannel(ctx, conn.UID)
	r.NoError(err)
	r.False(stored.Enabled)
}

// TestDispatchActivity_UpgradeActionsAreNotUninstalls is the negative
// control: Teams emits `remove-upgrade` around an app version upgrade, and
// treating that as an uninstall would silently break every customer on every
// release.
func TestDispatchActivity_UpgradeActionsAreNotUninstalls(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	conn := newConnection(ctx, t, svc, "teams-upgrade", testTenantID)

	r.NoError(DispatchActivity(ctx, svc, installActivity(testTenantID, "19:channel-a", "remove-upgrade")))

	stored, err := svc.db.GetChannel(ctx, conn.UID)
	r.NoError(err)
	r.True(stored.Enabled, "an app upgrade must not look like an uninstall")
}

// TestHandleInstall_ReinstallClearsUninstalledMarker covers the recovery
// path: reinstalling restores the connection instead of leaving a permanently
// dead row.
func TestHandleInstall_ReinstallClearsUninstalledMarker(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	newConnection(ctx, t, svc, "teams-reinstall", testTenantID)

	_, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)
	r.NoError(svc.HandleUninstall(ctx, testTenantID))

	conn, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)
	r.True(conn.Enabled)

	settings, err := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Empty(settings.UninstalledAt)
	r.Len(settings.Destinations, 1)
}
