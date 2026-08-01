package msteams

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestHandleUninstall_FansOutAcrossOrgs mirrors the Slack uninstall
// acceptance criterion: removing the app in a tenant must stop routing for
// EVERY org connected to that tenant, and must leave an unrelated tenant's
// connection untouched.
//
// Unlike Slack the rows are kept (the credentials are instance-level, so a
// reinstall restores service) — they are marked uninstalled and disabled.
func TestHandleUninstall_FansOutAcrossOrgs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, _ := setupService(t)

	connA := newConnection(t, ctx, svc, "teams-uninst-a", testTenantID)
	connB := newConnection(t, ctx, svc, "teams-uninst-b", testTenantID)
	connOther := newConnection(t, ctx, svc, "teams-uninst-oth", "other-tenant")

	// Give A a destination so we can prove routing state is cleared.
	_, err := svc.HandleInstall(ctx, installActivity(testTenantID, "19:channel-a", InstallActionAdd))
	r.NoError(err)

	r.NoError(svc.HandleUninstall(ctx, testTenantID))

	for _, uid := range []string{connA.UID, connB.UID} {
		conn, getErr := svc.db.GetChannel(ctx, uid)
		r.NoError(getErr)
		r.False(conn.Enabled, "uninstalled connection must stop routing")

		settings, parseErr := models.MSTeamsBotSettingsFromJSONMap(conn.Settings)
		r.NoError(parseErr)
		r.NotEmpty(settings.UninstalledAt)
		r.Empty(settings.Destinations)
		r.Empty(settings.ChannelID)
	}

	other, err := svc.db.GetChannel(ctx, connOther.UID)
	r.NoError(err)
	r.True(other.Enabled, "an unrelated tenant must be untouched")
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

	conn := newConnection(t, ctx, svc, "teams-uninst-disp", testTenantID)

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

	conn := newConnection(t, ctx, svc, "teams-upgrade", testTenantID)

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

	newConnection(t, ctx, svc, "teams-reinstall", testTenantID)

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
