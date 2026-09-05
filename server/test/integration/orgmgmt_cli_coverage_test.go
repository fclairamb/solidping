package integration

import (
	"context"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// newZeroOrgUserClient creates a brand-new user with no org membership and
// returns a client logged in as them (org-less access token). Used to exercise
// the requester side of the membership-request flow.
func newZeroOrgUserClient(
	ctx context.Context, t *testing.T, ts *TestServer, email string,
) *client.SolidPingClient {
	t.Helper()

	passwordHash, err := passwords.Hash(TestUserPassword)
	require.NoError(t, err)

	user := models.NewUser(email)
	user.PasswordHash = &passwordHash
	require.NoError(t, ts.Server.DBService().CreateUser(ctx, user))

	apiClient := ts.NewClient()
	_, err = apiClient.Login(ctx, "", email, TestUserPassword)
	require.NoError(t, err)

	return apiClient
}

// TestCLICoverage_OrgCreate covers the createOrg generated-client operation.
func TestCLICoverage_OrgCreate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	// Slug is optional in the schema now (spec 2026-09-05-01) — an explicit one
	// still has to be honored verbatim, which is what this asserts.
	explicitSlug := "cli-cov-org"

	resp, err := apiClient.CreateOrgWithResponse(ctx, client.CreateOrgJSONRequestBody{
		Name: "CLI Coverage Org",
		Slug: &explicitSlug,
	})
	r.NoError(err)
	r.Equal(201, resp.StatusCode())
	r.NotNil(resp.JSON201)
	r.Equal("cli-cov-org", resp.JSON201.Slug)
	r.Equal("CLI Coverage Org", resp.JSON201.Name)
	r.NotEmpty(resp.JSON201.Uid.String())
	r.NotEmpty(resp.JSON201.AccessToken)
	r.NotEmpty(resp.JSON201.RefreshToken)
	r.Positive(resp.JSON201.ExpiresIn)
	r.Equal("Bearer", resp.JSON201.TokenType)

	// A duplicate slug conflicts.
	dupResp, err := apiClient.CreateOrgWithResponse(ctx, client.CreateOrgJSONRequestBody{
		Name: "Dup",
		Slug: &explicitSlug,
	})
	r.NoError(err)
	r.Equal(409, dupResp.StatusCode())

	// ...and with no slug at all the server derives one from the name instead
	// of rejecting the request.
	derivedResp, err := apiClient.CreateOrgWithResponse(ctx, client.CreateOrgJSONRequestBody{
		Name: "Derived Slug Org",
	})
	r.NoError(err)
	r.Equal(201, derivedResp.StatusCode())
	r.NotNil(derivedResp.JSON201)
	r.Equal("derived-slug-org", derivedResp.JSON201.Slug)
}

// TestCLICoverage_OrgSettings covers getOrgSettings and updateOrgSettings with a
// round-trip: read the empty defaults, write, then read back the persisted values.
func TestCLICoverage_OrgSettings(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	getResp, err := apiClient.GetOrgSettingsWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Empty(getResp.JSON200.RegistrationEmailPattern)
	r.Nil(getResp.JSON200.SessionMaxDurationSeconds)

	pattern := "@acme\\.example$"
	sessionMax := 3600
	updateResp, err := apiClient.UpdateOrgSettingsWithResponse(ctx, TestOrgSlug,
		client.UpdateOrgSettingsJSONRequestBody{
			RegistrationEmailPattern:  &pattern,
			SessionMaxDurationSeconds: &sessionMax,
		})
	r.NoError(err)
	r.Equal(200, updateResp.StatusCode())
	r.NotNil(updateResp.JSON200)
	r.Equal(pattern, updateResp.JSON200.RegistrationEmailPattern)
	r.NotNil(updateResp.JSON200.SessionMaxDurationSeconds)
	r.Equal(sessionMax, *updateResp.JSON200.SessionMaxDurationSeconds)

	// Read back — the values must have persisted.
	getResp2, err := apiClient.GetOrgSettingsWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, getResp2.StatusCode())
	r.NotNil(getResp2.JSON200)
	r.Equal(pattern, getResp2.JSON200.RegistrationEmailPattern)
	r.NotNil(getResp2.JSON200.SessionMaxDurationSeconds)
	r.Equal(sessionMax, *getResp2.JSON200.SessionMaxDurationSeconds)

	// Clearing the session override (<=0) reverts to inherited.
	zero := 0
	clearResp, err := apiClient.UpdateOrgSettingsWithResponse(ctx, TestOrgSlug,
		client.UpdateOrgSettingsJSONRequestBody{SessionMaxDurationSeconds: &zero})
	r.NoError(err)
	r.Equal(200, clearResp.StatusCode())
	r.NotNil(clearResp.JSON200)
	r.Nil(clearResp.JSON200.SessionMaxDurationSeconds)
}

