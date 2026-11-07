package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

var errInjected = errors.New("injected failure")

// faultDB wraps a real database and fails the calls a test asks for, so the
// best-effort error paths of the personal org logic run against real state.
type faultDB struct {
	db.Service

	listStateEntries    func(ctx context.Context, orgUID *string, prefix string) ([]*models.StateEntry, error)
	listMembershipReqs  func() error
	getAutoPersonalOrg  func(ctx context.Context, userUID string, includeDeleted bool) (*models.Organization, error)
	createOrganization  func() error
	getMemberByUserOrg  func() error
	failNonOwnerMembers bool
	failAnyMember       bool
	failGetOrgBySlug    bool
	failCreateToken     bool
}

func (d *faultDB) ListStateEntries(ctx context.Context, orgUID *string, prefix string) ([]*models.StateEntry, error) {
	if d.listStateEntries != nil {
		return d.listStateEntries(ctx, orgUID, prefix)
	}

	return d.Service.ListStateEntries(ctx, orgUID, prefix)
}

func (d *faultDB) ListMembershipRequests(
	ctx context.Context, filter models.ListMembershipRequestsFilter,
) ([]*models.MembershipRequest, error) {
	if d.listMembershipReqs != nil {
		if err := d.listMembershipReqs(); err != nil {
			return nil, err
		}
	}

	return d.Service.ListMembershipRequests(ctx, filter)
}

func (d *faultDB) GetAutoPersonalOrgByOwner(
	ctx context.Context, userUID string, includeDeleted bool,
) (*models.Organization, error) {
	if d.getAutoPersonalOrg != nil {
		return d.getAutoPersonalOrg(ctx, userUID, includeDeleted)
	}

	return d.Service.GetAutoPersonalOrgByOwner(ctx, userUID, includeDeleted)
}

func (d *faultDB) CreateOrganization(ctx context.Context, org *models.Organization) error {
	if d.createOrganization != nil {
		if err := d.createOrganization(); err != nil {
			return err
		}
	}

	return d.Service.CreateOrganization(ctx, org)
}

func (d *faultDB) GetMemberByUserAndOrg(
	ctx context.Context, userUID, orgUID string,
) (*models.OrganizationMember, error) {
	if d.getMemberByUserOrg != nil {
		if err := d.getMemberByUserOrg(); err != nil {
			return nil, err
		}
	}

	return d.Service.GetMemberByUserAndOrg(ctx, userUID, orgUID)
}

func (d *faultDB) CreateOrganizationMember(ctx context.Context, member *models.OrganizationMember) error {
	if d.failAnyMember || (d.failNonOwnerMembers && member.Role != models.MemberRoleOwner) {
		return errInjected
	}

	return d.Service.CreateOrganizationMember(ctx, member)
}

func (d *faultDB) GetOrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	if d.failGetOrgBySlug {
		return nil, errInjected
	}

	return d.Service.GetOrganizationBySlug(ctx, slug)
}

func (d *faultDB) CreateUserToken(ctx context.Context, token *models.UserToken) error {
	if d.failCreateToken {
		return errInjected
	}

	return d.Service.CreateUserToken(ctx, token)
}

func newFaultFixture(t *testing.T) (*personalOrgFixture, *faultDB, context.Context) {
	t.Helper()

	f, ctx := newPersonalOrgFixture(t)
	fault := &faultDB{Service: f.db}
	f.svc.db = fault

	return f, fault, ctx
}

func inviteTo(ctx context.Context, t *testing.T, f *personalOrgFixture, slug, email string) *models.Organization {
	t.Helper()

	org, inviter := f.seededOrg(ctx, t, slug, "boss@"+slug+".example")
	_, err := f.svc.CreateInvitation(ctx, org.Slug, inviter.UID, InviteRequest{
		Email: email, Role: string(models.MemberRoleViewer), ExpiresIn: "24h", App: "dash0",
	})
	require.NoError(t, err)

	return org
}

