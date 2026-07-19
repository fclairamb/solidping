package checkgroups_test

// Round-trip test for the group-level escalationPolicyUid the REST API now
// exposes (spec 2026-07-19-01): create-with, get, update-set, and update-clear.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checkgroups"
)

func TestCheckGroupEscalationPolicyRoundTrip(t *testing.T) {
	t.Parallel()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(t.Context()))

	ctx := context.Background()
	org := models.NewOrganization("cg-esc", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	policy := models.NewEscalationPolicy(org.UID, "grp-policy")
	require.NoError(t, dbSvc.CreateEscalationPolicy(ctx, policy))

	svc := checkgroups.NewService(dbSvc)

	// Create with a policy attached.
	created, err := svc.CreateCheckGroup(ctx, org.Slug, checkgroups.CreateCheckGroupRequest{
		Name:                "Payments",
		EscalationPolicyUID: &policy.UID,
	})
	require.NoError(t, err)
	require.NotNil(t, created.EscalationPolicyUID)
	require.Equal(t, policy.UID, *created.EscalationPolicyUID)

	// Get echoes it back.
	got, err := svc.GetCheckGroup(ctx, org.Slug, created.UID)
	require.NoError(t, err)
	require.NotNil(t, got.EscalationPolicyUID)
	require.Equal(t, policy.UID, *got.EscalationPolicyUID)

	// Update with empty string clears it.
	empty := ""
	cleared, err := svc.UpdateCheckGroup(ctx, org.Slug, created.UID, checkgroups.UpdateCheckGroupRequest{
		EscalationPolicyUID: &empty,
	})
	require.NoError(t, err)
	require.Nil(t, cleared.EscalationPolicyUID, "empty string must clear the group policy")

	// Update with a UID sets it again.
	reset, err := svc.UpdateCheckGroup(ctx, org.Slug, created.UID, checkgroups.UpdateCheckGroupRequest{
		EscalationPolicyUID: &policy.UID,
	})
	require.NoError(t, err)
	require.NotNil(t, reset.EscalationPolicyUID)
	require.Equal(t, policy.UID, *reset.EscalationPolicyUID)

	// Omitting the field leaves it untouched.
	newName := "Payments 2"
	untouched, err := svc.UpdateCheckGroup(ctx, org.Slug, created.UID, checkgroups.UpdateCheckGroupRequest{
		Name: &newName,
	})
	require.NoError(t, err)
	require.NotNil(t, untouched.EscalationPolicyUID)
	require.Equal(t, policy.UID, *untouched.EscalationPolicyUID)
}
