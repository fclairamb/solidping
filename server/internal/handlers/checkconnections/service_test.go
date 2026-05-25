package checkconnections_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checkconnections"
)

// testSetup spins up an in-memory SQLite DB with one org, one check, and the
// supplied integration connections, returning the service and the org/check.
func testSetup(t *testing.T, conns ...*models.Channel) (
	context.Context, *checkconnections.Service, *models.Organization, *models.Check,
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

	return ctx, checkconnections.NewService(dbSvc), org, check
}

// TestSetConnectionsRejectsNonNotifyIntegration verifies binding a Freebox
// (CanNotify == false) integration as a notify target is rejected. This is the
// silent-no-op bug fix.
func TestSetConnectionsRejectsNonNotifyIntegration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	freebox := models.NewChannel("", models.ConnectionTypeFreebox, "Home Freebox")
	ctx, svc, org, check := testSetup(t, freebox)

	err := svc.SetConnections(ctx, org.Slug, check.UID, checkconnections.SetConnectionsRequest{
		ConnectionUIDs: []string{freebox.UID},
	})
	r.ErrorIs(err, checkconnections.ErrNotNotifyCapable)
}

// TestAddConnectionRejectsNonNotifyIntegration is the single-add variant of
// the rejection.
func TestAddConnectionRejectsNonNotifyIntegration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	freebox := models.NewChannel("", models.ConnectionTypeFreebox, "Home Freebox")
	ctx, svc, org, check := testSetup(t, freebox)

	err := svc.AddConnection(ctx, org.Slug, check.UID, freebox.UID)
	r.ErrorIs(err, checkconnections.ErrNotNotifyCapable)
}

// TestSetConnectionsAllowsNotifyIntegration is the positive guard: a
// notify-capable integration (webhook) binds without error.
func TestSetConnectionsAllowsNotifyIntegration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	webhook := models.NewChannel("", models.ConnectionTypeWebhook, "Ops webhook")
	ctx, svc, org, check := testSetup(t, webhook)

	err := svc.SetConnections(ctx, org.Slug, check.UID, checkconnections.SetConnectionsRequest{
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

	webhook := models.NewChannel("", models.ConnectionTypeWebhook, "Ops webhook")
	ctx, svc, org, check := testSetup(t, webhook)

	r.NoError(svc.AddConnection(ctx, org.Slug, check.UID, webhook.UID))
}

// TestSetConnectionsRejectsMixedWhenOneIsNonNotify verifies that a batch with
// even one non-notify integration is rejected wholesale.
func TestSetConnectionsRejectsMixedWhenOneIsNonNotify(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	webhook := models.NewChannel("", models.ConnectionTypeWebhook, "Ops webhook")
	freebox := models.NewChannel("", models.ConnectionTypeFreebox, "Home Freebox")
	ctx, svc, org, check := testSetup(t, webhook, freebox)

	err := svc.SetConnections(ctx, org.Slug, check.UID, checkconnections.SetConnectionsRequest{
		ConnectionUIDs: []string{webhook.UID, freebox.UID},
	})
	r.ErrorIs(err, checkconnections.ErrNotNotifyCapable)
}