// TestPersonalOrgInvitationScanSkipsUnusableEntries: index entries that name
// no org, repeat an org, hold no invitation for the user, or point at a
// deleted org are skipped, and the user ends up with a personal org.
func TestPersonalOrgInvitationScanSkipsUnusableEntries(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newPersonalOrgFixture(t)
	user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")

	r.NoError(f.db.SetStateEntry(ctx, nil, inviteKeyPrefix+"nil-value", nil, nil))
	r.NoError(f.db.SetStateEntry(ctx, nil, inviteKeyPrefix+"no-org", &models.JSONMap{keyInviteOrgUID: ""}, nil))

	// Two invitations of the same org, both for someone else.
	other := inviteTo(ctx, t, f, "others", "bob@acme.com")
	_, err := f.svc.CreateInvitation(ctx, other.Slug, "", InviteRequest{
		Email: "carol@acme.com", Role: string(models.MemberRoleViewer), ExpiresIn: "24h", App: "dash0",
	})
	r.NoError(err)

	// An invitation for the user in an org that was deleted since.
	gone := inviteTo(ctx, t, f, "gone", user.Email)
	r.NoError(f.db.DeleteOrganization(ctx, gone.UID))

	member := f.svc.ensurePersonalOrg(ctx, user)
	r.NotNil(member)
	r.Equal(models.MemberRoleOwner, member.Role)
	r.Equal("alice", member.Organization.Slug)
}

// TestPersonalOrgInvitationJoinFailure: an invitation that cannot be used
// leaves the user org-less instead of giving them another org.
func TestPersonalOrgInvitationJoinFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, fault, ctx := newFaultFixture(t)
	user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")
	inviteTo(ctx, t, f, "inviter", user.Email)

	fault.failNonOwnerMembers = true

	r.Nil(f.svc.ensurePersonalOrg(ctx, user))

	orgs, err := f.db.ListMembersByUser(ctx, user.UID)
	r.NoError(err)
	r.Empty(orgs)
}

// TestPersonalOrgBestEffortOnStoreFailures: a failing lookup never fails the
// sign-in; it either falls through to a personal org or keeps /no-org.
func TestPersonalOrgBestEffortOnStoreFailures(t *testing.T) {
	t.Parallel()

	t.Run("invitation index unreadable falls through to a personal org", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, fault, ctx := newFaultFixture(t)
		user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")

		fault.listStateEntries = func(
			ctx context.Context, orgUID *string, prefix string,
		) ([]*models.StateEntry, error) {
			if orgUID == nil && prefix == inviteKeyPrefix {
				return nil, errInjected
			}

			return f.db.ListStateEntries(ctx, orgUID, prefix)
		}

		member := f.svc.ensurePersonalOrg(ctx, user)
		r.NotNil(member)
		r.Equal("alice", member.Organization.Slug)
	})

	t.Run("a user without an email still gets a personal org", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, _, ctx := newFaultFixture(t)
		user := models.NewUser("")
		user.Name = "Nomail"
		r.NoError(f.db.CreateUser(ctx, user))

		member := f.svc.ensurePersonalOrg(ctx, user)
		r.NotNil(member)
		r.Equal("nomail", member.Organization.Slug)
	})

	t.Run("membership requests unreadable keeps the user org-less", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, fault, ctx := newFaultFixture(t)
		user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")
		fault.listMembershipReqs = func() error { return errInjected }

		r.Nil(f.svc.ensurePersonalOrg(ctx, user))

		previous, err := f.db.GetAutoPersonalOrgByOwner(ctx, user.UID, true)
		r.NoError(err)
		r.Nil(previous, "no org created in doubt")
	})

	t.Run("personal org lookup failing keeps the user org-less", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, fault, ctx := newFaultFixture(t)
		user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")
		fault.getAutoPersonalOrg = func(context.Context, string, bool) (*models.Organization, error) {
			return nil, errInjected
		}

		r.Nil(f.svc.ensurePersonalOrg(ctx, user))
	})
}

