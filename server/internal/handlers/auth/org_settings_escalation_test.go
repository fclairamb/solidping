package auth

// Tests for the org default escalation policy exposed on the org settings
// endpoint (spec 2026-07-19-01): GET returns the current default + blast-radius
// count, PATCH sets/clears it, and an out-of-org UID is rejected.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func seedInheritingChecks(t *testing.T, dbSvc db.Service, orgUID string, slugs ...string) {
	t.Helper()

	for _, slug := range slugs {
		check := models.NewCheck(orgUID, slug, "http")
		check.Enabled = false
		require.NoError(t, dbSvc.CreateCheck(t.Context(), check))
	}
}

func TestOrgSettingsDefaultEscalationPolicy(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("esc-settings", "Esc Settings")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	policy := models.NewEscalationPolicy(org.UID, "org-wide")
	r.NoError(dbSvc.CreateEscalationPolicy(ctx, policy))

	// Two checks with no policy of their own → blast radius of 2.
	seedInheritingChecks(t, dbSvc, org.UID, "inh-one", "inh-two")

	// Unset by default; blast radius reported.
	got, err := svc.GetOrgSettings(ctx, org.Slug)
	r.NoError(err)
	r.Nil(got.DefaultEscalationPolicyUID)
	r.Equal(2, got.InheritingCheckCount)

	// Set it.
	updated, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{
		DefaultEscalationPolicyUID: &policy.UID,
	})
	r.NoError(err)
	r.NotNil(updated.DefaultEscalationPolicyUID)
	r.Equal(policy.UID, *updated.DefaultEscalationPolicyUID)

	// Clear it with an empty string.
	empty := ""
	cleared, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{
		DefaultEscalationPolicyUID: &empty,
	})
	r.NoError(err)
	r.Nil(cleared.DefaultEscalationPolicyUID)
}

func TestOrgSettingsDefaultEscalationPolicyRejectsForeignUID(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	orgA := models.NewOrganization("esc-a", "A")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))
	orgB := models.NewOrganization("esc-b", "B")
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	// Policy belongs to org B.
	foreign := models.NewEscalationPolicy(orgB.UID, "b-policy")
	r.NoError(dbSvc.CreateEscalationPolicy(ctx, foreign))

	_, err := svc.UpdateOrgSettings(ctx, orgA.Slug, UpdateOrgSettingsRequest{
		DefaultEscalationPolicyUID: &foreign.UID,
	})
	r.ErrorIs(err, ErrInvalidEscalationPolicy)

	// A garbage UID is likewise rejected, not silently stored.
	bogus := "not-a-real-policy"
	_, err = svc.UpdateOrgSettings(ctx, orgA.Slug, UpdateOrgSettingsRequest{
		DefaultEscalationPolicyUID: &bogus,
	})
	r.ErrorIs(err, ErrInvalidEscalationPolicy)
}
