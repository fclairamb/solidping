package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const inviteeEmail = "invitee@acme.com"

// inviteWorld is an organization with a pending invitation for inviteeEmail.
type inviteWorld struct {
	*edgeWorld

	slug    string
	inviter *models.User
	invite  *InviteResponse
}

func newInviteWorld(t *testing.T) *inviteWorld {
	t.Helper()

	w := newEdgeWorld(t)
	slug, inviter, _ := w.signedIn(t, models.MemberRoleOwner)

	invite, err := w.svc.CreateInvitation(w.ctx, slug, inviter.UID, InviteRequest{
		Email: inviteeEmail, Role: string(models.MemberRoleUser), ExpiresIn: "24h", App: "dash0",
	})
	require.NoError(t, err)

	return &inviteWorld{edgeWorld: w, slug: slug, inviter: inviter, invite: invite}
}

// account creates the invited account, so it accepts signed in.
func (w *inviteWorld) account(t *testing.T) *models.User {
	t.Helper()

	user := models.NewUser(inviteeEmail)
	require.NoError(t, w.dbSvc.CreateUser(w.ctx, user))

	return user
}

func (w *inviteWorld) accept(callerUID string) (*LoginResponse, error) {
	return w.svc.AcceptInvite(w.ctx, AcceptInviteRequest{
		Token: w.invite.Token, Name: "Invitee", Password: "testpass1234",
	}, callerUID)
}

// pending reports whether the invitation can still be looked up.
func (w *inviteWorld) pending(t *testing.T) bool {
	t.Helper()

	info, err := w.svc.GetInviteInfo(w.ctx, w.invite.Token)
	if err != nil {
		require.ErrorIs(t, err, ErrInvitationNotFound)

		return false
	}

	require.Equal(t, w.slug, info.OrgSlug)

	return true
}

func (w *inviteWorld) isMember(t *testing.T, userUID string) bool {
	t.Helper()

	members, err := w.dbSvc.ListMembersByUser(w.ctx, userUID)
	require.NoError(t, err)

	return len(members) > 0
}

func TestCreateInvitationRefusesTheOwnerRole(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newInviteWorld(t)

	_, err := w.svc.CreateInvitation(w.ctx, w.slug, w.inviter.UID, InviteRequest{
		Email: "bob@acme.com", Role: string(models.MemberRoleOwner), ExpiresIn: "24h", App: "dash0",
	})
	r.ErrorIs(err, ErrInvalidCredentials)
	r.ErrorContains(err, "invalid role")

	list, err := w.svc.ListInvitations(w.ctx, w.slug)
	r.NoError(err)
	r.Len(list.Data, 1, "only the invitation of the fixture exists")
}

func TestListInvitationsSkipsEntriesWithoutValue(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newInviteWorld(t)
	orgUID := w.org(t, w.slug).UID
	r.NoError(w.dbSvc.SetStateEntry(w.ctx, &orgUID, inviteKeyPrefix+"no-value", nil, nil))

	list, err := w.svc.ListInvitations(w.ctx, w.slug)
	r.NoError(err)
	r.Len(list.Data, 1)
	r.Equal(w.invite.UID, list.Data[0].UID)
	r.Equal(inviteeEmail, list.Data[0].Email)
}

// TestInvitationLookup covers the two-step lookup of an invitation token:
// the global index entry, then the entry of the organization it names.
func TestInvitationLookup(t *testing.T) {
	t.Parallel()

	t.Run("index entry naming no organization", func(t *testing.T) {
		t.Parallel()

		w := newInviteWorld(t)
		require.NoError(t, w.dbSvc.SetStateEntry(w.ctx, nil,
			inviteKeyPrefix+hashPendingToken("stray-token"), &models.JSONMap{"note": "no org"}, nil))

		_, err := w.svc.GetInviteInfo(w.ctx, "stray-token")
		require.ErrorIs(t, err, ErrInvitationNotFound)
	})

	t.Run("organization deleted since", func(t *testing.T) {
		t.Parallel()

		w := newInviteWorld(t)
		require.True(t, w.pending(t))
		require.NoError(t, w.dbSvc.DeleteOrganization(w.ctx, w.org(t, w.slug).UID))

		_, err := w.svc.GetInviteInfo(w.ctx, w.invite.Token)
		require.ErrorIs(t, err, ErrInvitationNotFound)
	})

	failures := []struct {
		name    string
		fault   sqlFault
		wantMsg string
	}{
		{"organization entry cannot be read", failingSQL(`FROM "state_entries"`).after(1), "failed to get invitation"},
		{"organization cannot be read", failingSQL(`FROM "organizations"`), "failed to get organization"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newInviteWorld(t)

			var err error

			w.fault.during(t, tt.fault, func() {
				_, err = w.svc.GetInviteInfo(w.ctx, w.invite.Token)
			})
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, tt.wantMsg)
			require.NotErrorIs(t, err, ErrInvitationNotFound, "a store failure is not a missing invitation")
		})
	}
}

func TestRevokeInvitationSurfacesAStoreFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newInviteWorld(t)

	var err error

	w.fault.during(t, softDeletedStateEntries, func() {
		err = w.svc.RevokeInvitation(w.ctx, w.slug, w.invite.UID)
	})
	r.ErrorIs(err, context.Canceled)
	r.True(w.pending(t), "the invitation is still there")

	r.NoError(w.svc.RevokeInvitation(w.ctx, w.slug, w.invite.UID))
	r.False(w.pending(t))
}

// TestAcceptInviteSurfacesStoreFailures: an acceptance that hits a store
// failure answers an error naming the step and never a session.
func TestAcceptInviteSurfacesStoreFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// existing: the invited address already has an account, signed in.
		existing    bool
		fault       sqlFault
		wantMsg     string
		wantMember  bool
		wantPending bool
	}{
		{
			name: "looking the invited account up", fault: failingSQL(`FROM "users"`),
			wantPending: true,
		},
		{
			name: "creating the account", fault: failingSQL(`INSERT INTO "users"`),
			wantMsg: "failed to create user", wantPending: true,
		},
		{
			name: "creating the membership", existing: true,
			fault:   failingSQL(`INSERT INTO "organization_members"`),
			wantMsg: "failed to create membership", wantPending: true,
		},
		{
			name: "reading the membership back", existing: true,
			fault:   failingSQL(`FROM "organization_members"`).after(1),
			wantMsg: "failed to get membership", wantMember: true,
		},
		{
			name: "storing the session", existing: true,
			fault:   failingSQL(`INSERT INTO "user_tokens"`),
			wantMsg: "failed to store refresh token", wantMember: true,
		},
		{
			name: "listing the organizations of the account", existing: true,
			fault:   failingSQL(`FROM "organization_members"`).after(2),
			wantMsg: "failed to list user organizations", wantMember: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newInviteWorld(t)

			callerUID := ""
			if tt.existing {
				callerUID = w.account(t).UID
			}

			var (
				resp *LoginResponse
				err  error
			)

			w.fault.during(t, tt.fault, func() { resp, err = w.accept(callerUID) })
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, tt.wantMsg)
			require.Nil(t, resp)
			require.Equal(t, tt.wantPending, w.pending(t))

			if tt.existing {
				require.Equal(t, tt.wantMember, w.isMember(t, callerUID))
			}

			if !tt.wantPending {
				return
			}

			// Nothing was consumed: the same link works once the store is back.
			resp, err = w.accept(callerUID)
			require.NoError(t, err)
			require.Equal(t, w.slug, resp.Organization.Slug)
			require.Equal(t, string(models.MemberRoleUser), resp.User.Role)
		})
	}
}

// TestInvitationWithoutAddressIsLinkOnly: with no address to write to, the
// invitation exists and reports that no email was sent.
func TestInvitationWithoutAddressIsLinkOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newInviteWorld(t)
	w.svc.fullCfg.Email.Enabled = true

	invite, err := w.svc.CreateInvitation(w.ctx, w.slug, w.inviter.UID, InviteRequest{
		Role: string(models.MemberRoleViewer), ExpiresIn: "24h", App: "dash0",
	})
	r.NoError(err)
	r.False(invite.EmailSent)
	r.Contains(invite.InviteURL, invite.Token)

	info, err := w.svc.GetInviteInfo(w.ctx, invite.Token)
	r.NoError(err)
	r.Equal(string(models.MemberRoleViewer), info.Role)
}