// TestPersonalOrgReturnsTheExistingLiveOne: a personal org that already
// exists is reused, whether or not its membership is readable.
func TestPersonalOrgReturnsTheExistingLiveOne(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, fault, ctx := newFaultFixture(t)
	user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")

	created := f.svc.createPersonalOrg(ctx, user)
	r.NotNil(created)

	again := f.svc.createPersonalOrg(ctx, user)
	r.NotNil(again)
	r.Equal(created.Organization.UID, again.Organization.UID)
	r.Equal(models.MemberRoleOwner, again.Role)

	// Membership row not readable (yet): the OWNER membership is synthesized.
	fault.getMemberByUserOrg = func() error { return errInjected }

	synthetic := f.svc.personalOrgMember(ctx, user.UID, created.Organization)
	r.Equal(models.MemberRoleOwner, synthetic.Role)
	r.Equal(user.UID, synthetic.UserUID)
	r.Equal(created.Organization.UID, synthetic.Organization.UID)
}

// TestPersonalOrgCreationRace: when creation is refused because a concurrent
// sign-in won, the winner's org is used; with no winner, the user stays
// org-less.
func TestPersonalOrgCreationRace(t *testing.T) {
	t.Parallel()

	t.Run("the concurrent winner is used", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, fault, ctx := newFaultFixture(t)
		user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")

		winner := f.svc.createPersonalOrg(ctx, user)
		r.NotNil(winner)

		fault.getAutoPersonalOrg = func(
			ctx context.Context, userUID string, includeDeleted bool,
		) (*models.Organization, error) {
			if includeDeleted {
				return nil, nil //nolint:nilnil // simulates "not created yet" at the first check
			}

			return f.db.GetAutoPersonalOrgByOwner(ctx, userUID, includeDeleted)
		}
		fault.createOrganization = func() error { return errInjected }

		member := f.svc.createPersonalOrg(ctx, user)
		r.NotNil(member)
		r.Equal(winner.Organization.UID, member.Organization.UID)
	})

	t.Run("creation failing without a winner", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, fault, ctx := newFaultFixture(t)
		user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")
		fault.createOrganization = func() error { return errInjected }

		r.Nil(f.svc.createPersonalOrg(ctx, user))
	})
}

func TestFirstNameOfRejectsNonAlphanumericTokens(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Empty(firstNameOf("---"))
	r.Empty(firstNameOf("   "))
	r.Equal("Zoé", firstNameOf("  Zoé Durand"))
}

// TestCreateOrgFailures: the failures of the shared creation path reach the caller.
func TestCreateOrgFailures(t *testing.T) {
	t.Parallel()

	t.Run("slug lookup failing", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, fault, ctx := newFaultFixture(t)
		user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")
		fault.failGetOrgBySlug = true

		_, err := f.svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Shop", Slug: "shop"}, Context{})
		r.ErrorIs(err, errInjected)
	})

	t.Run("membership creation failing", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, fault, ctx := newFaultFixture(t)
		user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")
		fault.failAnyMember = true

		_, err := f.svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Shop", Slug: "shop"}, Context{})
		r.ErrorIs(err, errInjected)
	})
}

// TestCreateOrgRefusals: an invalid or taken slug is refused, and a session
// that cannot be minted fails the creation call.
func TestCreateOrgRefusals(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, fault, ctx := newFaultFixture(t)
	user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")

	_, err := f.svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Shop", Slug: "Not A Slug!"}, Context{})
	r.ErrorIs(err, ErrInvalidOrgSlug)

	f.seededOrg(ctx, t, "taken", "boss@taken.example")

	_, err = f.svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Shop", Slug: "taken"}, Context{})
	r.ErrorIs(err, ErrOrgSlugTaken)

	fault.failCreateToken = true

	_, err = f.svc.CreateOrg(ctx, user.UID, CreateOrgRequest{Name: "Shop", Slug: "shop"}, Context{})
	r.ErrorIs(err, errInjected)
}
