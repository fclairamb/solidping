package escalationpolicies_test

// Integration tests for the escalation-policy service against an in-memory
// sqlite backend, focused on uid addressing for GET/PATCH/DELETE.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/escalationpolicies"
)

func newPolicyTestService(t *testing.T) (*escalationpolicies.Service, *models.Organization) {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)

	t.Cleanup(func() { _ = dbSvc.Close() })

	require.NoError(t, dbSvc.Initialize(t.Context()))

	org := models.NewOrganization("test", "")
	require.NoError(t, dbSvc.CreateOrganization(t.Context(), org))

	return escalationpolicies.NewService(dbSvc), org
}

func createTestPolicy(
	t *testing.T, svc *escalationpolicies.Service, orgUID, name string,
) *models.EscalationPolicy {
	t.Helper()

	policy, err := svc.CreatePolicy(t.Context(), &escalationpolicies.CreatePolicyInput{
		OrganizationUID: orgUID,
		Name:            name,
		Steps: []escalationpolicies.StepInput{
			{
				DelaySeconds: 0,
				Targets: []escalationpolicies.TargetInput{
					{Type: models.EscalationTargetAllAdmins},
				},
			},
		},
	})
	require.NoError(t, err)

	return policy
}

func TestServiceGetPolicy(t *testing.T) {
	t.Parallel()

	svc, org := newPolicyTestService(t)
	policy := createTestPolicy(t, svc, org.UID, "oncall")

	t.Run("by uid", func(t *testing.T) {
		t.Parallel()

		detail, err := svc.GetPolicy(t.Context(), org.UID, policy.UID)
		require.NoError(t, err)
		require.Equal(t, policy.UID, detail.Policy.UID)
		require.Len(t, detail.Steps, 1)
	})

	t.Run("slug-shaped identifier returns not found", func(t *testing.T) {
		t.Parallel()

		_, err := svc.GetPolicy(t.Context(), org.UID, "oncall")
		require.ErrorIs(t, err, escalationpolicies.ErrPolicyNotFound)
	})

	t.Run("unknown uid returns not found", func(t *testing.T) {
		t.Parallel()

		_, err := svc.GetPolicy(t.Context(), org.UID, "00000000-0000-0000-0000-000000000000")
		require.ErrorIs(t, err, escalationpolicies.ErrPolicyNotFound)
	})
}

func TestServiceUpdatePolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org := newPolicyTestService(t)
	policy := createTestPolicy(t, svc, org.UID, "oncall")

	newName := "On-Call Policy"

	// Update addressed by UID.
	detail, err := svc.UpdatePolicy(t.Context(), org.UID, policy.UID, &escalationpolicies.UpdatePolicyInput{
		Name: &newName,
	})
	r.NoError(err)
	r.Equal("On-Call Policy", detail.Policy.Name)

	// A slug-shaped identifier no longer resolves → not found.
	_, err = svc.UpdatePolicy(t.Context(), org.UID, "oncall", &escalationpolicies.UpdatePolicyInput{
		Name: &newName,
	})
	r.ErrorIs(err, escalationpolicies.ErrPolicyNotFound)
}

func TestServiceDeletePolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org := newPolicyTestService(t)

	// Delete by UID.
	p1 := createTestPolicy(t, svc, org.UID, "byuid")
	r.NoError(svc.DeletePolicy(t.Context(), org.UID, p1.UID))
	_, err := svc.GetPolicy(t.Context(), org.UID, p1.UID)
	r.ErrorIs(err, escalationpolicies.ErrPolicyNotFound)

	// A slug-shaped identifier no longer resolves → not found.
	r.ErrorIs(svc.DeletePolicy(t.Context(), org.UID, "ghost"), escalationpolicies.ErrPolicyNotFound)
}
