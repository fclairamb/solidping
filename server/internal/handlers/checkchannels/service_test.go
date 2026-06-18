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