// TestCLICoverage_InvitationLifecycle covers createInvitation, listInvitations,
// and deleteInvitation end to end.
func TestCLICoverage_InvitationLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	role := client.CreateInvitationRequestRoleUser
	expiresIn := client.CreateInvitationRequestExpiresInN24h
	app := client.CreateInvitationRequestAppDash0
	createResp, err := apiClient.CreateInvitationWithResponse(ctx, TestOrgSlug,
		client.CreateInvitationJSONRequestBody{
			Email:     openapi_types.Email("invitee@example.com"),
			Role:      &role,
			ExpiresIn: &expiresIn,
			App:       &app,
		})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	r.Equal(openapi_types.Email("invitee@example.com"), createResp.JSON201.Email)
	r.NotEmpty(createResp.JSON201.Token)
	r.NotEmpty(createResp.JSON201.InviteUrl)
	inviteUID := createResp.JSON201.Uid

	// List — the invitation must be present.
	listResp, err := apiClient.ListInvitationsWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	found := false
	for i := range *listResp.JSON200.Data {
		if (*listResp.JSON200.Data)[i].Uid == inviteUID {
			found = true
			r.Equal(openapi_types.Email("invitee@example.com"), (*listResp.JSON200.Data)[i].Email)
		}
	}
	r.True(found, "created invitation must appear in the list")

	// Revoke; a subsequent list must not contain it.
	delResp, err := apiClient.DeleteInvitationWithResponse(ctx, TestOrgSlug, inviteUID)
	r.NoError(err)
	r.Equal(204, delResp.StatusCode())

	listResp2, err := apiClient.ListInvitationsWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, listResp2.StatusCode())
	r.NotNil(listResp2.JSON200)
	if listResp2.JSON200.Data != nil {
		for i := range *listResp2.JSON200.Data {
			r.NotEqual(inviteUID, (*listResp2.JSON200.Data)[i].Uid, "revoked invitation must be gone")
		}
	}
}

// TestCLICoverage_MembershipRequestApprove covers the request → admin-list →
// approve path, plus verifying the approved requester becomes a member.
func TestCLICoverage_MembershipRequestApprove(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	const requesterEmail = "joiner-approve@example.com"
	requester := newZeroOrgUserClient(ctx, t, ts, requesterEmail)

	message := "please let me in"
	createResp, err := requester.CreateMembershipRequestWithResponse(ctx,
		client.CreateMembershipRequestJSONRequestBody{
			OrgSlug: TestOrgSlug,
			Message: &message,
		})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	r.Equal(TestOrgSlug, createResp.JSON201.Organization.Slug)
	r.Equal(client.MembershipRequestSummaryStatus("pending"), createResp.JSON201.Status)
	requestUID := createResp.JSON201.Uid

	// The requester can see their own request.
	mineResp, err := requester.ListMyMembershipRequestsWithResponse(ctx)
	r.NoError(err)
	r.Equal(200, mineResp.StatusCode())
	r.NotNil(mineResp.JSON200)
	r.NotNil(mineResp.JSON200.Data)
	mineFound := false
	for i := range *mineResp.JSON200.Data {
		if (*mineResp.JSON200.Data)[i].Uid == requestUID {
			mineFound = true
		}
	}
	r.True(mineFound, "requester must see their own request")

	// The org admin sees the pending request with the requester's identity.
	admin := cliCovAuthedClient(ctx, t, ts)
	adminListResp, err := admin.ListOrgMembershipRequestsWithResponse(ctx, TestOrgSlug, nil)
	r.NoError(err)
	r.Equal(200, adminListResp.StatusCode())
	r.NotNil(adminListResp.JSON200)
	r.NotNil(adminListResp.JSON200.Data)
	adminFound := false
	for i := range *adminListResp.JSON200.Data {
		view := (*adminListResp.JSON200.Data)[i]
		if view.Uid == requestUID {
			adminFound = true
			r.Equal(openapi_types.Email(requesterEmail), view.User.Email)
		}
	}
	r.True(adminFound, "admin must see the pending request")

	// Approve — the requester becomes a member.
	approveResp, err := admin.ApproveMembershipRequestWithResponse(ctx, TestOrgSlug, requestUID,
		client.ApproveMembershipRequestJSONRequestBody{})
	r.NoError(err)
	r.Equal(200, approveResp.StatusCode())

	membersResp, err := admin.ListMembersWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, membersResp.StatusCode())
	r.NotNil(membersResp.JSON200)
	r.NotNil(membersResp.JSON200.Data)
	memberFound := false
	for i := range *membersResp.JSON200.Data {
		m := (*membersResp.JSON200.Data)[i]
		if m.Email != nil && string(*m.Email) == requesterEmail {
			memberFound = true
		}
	}
	r.True(memberFound, "approved requester must now be a member")
}

