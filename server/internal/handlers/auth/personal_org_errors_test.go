package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// personalOrgFaultDB wraps a real db.Service and forces individual lookups of
// the personal-org flow to fail or to answer with crafted rows.
type personalOrgFaultDB struct {
	db.Service

	failCreateOrg      bool
	failFoundOrgLookup bool
	failMemberLookup   bool
	failStateList      bool
	failRequestList    bool
	failParamList      bool
	failBump           bool
	failCreateToken    bool
	failCreateMember   bool
	params             []*models.Parameter
}

func (p *personalOrgFaultDB) CreateOrganization(ctx context.Context, org *models.Organization) error {
	if p.failCreateOrg {
		return fmt.Errorf("create org: %w", errFallbackForced)
	}

	return p.Service.CreateOrganization(ctx, org)
}

// GetOrganizationBySlug fails only for an organization that exists, so the
// slug-availability probe of CreateOrg keeps answering "free".
func (p *personalOrgFaultDB) GetOrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	org, err := p.Service.GetOrganizationBySlug(ctx, slug)
	if err == nil && p.failFoundOrgLookup {
		return nil, fmt.Errorf("get org: %w", errFallbackForced)
	}

	return org, err
}

func (p *personalOrgFaultDB) GetMemberByUserAndOrg(
	ctx context.Context, userUID, orgUID string,
) (*models.OrganizationMember, error) {
	if p.failMemberLookup {
		return nil, fmt.Errorf("get member: %w", errFallbackForced)
	}

	return p.Service.GetMemberByUserAndOrg(ctx, userUID, orgUID)
}

func (p *personalOrgFaultDB) ListStateEntries(
	ctx context.Context, orgUID *string, prefix string,
) ([]*models.StateEntry, error) {
	if p.failStateList {
		return nil, fmt.Errorf("list state: %w", errFallbackForced)
	}

	return p.Service.ListStateEntries(ctx, orgUID, prefix)
}

func (p *personalOrgFaultDB) ListMembershipRequests(
	ctx context.Context, filter models.ListMembershipRequestsFilter,
) ([]*models.MembershipRequest, error) {
	if p.failRequestList {
		return nil, fmt.Errorf("list requests: %w", errFallbackForced)
	}

	return p.Service.ListMembershipRequests(ctx, filter)
}

func (p *personalOrgFaultDB) ListOrgParametersByKey(
	ctx context.Context, key string,
) ([]*models.Parameter, error) {
	if p.failParamList {
		return nil, fmt.Errorf("list params: %w", errFallbackForced)
	}

	if p.params != nil {
		return p.params, nil
	}

	return p.Service.ListOrgParametersByKey(ctx, key)
}

func (p *personalOrgFaultDB) BumpGlobalCounter(
	ctx context.Context, key string, limit int, ttl time.Duration,
) (bool, error) {
	if p.failBump {
		return false, fmt.Errorf("bump counter: %w", errFallbackForced)
	}

	return p.Service.BumpGlobalCounter(ctx, key, limit, ttl)
}

func (p *personalOrgFaultDB) CreateUserToken(ctx context.Context, token *models.UserToken) error {
	if p.failCreateToken {
		return fmt.Errorf("create token: %w", errFallbackForced)
	}

	return p.Service.CreateUserToken(ctx, token)
}

func (p *personalOrgFaultDB) CreateOrganizationMember(ctx context.Context, member *models.OrganizationMember) error {
	if p.failCreateMember {
		return fmt.Errorf("create member: %w", errFallbackForced)
	}

	return p.Service.CreateOrganizationMember(ctx, member)
}

func newPersonalOrgFaultFixture(t *testing.T) (*loginAuditFixture, *personalOrgFaultDB, *models.User, context.Context) {
	t.Helper()

	f, ctx := newLoginAuditFixture(t)
	fault := &personalOrgFaultDB{Service: f.db}
	f.svc.db = fault

	user := f.user(ctx, t, "jane@acme.com")
	user.Name = "Jane Doe"

	return f, fault, user, ctx
}

func TestEnsurePersonalOrgBestEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		set  func(p *personalOrgFaultDB)
	}{
		{name: "organization creation fails", set: func(p *personalOrgFaultDB) { p.failCreateOrg = true }},
		{name: "created organization cannot be loaded", set: func(p *personalOrgFaultDB) { p.failFoundOrgLookup = true }},
		{name: "membership cannot be loaded", set: func(p *personalOrgFaultDB) { p.failMemberLookup = true }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f, fault, user, ctx := newPersonalOrgFaultFixture(t)
			tc.set(fault)

			r.Nil(f.svc.ensurePersonalOrg(ctx, user), "a failure degrades to the org-less session")
		})
	}
}

func TestEnsurePersonalOrgSkipsNilAndSuperAdmin(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, _, user, ctx := newPersonalOrgFaultFixture(t)
	user.SuperAdmin = true

	r.Nil(f.svc.ensurePersonalOrg(ctx, nil))
	r.Nil(f.svc.ensurePersonalOrg(ctx, user))

	orgs, err := f.db.ListOrganizations(ctx)
	r.NoError(err)
	r.Len(orgs, 1, "only the fixture org exists")
}

func TestPersonalFirstNameFallsBackToEmailLocalPart(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("Jane", personalFirstName(&models.User{Name: "  Jane  Doe", Email: "x@acme.com"}))
	r.Equal("jane.doe", personalFirstName(&models.User{Email: "jane.doe@acme.com"}))
}

func TestHasPendingInvitationIgnoresUnusableIndexEntries(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, _, _, ctx := newPersonalOrgFaultFixture(t)
	ttl := time.Hour

	// Two index entries for the same org: the second must be skipped, and an
	// entry without an org UID must be ignored altogether.
	r.NoError(f.db.SetStateEntry(ctx, nil, inviteKeyPrefix+"a", &models.JSONMap{keyInviteOrgUID: f.org.UID}, &ttl))
	r.NoError(f.db.SetStateEntry(ctx, nil, inviteKeyPrefix+"b", &models.JSONMap{keyInviteOrgUID: f.org.UID}, &ttl))
	r.NoError(f.db.SetStateEntry(ctx, nil, inviteKeyPrefix+"c", &models.JSONMap{"other": "x"}, &ttl))
	r.NoError(f.db.SetStateEntry(ctx, nil, inviteKeyPrefix+"d", nil, &ttl))

	r.False(f.svc.hasPendingInvitation(ctx, "jane@acme.com"), "no org-scoped invitation matches")

	r.NoError(f.db.SetStateEntry(ctx, &f.org.UID, inviteKeyPrefix+"a",
		&models.JSONMap{keyEmail: "jane@acme.com", "role": string(models.MemberRoleUser)}, &ttl))
	r.True(f.svc.hasPendingInvitation(ctx, "jane@acme.com"))
}

func TestPendingChecksTreatLookupFailuresAsNegative(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, fault, user, ctx := newPersonalOrgFaultFixture(t)

	fault.failStateList = true
	fault.failRequestList = true
	fault.failParamList = true

	r.False(f.svc.hasPendingInvitation(ctx, user.Email))
	r.False(f.svc.hasPendingMembershipRequest(ctx, user.UID))
	r.Empty(f.svc.autoJoinOrgUIDs(ctx, user.Email))
}

func TestAutoJoinOrgUIDsSkipsUnusableParameters(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, fault, _, ctx := newPersonalOrgFaultFixture(t)
	orgUID := f.org.UID
	param := func(org *string, value any) *models.Parameter {
		return &models.Parameter{
			OrganizationUID: org, Key: "registration.email_pattern", Value: models.JSONMap{"value": value},
		}
	}

	fault.params = []*models.Parameter{
		param(nil, ".*@acme\\.com"),       // no owning org
		param(&orgUID, ""),                // feature disabled
		param(&orgUID, 42),                // not a string
		param(&orgUID, ".*"),              // unsafe: no @ separator
		param(&orgUID, ".*@other\\.com"),  // does not match
		param(&orgUID, "^.*@acme\\.com$"), // matches
	}

	r.Equal([]string{orgUID}, f.svc.autoJoinOrgUIDs(ctx, "jane@acme.com"))
}
