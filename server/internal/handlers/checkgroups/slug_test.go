package checkgroups_test

// Coverage for changing a check group's slug via UpdateCheckGroup (spec
// 2026-07-28-02): a valid new slug takes effect and the group becomes
// reachable by it, a conflicting slug is rejected, an invalid-format slug is
// rejected, and leaving the field untouched (e.g. a name-only update) never
// changes it.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checkgroups"
)

func TestUpdateCheckGroupSlug(t *testing.T) {
	t.Parallel()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(t.Context()))

	ctx := context.Background()
	org := models.NewOrganization("cg-slug", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	svc := checkgroups.NewService(dbSvc)

	created, err := svc.CreateCheckGroup(ctx, org.Slug, checkgroups.CreateCheckGroupRequest{
		Name: "Payments",
	})
	require.NoError(t, err)
	require.Equal(t, "payments", created.Slug)

	other, err := svc.CreateCheckGroup(ctx, org.Slug, checkgroups.CreateCheckGroupRequest{
		Name: "Billing",
	})
	require.NoError(t, err)
	require.Equal(t, "billing", other.Slug)

	// A name-only update must never touch the slug — this is the regression
	// this spec most needs to prevent (renaming a group must not silently
	// rewrite the identifier DevOps scripts depend on).
	newName := "Payments API"
	untouched, err := svc.UpdateCheckGroup(ctx, org.Slug, created.UID, checkgroups.UpdateCheckGroupRequest{
		Name: &newName,
	})
	require.NoError(t, err)
	require.Equal(t, "payments", untouched.Slug)

	// A valid new slug takes effect and the group becomes reachable by it.
	newSlug := "payments-api"
	updated, err := svc.UpdateCheckGroup(ctx, org.Slug, created.UID, checkgroups.UpdateCheckGroupRequest{
		Slug: &newSlug,
	})
	require.NoError(t, err)
	require.Equal(t, newSlug, updated.Slug)

	byNewSlug, err := svc.GetCheckGroup(ctx, org.Slug, newSlug)
	require.NoError(t, err)
	require.Equal(t, created.UID, byNewSlug.UID)

	_, err = svc.GetCheckGroup(ctx, org.Slug, "payments")
	require.ErrorIs(t, err, checkgroups.ErrCheckGroupNotFound, "the old slug must stop resolving")

	// A slug that collides with another group's is rejected.
	conflict := "billing"
	_, err = svc.UpdateCheckGroup(ctx, org.Slug, created.UID, checkgroups.UpdateCheckGroupRequest{
		Slug: &conflict,
	})
	require.ErrorIs(t, err, checkgroups.ErrSlugConflict)

	// An invalid-format slug is rejected too (uppercase not allowed).
	invalid := "Not-Valid"
	_, err = svc.UpdateCheckGroup(ctx, org.Slug, created.UID, checkgroups.UpdateCheckGroupRequest{
		Slug: &invalid,
	})
	require.ErrorIs(t, err, checkgroups.ErrInvalidSlugFormat)

	// The slug from the successful update above must still be in effect —
	// the two rejected attempts above must not have partially applied.
	stillCurrent, err := svc.GetCheckGroup(ctx, org.Slug, created.UID)
	require.NoError(t, err)
	require.Equal(t, newSlug, stillCurrent.Slug)
}
