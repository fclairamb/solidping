package dbcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runOrganizations(t *testing.T, f *fixture) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := f.svc

	// Previous slugs: a renamed org stays reachable under its old slug until released.
	r.NoError(svc.AddOrganizationPreviousSlug(ctx, f.org.UID, "old-contract"))
	got, err := svc.GetOrganizationByPreviousSlug(ctx, "old-contract")
	r.NoError(err)
	r.Equal(f.org.UID, got.UID)

	prev, err := svc.ListOrganizationPreviousSlugs(ctx, f.org.UID)
	r.NoError(err)
	r.Len(prev, 1)

	r.NoError(svc.ReleaseOrganizationPreviousSlugsForOrg(ctx, f.org.UID))
	_, err = svc.GetOrganizationByPreviousSlug(ctx, "old-contract")
	r.Error(err, "a released slug no longer resolves")

	// UpdateOrganization renames and the new name is read back.
	newName := "Renamed Org"
	r.NoError(svc.UpdateOrganization(ctx, f.org.UID, models.OrganizationUpdate{Name: &newName}))
	org, err := svc.GetOrganization(ctx, f.org.UID)
	r.NoError(err)
	r.Equal(newName, org.Name)

	// Organization providers.
	prov := models.NewOrganizationProvider(f.org.UID, models.ProviderTypeGoogle, "contract-google-id")
	r.NoError(svc.CreateOrganizationProvider(ctx, prov))
	provs, err := svc.ListOrganizationProviders(ctx, f.org.UID)
	r.NoError(err)
	r.Len(provs, 1)

	// Membership requests: create, read, update, approve.
	joiner := models.NewUser("joiner@acme.com")
	r.NoError(svc.CreateUser(ctx, joiner))

	msg := "let me in"
	req := models.NewMembershipRequest(f.org.UID, joiner.UID, &msg)
	r.NoError(svc.CreateMembershipRequest(ctx, req))

	byUID, err := svc.GetMembershipRequest(ctx, req.UID)
	r.NoError(err)
	r.Equal(models.MembershipRequestStatusPending, byUID.Status)

	byPair, err := svc.GetMembershipRequestByOrgAndUser(ctx, f.org.UID, joiner.UID)
	r.NoError(err)
	r.Equal(req.UID, byPair.UID)

	req.Status = models.MembershipRequestStatusApproved
	member := models.NewOrganizationMember(f.org.UID, joiner.UID, models.MemberRoleUser)
	r.NoError(svc.ApproveMembershipRequest(ctx, req, member))

	approved, err := svc.GetMembershipRequest(ctx, req.UID)
	r.NoError(err)
	r.Equal(models.MembershipRequestStatusApproved, approved.Status)

	gotMember, err := svc.GetMemberByUserAndOrg(ctx, joiner.UID, f.org.UID)
	r.NoError(err)
	r.Equal(models.MemberRoleUser, gotMember.Role)

	reason := "changed my mind"
	req.DecisionReason = &reason
	r.NoError(svc.UpdateMembershipRequest(ctx, req))
	updated, err := svc.GetMembershipRequest(ctx, req.UID)
	r.NoError(err)
	r.NotNil(updated.DecisionReason)
	r.Equal(reason, *updated.DecisionReason)
}
