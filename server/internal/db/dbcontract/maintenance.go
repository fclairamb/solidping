package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runMaintenanceWindows(t *testing.T, f *fixture) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := f.svc

	now := time.Now()
	win := models.NewMaintenanceWindow(f.org.UID, "DB upgrade", now.Add(time.Hour), now.Add(2*time.Hour))
	r.NoError(svc.CreateMaintenanceWindow(ctx, win))

	got, err := svc.GetMaintenanceWindow(ctx, f.org.UID, win.UID)
	r.NoError(err)
	r.Equal("DB upgrade", got.Title)

	upcoming, err := svc.ListMaintenanceWindows(ctx, f.org.UID, models.ListMaintenanceWindowsFilter{Status: "upcoming"})
	r.NoError(err)
	r.Len(upcoming, 1)

	active, err := svc.ListMaintenanceWindows(ctx, f.org.UID, models.ListMaintenanceWindowsFilter{Status: "active"})
	r.NoError(err)
	r.Empty(active, "a window starting in an hour is not active yet")

	group := models.NewCheckGroup(f.org.UID, "Maintenance group", "maintenance-group")
	r.NoError(svc.CreateCheckGroup(ctx, group))

	r.NoError(svc.SetMaintenanceWindowChecks(ctx, win.UID, []string{f.check.UID}, []string{group.UID}))
	links, err := svc.ListMaintenanceWindowChecks(ctx, win.UID)
	r.NoError(err)
	r.Len(links, 2)

	forGroup, err := svc.ListMaintenanceWindowsForCheckGroup(ctx, group.UID)
	r.NoError(err)
	r.Len(forGroup, 1)

	// Replacing the links drops the group.
	r.NoError(svc.SetMaintenanceWindowChecks(ctx, win.UID, []string{f.check.UID}, nil))
	links, err = svc.ListMaintenanceWindowChecks(ctx, win.UID)
	r.NoError(err)
	r.Len(links, 1)

	title := "DB upgrade (extended)"
	end := now.Add(3 * time.Hour)
	r.NoError(svc.UpdateMaintenanceWindow(ctx, win.UID, models.MaintenanceWindowUpdate{Title: &title, EndAt: &end}))
	got, err = svc.GetMaintenanceWindow(ctx, f.org.UID, win.UID)
	r.NoError(err)
	r.Equal(title, got.Title)

	r.NoError(svc.DeleteMaintenanceWindow(ctx, f.org.UID, win.UID))
	_, err = svc.GetMaintenanceWindow(ctx, f.org.UID, win.UID)
	r.Error(err)
}
