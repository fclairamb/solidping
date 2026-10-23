package dbcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

//nolint:funlen // linear contract scenario
func runOrganizations(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	// Previous slugs: a renamed org stays reachable under its old slug until released.
	must.NoError(svc.AddOrganizationPreviousSlug(ctx, fix.org.UID, "old-contract"))
	got, err := svc.GetOrganizationByPreviousSlug(ctx, "old-contract")
	must.NoError(err)
	must.Equal(fix.org.UID, got.UID)

	prev, err := svc.ListOrganizationPreviousSlugs(ctx, fix.org.UID)
	must.NoError(err)
	must.Len(prev, 1)

	must.NoError(svc.ReleaseOrganizationPreviousSlugsForOrg(ctx, fix.org.UID))
	_, err = svc.GetOrganizationByPreviousSlug(ctx, "old-contract")
	must.Error(err, "a released slug no longer resolves")

	// UpdateOrganization renames and the new name is read back.
	newName := "Renamed Org"
	must.NoError(svc.UpdateOrganization(ctx, fix.org.UID, models.OrganizationUpdate{Name: &newName}))
	org, err := svc.GetOrganization(ctx, fix.org.UID)
	must.NoError(err)
	must.Equal(newName, org.Name)

	// Organization providers.
	prov := models.NewOrganizationProvider(fix.org.UID, models.ProviderTypeGoogle, "contract-google-id")
	must.NoError(svc.CreateOrganizationProvider(ctx, prov))
	provs, err := svc.ListOrganizationProviders(ctx, fix.org.UID)
	must.NoError(err)
	must.Len(provs, 1)

	// Membership requests: create, read, update, approve.
	joiner := models.NewUser("joiner@acme.com")
	must.NoError(svc.CreateUser(ctx, joiner))

	msg := "let me in"
	req := models.NewMembershipRequest(fix.org.UID, joiner.UID, &msg)
	must.NoError(svc.CreateMembershipRequest(ctx, req))

	byUID, err := svc.GetMembershipRequest(ctx, req.UID)
	must.NoError(err)
	must.Equal(models.MembershipRequestStatusPending, byUID.Status)

	byPair, err := svc.GetMembershipRequestByOrgAndUser(ctx, fix.org.UID, joiner.UID)
	must.NoError(err)
	must.Equal(req.UID, byPair.UID)

	req.Status = models.MembershipRequestStatusApproved
	member := models.NewOrganizationMember(fix.org.UID, joiner.UID, models.MemberRoleUser)
	must.NoError(svc.ApproveMembershipRequest(ctx, req, member))

	approved, err := svc.GetMembershipRequest(ctx, req.UID)
	must.NoError(err)
	must.Equal(models.MembershipRequestStatusApproved, approved.Status)

	gotMember, err := svc.GetMemberByUserAndOrg(ctx, joiner.UID, fix.org.UID)
	must.NoError(err)
	must.Equal(models.MemberRoleUser, gotMember.Role)

	reason := "changed my mind"
	req.DecisionReason = &reason
	must.NoError(svc.UpdateMembershipRequest(ctx, req))
	updated, err := svc.GetMembershipRequest(ctx, req.UID)
	must.NoError(err)
	must.NotNil(updated.DecisionReason)
	must.Equal(reason, *updated.DecisionReason)
}
