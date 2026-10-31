package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

var errConsentForced = errors.New("forced consent lookup failure")

// consentFaultDB fails the lookups ConsentOrg and HasMembership depend on.
type consentFaultDB struct {
	db.Service

	failOrg     bool
	failMember  bool
	failUser    bool
	failMembers bool
}

func (c *consentFaultDB) GetOrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	if c.failOrg {
		return nil, fmt.Errorf("get org: %w", errConsentForced)
	}

	return c.Service.GetOrganizationBySlug(ctx, slug)
}

func (c *consentFaultDB) GetMemberByUserAndOrg(
	ctx context.Context, userUID, orgUID string,
) (*models.OrganizationMember, error) {
	if c.failMember {
		return nil, fmt.Errorf("get member: %w", errConsentForced)
	}

	return c.Service.GetMemberByUserAndOrg(ctx, userUID, orgUID)
}

func (c *consentFaultDB) GetUser(ctx context.Context, uid string) (*models.User, error) {
	if c.failUser {
		return nil, fmt.Errorf("get user: %w", errConsentForced)
	}

	return c.Service.GetUser(ctx, uid)
}

func (c *consentFaultDB) ListMembersByUser(ctx context.Context, uid string) ([]*models.OrganizationMember, error) {
	if c.failMembers {
		return nil, fmt.Errorf("list members: %w", errConsentForced)
	}

	return c.Service.ListMembersByUser(ctx, uid)
}

func TestConsentOrg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		slug    string
		user    func(f oauthFixture) string
		fault   consentFaultDB
		wantOrg bool
		wantErr error
	}{
		{name: "member of the named org", slug: "oauth-svc-org", wantOrg: true},
		{name: "no org in the session", slug: "", wantErr: errConsentNoOrg},
		{name: "named org does not exist", slug: "ghost", wantErr: errConsentNoOrg},
		{name: "org lookup fails", slug: "oauth-svc-org", fault: consentFaultDB{failOrg: true}, wantErr: errConsentForced},
		{
			name: "membership lookup fails", slug: "oauth-svc-org",
			fault: consentFaultDB{failMember: true}, wantErr: errConsentForced,
		},
		{
			name: "user left the org", slug: "oauth-svc-org",
			user: func(_ oauthFixture) string { return "unknown-user" }, wantErr: errConsentNoOrg,
		},
		{
			name: "user lookup fails for a non-member", slug: "oauth-svc-org",
			user:  func(_ oauthFixture) string { return "unknown-user" },
			fault: consentFaultDB{failUser: true}, wantErr: errConsentForced,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f := setupOAuthService(t)
			r.NoError(f.db.CreateOrganizationMember(f.ctx,
				models.NewOrganizationMember(f.org.UID, f.user.UID, models.MemberRoleUser)))

			fault := tc.fault
			fault.Service = f.db
			f.svc.db = &fault

			uid := f.user.UID
			if tc.user != nil {
				uid = tc.user(f)
			}

			org, err := f.svc.ConsentOrg(f.ctx, &auth.Claims{UserUID: uid, OrgSlug: tc.slug})
			if tc.wantErr != nil {
				r.ErrorIs(err, tc.wantErr)
				r.Nil(org)

				return
			}

			r.NoError(err)
			r.Equal(f.org.UID, org.UID)
		})
	}
}

// TestConsentOrgSuperAdminWithoutMembership: a super admin may consent in any
// org, member or not; an ordinary user who is not a member may not.
func TestConsentOrgSuperAdminWithoutMembership(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := setupOAuthService(t)

	admin := models.NewUser("root@acme.com")
	admin.SuperAdmin = true
	r.NoError(f.db.CreateUser(f.ctx, admin))

	outsider := models.NewUser("outsider@acme.com")
	r.NoError(f.db.CreateUser(f.ctx, outsider))

	org, err := f.svc.ConsentOrg(f.ctx, &auth.Claims{UserUID: admin.UID, OrgSlug: f.org.Slug})
	r.NoError(err)
	r.Equal(f.org.UID, org.UID)

	_, err = f.svc.ConsentOrg(f.ctx, &auth.Claims{UserUID: outsider.UID, OrgSlug: f.org.Slug})
	r.ErrorIs(err, errConsentNoOrg)
}

func TestHasMembershipListFailure(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := setupOAuthService(t)
	r.NoError(f.db.CreateOrganizationMember(f.ctx,
		models.NewOrganizationMember(f.org.UID, f.user.UID, models.MemberRoleUser)))

	has, err := f.svc.HasMembership(f.ctx, f.user.UID)
	r.NoError(err)
	r.True(has, "the seeded user belongs to the seeded org")

	f.svc.db = &consentFaultDB{Service: f.db, failMembers: true}

	_, err = f.svc.HasMembership(f.ctx, f.user.UID)
	r.ErrorIs(err, errConsentForced)
}

// TestAuthorizeConsentLookupFailuresAre500: a storage failure while resolving
// the consent org, or the user's memberships, is an internal error rather than
// a redirect to a page that could not work.
func TestAuthorizeConsentLookupFailuresAre500(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		token string
		fault consentFaultDB
	}{
		{name: "consent org lookup", token: "session", fault: consentFaultDB{failOrg: true}},
		{name: "membership list", token: "orgless", fault: consentFaultDB{failMembers: true}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f := setupConsentFixture(t)
			fault := tc.fault
			fault.Service = f.handler.svc.db
			f.handler.svc.db = &fault

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
				PathAuthorize+"?"+f.params().Encode(), http.NoBody)
			req.Header.Set("Authorization", "Bearer "+f.tokens[tc.token])

			rec := httptest.NewRecorder()
			r.NoError(f.handler.Authorize(rec, req))
			r.Equal(http.StatusInternalServerError, rec.Code, rec.Body.String())
		})
	}
}
