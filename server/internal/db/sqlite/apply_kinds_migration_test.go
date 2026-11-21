package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestApplyKindsColumnsSQLite pins migration 039 on SQLite: slug and
// managed_by round-trip on channels and maintenance windows, managed_by on
// status pages, slugs are unique per organization among live rows, and a
// soft-deleted row frees its slug.
func TestApplyKindsColumnsSQLite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(svc.CreateOrganization(ctx, org))
	other := models.NewOrganization("other", "Other")
	r.NoError(svc.CreateOrganization(ctx, other))

	slug, owner := "alerts", "prod"

	chA := models.NewIntegration(org.UID, models.ConnectionTypeNtfy, "A")
	chA.Slug, chA.ManagedBy = &slug, &owner
	r.NoError(svc.CreateChannel(ctx, chA))

	got, err := svc.GetChannel(ctx, chA.UID)
	r.NoError(err)
	r.Equal(slug, *got.Slug)
	r.Equal(owner, *got.ManagedBy)

	dup := models.NewIntegration(org.UID, models.ConnectionTypeNtfy, "B")
	dup.Slug = &slug
	r.Error(svc.CreateChannel(ctx, dup), "slug is unique per organization")

	// NULL slugs never collide; another organization may reuse a slug.
	r.NoError(svc.CreateChannel(ctx, models.NewIntegration(org.UID, models.ConnectionTypeNtfy, "C")))
	r.NoError(svc.CreateChannel(ctx, models.NewIntegration(org.UID, models.ConnectionTypeNtfy, "D")))

	foreign := models.NewIntegration(other.UID, models.ConnectionTypeNtfy, "E")
	foreign.Slug = &slug
	r.NoError(svc.CreateChannel(ctx, foreign))

	r.NoError(svc.DeleteChannel(ctx, chA.UID))

	dup2 := models.NewIntegration(org.UID, models.ConnectionTypeNtfy, "F")
	dup2.Slug = &slug
	r.NoError(svc.CreateChannel(ctx, dup2), "a deleted row frees its slug")

	start := time.Now().Add(time.Hour)
	win := models.NewMaintenanceWindow(org.UID, "W", start, start.Add(time.Hour))
	win.Slug, win.ManagedBy = &slug, &owner
	r.NoError(svc.CreateMaintenanceWindow(ctx, win))

	winDup := models.NewMaintenanceWindow(org.UID, "W2", start, start.Add(time.Hour))
	winDup.Slug = &slug
	r.Error(svc.CreateMaintenanceWindow(ctx, winDup))

	newSlug := "renamed"
	r.NoError(svc.UpdateMaintenanceWindow(ctx, win.UID, models.MaintenanceWindowUpdate{Slug: &newSlug}))

	gotWin, err := svc.GetMaintenanceWindow(ctx, org.UID, win.UID)
	r.NoError(err)
	r.Equal(newSlug, *gotWin.Slug)
	r.Equal(owner, *gotWin.ManagedBy)

	page := models.NewStatusPage(org.UID, "Status", "status")
	r.NoError(svc.CreateStatusPage(ctx, page))
	r.NoError(svc.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{ManagedBy: &owner}))

	gotPage, err := svc.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)
	r.Equal(owner, *gotPage.ManagedBy)
}
