package incidents

// White-box tests for resolveEscalationPolicyUID (spec 2026-07-19-01): the
// precedence chain check → group → org default → none, opt-in behavior when no
// org default is set, and the soft-delete fallback the delete-guard promises.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func newResolverTestDB(t *testing.T) (db.Service, *models.Organization) {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(t.Context()))

	org := models.NewOrganization("resolver-test", "")
	require.NoError(t, dbSvc.CreateOrganization(t.Context(), org))

	return dbSvc, org
}

func mkPolicy(t *testing.T, dbSvc db.Service, orgUID, name string) string {
	t.Helper()

	policy := models.NewEscalationPolicy(orgUID, name)
	require.NoError(t, dbSvc.CreateEscalationPolicy(t.Context(), policy))

	return policy.UID
}

func mkGroup(t *testing.T, dbSvc db.Service, orgUID, slug, policyUID string) string {
	t.Helper()

	group := models.NewCheckGroup(orgUID, slug, slug)
	if policyUID != "" {
		group.EscalationPolicyUID = &policyUID
	}
	require.NoError(t, dbSvc.CreateCheckGroup(t.Context(), group))

	return group.UID
}

func setOrgDefault(t *testing.T, dbSvc db.Service, orgUID, policyUID string) {
	t.Helper()

	require.NoError(t, dbSvc.UpdateOrganization(t.Context(), orgUID, models.OrganizationUpdate{
		DefaultEscalationPolicyUID: &policyUID,
	}))
}

func ptr(s string) *string { return &s }

func TestResolveEscalationPolicyUID_Precedence(t *testing.T) {
	t.Parallel()

	dbSvc, org := newResolverTestDB(t)
	ctx := context.Background()

	checkPolicy := mkPolicy(t, dbSvc, org.UID, "check-policy")
	groupPolicy := mkPolicy(t, dbSvc, org.UID, "group-policy")
	orgPolicy := mkPolicy(t, dbSvc, org.UID, "org-policy")

	groupWithPolicy := mkGroup(t, dbSvc, org.UID, "grp-with", groupPolicy)
	groupNoPolicy := mkGroup(t, dbSvc, org.UID, "grp-none", "")

	setOrgDefault(t, dbSvc, org.UID, orgPolicy)

	t.Run("check policy wins over group and org default", func(t *testing.T) {
		t.Parallel()
		check := &models.Check{
			OrganizationUID:     org.UID,
			EscalationPolicyUID: ptr(checkPolicy),
			CheckGroupUID:       ptr(groupWithPolicy),
		}
		require.Equal(t, checkPolicy, resolveEscalationPolicyUID(ctx, dbSvc, check))
	})

	t.Run("group policy wins over org default when check has none", func(t *testing.T) {
		t.Parallel()
		check := &models.Check{
			OrganizationUID: org.UID,
			CheckGroupUID:   ptr(groupWithPolicy),
		}
		require.Equal(t, groupPolicy, resolveEscalationPolicyUID(ctx, dbSvc, check))
	})

	t.Run("org default applies when check and group both resolve to none", func(t *testing.T) {
		t.Parallel()
		check := &models.Check{
			OrganizationUID: org.UID,
			CheckGroupUID:   ptr(groupNoPolicy),
		}
		require.Equal(t, orgPolicy, resolveEscalationPolicyUID(ctx, dbSvc, check))
	})

	t.Run("org default applies to a check with no group", func(t *testing.T) {
		t.Parallel()
		check := &models.Check{OrganizationUID: org.UID}
		require.Equal(t, orgPolicy, resolveEscalationPolicyUID(ctx, dbSvc, check))
	})
}

// TestResolveEscalationPolicyUID_OrgDefaultOptIn is the positive control: the
// SAME fixtures resolve to none when no org default is set, and to the default
// once it is — proving the org default is what changes behavior, and that an
// org without one keeps the historical check → group → none semantics.
func TestResolveEscalationPolicyUID_OrgDefaultOptIn(t *testing.T) {
	t.Parallel()

	dbSvc, org := newResolverTestDB(t)
	ctx := context.Background()

	orgPolicy := mkPolicy(t, dbSvc, org.UID, "org-policy")
	group := mkGroup(t, dbSvc, org.UID, "grp", "")

	// A check with no direct policy and a group with no policy.
	check := &models.Check{OrganizationUID: org.UID, CheckGroupUID: ptr(group)}

	// Control: no org default → none (today's behavior, unchanged).
	require.Equal(t, "", resolveEscalationPolicyUID(ctx, dbSvc, check),
		"with no org default the check must resolve to no policy")

	// Set the default → the same check now pages via the default.
	setOrgDefault(t, dbSvc, org.UID, orgPolicy)
	require.Equal(t, orgPolicy, resolveEscalationPolicyUID(ctx, dbSvc, check),
		"with an org default set the same check must resolve to it")

	// A check that names its own (bare, no group) also picks up the default.
	bare := &models.Check{OrganizationUID: org.UID}
	require.Equal(t, orgPolicy, resolveEscalationPolicyUID(ctx, dbSvc, bare))
}

// TestResolveEscalationPolicyUID_DeletedPolicyFallsBack asserts the delete-guard
// promise: deleting an assigned policy (a soft delete — the FK never nulls the
// stale UID) makes its checks fall back to inherited escalation (here, the org
// default) rather than page a now-nonexistent policy or silently stop.
func TestResolveEscalationPolicyUID_DeletedPolicyFallsBack(t *testing.T) {
	t.Parallel()

	dbSvc, org := newResolverTestDB(t)
	ctx := context.Background()

	assigned := mkPolicy(t, dbSvc, org.UID, "assigned")
	orgPolicy := mkPolicy(t, dbSvc, org.UID, "org-default")
	setOrgDefault(t, dbSvc, org.UID, orgPolicy)

	check := &models.Check{OrganizationUID: org.UID, EscalationPolicyUID: ptr(assigned)}

	// While the assigned policy is alive it wins.
	require.Equal(t, assigned, resolveEscalationPolicyUID(ctx, dbSvc, check))

	// Delete it (soft delete) → resolution falls back to the org default even
	// though check.EscalationPolicyUID still physically points at the dead UID.
	require.NoError(t, dbSvc.DeleteEscalationPolicy(ctx, assigned))
	require.Equal(t, orgPolicy, resolveEscalationPolicyUID(ctx, dbSvc, check))

	// With no org default either, it resolves to none (never the dead policy).
	require.NoError(t, dbSvc.UpdateOrganization(ctx, org.UID, models.OrganizationUpdate{
		ClearDefaultEscalationPolicyUID: true,
	}))
	require.Equal(t, "", resolveEscalationPolicyUID(ctx, dbSvc, check))
}