// TestCLICoverage_MembershipRequestReject covers the request → admin reject path
// and the status-filtered admin listing.
func TestCLICoverage_MembershipRequestReject(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	requester := newZeroOrgUserClient(ctx, t, ts, "joiner-reject@example.com")

	createResp, err := requester.CreateMembershipRequestWithResponse(ctx,
		client.CreateMembershipRequestJSONRequestBody{OrgSlug: TestOrgSlug})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	requestUID := createResp.JSON201.Uid

	admin := cliCovAuthedClient(ctx, t, ts)
	reason := "not this time"
	rejectResp, err := admin.RejectMembershipRequestWithResponse(ctx, TestOrgSlug, requestUID,
		client.RejectMembershipRequestJSONRequestBody{Reason: &reason})
	r.NoError(err)
	r.Equal(200, rejectResp.StatusCode())

	// Filter the admin list by the rejected status.
	rejected := client.ListOrgMembershipRequestsParamsStatusRejected
	filterResp, err := admin.ListOrgMembershipRequestsWithResponse(ctx, TestOrgSlug,
		&client.ListOrgMembershipRequestsParams{Status: &rejected})
	r.NoError(err)
	r.Equal(200, filterResp.StatusCode())
	r.NotNil(filterResp.JSON200)
	r.NotNil(filterResp.JSON200.Data)
	found := false
	for i := range *filterResp.JSON200.Data {
		view := (*filterResp.JSON200.Data)[i]
		if view.Uid == requestUID {
			found = true
			r.Equal(client.MembershipRequestAdminViewStatus("rejected"), view.Status)
			r.NotNil(view.DecisionReason)
			r.Equal(reason, *view.DecisionReason)
		}
	}
	r.True(found, "rejected request must appear in the status-filtered list")
}

// TestCLICoverage_MembershipRequestCancel covers the request → cancel path.
func TestCLICoverage_MembershipRequestCancel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	requester := newZeroOrgUserClient(ctx, t, ts, "joiner-cancel@example.com")

	createResp, err := requester.CreateMembershipRequestWithResponse(ctx,
		client.CreateMembershipRequestJSONRequestBody{OrgSlug: TestOrgSlug})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	requestUID := createResp.JSON201.Uid

	cancelResp, err := requester.CancelMembershipRequestWithResponse(ctx, requestUID)
	r.NoError(err)
	r.Equal(204, cancelResp.StatusCode())

	// The request now reads as canceled in the requester's own list.
	mineResp, err := requester.ListMyMembershipRequestsWithResponse(ctx)
	r.NoError(err)
	r.Equal(200, mineResp.StatusCode())
	r.NotNil(mineResp.JSON200)
	r.NotNil(mineResp.JSON200.Data)
	found := false
	for i := range *mineResp.JSON200.Data {
		req := (*mineResp.JSON200.Data)[i]
		if req.Uid == requestUID {
			found = true
			r.Equal(client.MembershipRequestSummaryStatus("canceled"), req.Status)
		}
	}
	r.True(found, "canceled request must still be listed with canceled status")
}
