package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runMaintenanceWindows(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	now := time.Now()
	win := models.NewMaintenanceWindow(fix.org.UID, "DB upgrade", now.Add(time.Hour), now.Add(2*time.Hour))
	must.NoError(svc.CreateMaintenanceWindow(ctx, win))

	got, err := svc.GetMaintenanceWindow(ctx, fix.org.UID, win.UID)
	must.NoError(err)
	must.Equal("DB upgrade", got.Title)

	upcoming, err := svc.ListMaintenanceWindows(ctx, fix.org.UID, models.ListMaintenanceWindowsFilter{Status: "upcoming"})
	must.NoError(err)
	must.Len(upcoming, 1)

	active, err := svc.ListMaintenanceWindows(ctx, fix.org.UID, models.ListMaintenanceWindowsFilter{Status: "active"})
	must.NoError(err)
	must.Empty(active, "a window starting in an hour is not active yet")

	group := models.NewCheckGroup(fix.org.UID, "Maintenance group", "maintenance-group")
	must.NoError(svc.CreateCheckGroup(ctx, group))

	must.NoError(svc.SetMaintenanceWindowChecks(ctx, win.UID, []string{fix.check.UID}, []string{group.UID}))
	links, err := svc.ListMaintenanceWindowChecks(ctx, win.UID)
	must.NoError(err)
	must.Len(links, 2)

	forGroup, err := svc.ListMaintenanceWindowsForCheckGroup(ctx, group.UID)
	must.NoError(err)
	must.Len(forGroup, 1)

	// Replacing the links drops the group.
	must.NoError(svc.SetMaintenanceWindowChecks(ctx, win.UID, []string{fix.check.UID}, nil))
	links, err = svc.ListMaintenanceWindowChecks(ctx, win.UID)
	must.NoError(err)
	must.Len(links, 1)

	title := "DB upgrade (extended)"
	end := now.Add(3 * time.Hour)
	must.NoError(svc.UpdateMaintenanceWindow(ctx, win.UID, models.MaintenanceWindowUpdate{Title: &title, EndAt: &end}))
	got, err = svc.GetMaintenanceWindow(ctx, fix.org.UID, win.UID)
	must.NoError(err)
	must.Equal(title, got.Title)

	must.NoError(svc.DeleteMaintenanceWindow(ctx, fix.org.UID, win.UID))
	_, err = svc.GetMaintenanceWindow(ctx, fix.org.UID, win.UID)
	must.Error(err)
}
