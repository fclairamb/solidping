package checkgroups_test

// Coverage for the derived group status and memberStatusCounts fields (spec
// 2026-08-01-01): both List and Get expose them, a freshly created group with
// no members reports "created", and disabled / soft-deleted checks are
// excluded from the counts.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checkgroups"
)

// newMemberCheck creates and persists a check in the given group with the
// given status and enabled flag, returning it for further mutation (e.g.
// soft-delete) by the caller.
func newMemberCheck(
	ctx context.Context, t *testing.T, dbSvc *sqlite.Service,
	orgUID, groupUID, slug string, status models.CheckStatus, enabled bool,
) *models.Check {
	t.Helper()

	check := models.NewCheck(orgUID, slug, "http")
	check.CheckGroupUID = &groupUID
	check.Status = status
	check.Enabled = enabled
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	return check
}

func TestCheckGroupStatusRollupOnListAndGet(t *testing.T) {
	t.Parallel()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(t.Context()))

	ctx := context.Background()
	org := models.NewOrganization("cg-status", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	svc := checkgroups.NewService(dbSvc)

	// A brand-new, empty group must roll up to "created" with no counts.
	empty, err := svc.CreateCheckGroup(ctx, org.Slug, checkgroups.CreateCheckGroupRequest{
		Name: "Empty Group",
	})
	require.NoError(t, err)
	require.Equal(t, "created", empty.Status)
	require.Empty(t, empty.MemberStatusCounts)

	// A group with one up and one down enabled member is "degraded", plus a
	// disabled-down member and a soft-deleted-down member that must both be
	// excluded from the counts (otherwise this would roll up to "down").
	degraded, err := svc.CreateCheckGroup(ctx, org.Slug, checkgroups.CreateCheckGroupRequest{
		Name: "Degraded Group",
	})
	require.NoError(t, err)

	newMemberCheck(ctx, t, dbSvc, org.UID, degraded.UID, "up-1", models.CheckStatusUp, true)
	newMemberCheck(ctx, t, dbSvc, org.UID, degraded.UID, "down-1", models.CheckStatusDown, true)
	newMemberCheck(ctx, t, dbSvc, org.UID, degraded.UID, "disabled-down", models.CheckStatusDown, false)

	deletedCheck := newMemberCheck(ctx, t, dbSvc, org.UID, degraded.UID, "deleted-down", models.CheckStatusDown, true)
	require.NoError(t, dbSvc.DeleteCheck(ctx, deletedCheck.UID))

	// GetCheckGroup reflects the rollup and per-status counts.
	got, err := svc.GetCheckGroup(ctx, org.Slug, degraded.UID)
	require.NoError(t, err)
	require.Equal(t, "degraded", got.Status)
	require.Equal(t, map[string]int{"up": 1, "down": 1}, got.MemberStatusCounts)

	// ListCheckGroups reflects the same rollup and counts for every group.
	list, err := svc.ListCheckGroups(ctx, org.Slug)
	require.NoError(t, err)
	require.Len(t, list, 2)

	byUID := make(map[string]checkgroups.CheckGroupResponse, len(list))
	for _, g := range list {
		byUID[g.UID] = g
	}

	require.Equal(t, "created", byUID[empty.UID].Status)
	require.Empty(t, byUID[empty.UID].MemberStatusCounts)

	require.Equal(t, "degraded", byUID[degraded.UID].Status)
	require.Equal(t, map[string]int{"up": 1, "down": 1}, byUID[degraded.UID].MemberStatusCounts)
}

// TestCheckGroupStatusAllDownIsDown covers the "all considered members down"
// branch end-to-end through the service, distinguishing it from "degraded".
func TestCheckGroupStatusAllDownIsDown(t *testing.T) {
	t.Parallel()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(t.Context()))

	ctx := context.Background()
	org := models.NewOrganization("cg-status-down", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	svc := checkgroups.NewService(dbSvc)

	group, err := svc.CreateCheckGroup(ctx, org.Slug, checkgroups.CreateCheckGroupRequest{
		Name: "All Down",
	})
	require.NoError(t, err)

	newMemberCheck(ctx, t, dbSvc, org.UID, group.UID, "down-1", models.CheckStatusDown, true)
	newMemberCheck(ctx, t, dbSvc, org.UID, group.UID, "down-2", models.CheckStatusDown, true)

	got, err := svc.GetCheckGroup(ctx, org.Slug, group.UID)
	require.NoError(t, err)
	require.Equal(t, "down", got.Status)
	require.Equal(t, map[string]int{"down": 2}, got.MemberStatusCounts)
}
