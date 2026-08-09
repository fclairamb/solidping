package auth

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// seedOwnedOrg creates an org with one owner and returns both.
func seedOwnedOrg(
	ctx context.Context, t *testing.T, dbService db.Service, slug string,
) (*models.Organization, *models.User) {
	t.Helper()

	org := models.NewOrganization(slug, slug)
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	user := models.NewUser("owner@" + slug + ".example")
	require.NoError(t, dbService.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleOwner)
	now := time.Now()
	member.JoinedAt = &now
	require.NoError(t, dbService.CreateOrganizationMember(ctx, member))

	return org, user
}

// TestUpdateOrgProfileRenamesAndKeepsTheOldSlugResolvable pins the rename
// contract: the org answers on its new slug, the old slug is retained as an
// alias pointing at it, and the caller gets a session that already works
// against the new slug.
func TestUpdateOrgProfileRenamesAndKeepsTheOldSlugResolvable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbService, ctx := setupAuthTestService(t)
	org, owner := seedOwnedOrg(ctx, t, dbService, "before")

	// Control: nothing aliases "before" while it is the live slug.
	_, err := dbService.GetOrganizationByPreviousSlug(ctx, "before")
	r.Error(err)

	newSlug := "after"
	name := "After Inc"

	resp, err := svc.UpdateOrgProfile(ctx, "before", owner.UID,
		UpdateOrgProfileRequest{Slug: &newSlug, Name: &name}, Context{})
	r.NoError(err)
	r.Equal("after", resp.Slug)
	r.Equal("After Inc", resp.Name)
	r.Equal("before", resp.PreviousSlug)

	// The org moved.
	live, err := dbService.GetOrganizationBySlug(ctx, "after")
	r.NoError(err)
	r.Equal(org.UID, live.UID)

	_, err = dbService.GetOrganizationBySlug(ctx, "before")
	r.Error(err, "the previous slug must no longer be a live org slug")

	// …and the old slug still resolves through the alias, to the same org.
	aliased, err := dbService.GetOrganizationByPreviousSlug(ctx, "before")
	r.NoError(err)
	r.Equal(org.UID, aliased.UID)
	r.Equal("after", aliased.Slug)

	// The response session is scoped to the NEW slug, which is what keeps the
	// caller's very next request from 403ing on the token-scope check.
	r.NotEmpty(resp.AccessToken)
	r.NotEmpty(resp.RefreshToken)

	claims, err := svc.ValidateToken(ctx, resp.AccessToken)
	r.NoError(err)
	r.Equal("after", claims.OrgSlug)
}

// TestUpdateOrgProfileOldTokenIsScopedToTheOldSlug pins the reason the rename
// re-mints a session at all: a token minted before the rename carries the old
// slug and is refused against the new one, while the freshly minted token is
// accepted. Both halves must hold or the re-mint is pointless.
func TestUpdateOrgProfileOldTokenIsScopedToTheOldSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbService, ctx := setupAuthTestService(t)
	org, owner := seedOwnedOrg(ctx, t, dbService, "stale-old")

	staleToken, err := svc.generateAccessToken(owner.UID, org.Slug, string(models.MemberRoleOwner), "session")
	r.NoError(err)

	staleClaims, err := svc.ValidateToken(ctx, staleToken)
	r.NoError(err)

	// Control: the stale token works against the org as it stands today.
	_, denial := AuthorizeOrgAccess(ctx, dbService, staleClaims, owner, "stale-old")
	r.Equal(OrgAccessGranted, denial)

	newSlug := "stale-new"
	resp, err := svc.UpdateOrgProfile(ctx, "stale-old", owner.UID,
		UpdateOrgProfileRequest{Slug: &newSlug}, Context{})
	r.NoError(err)

	_, denial = AuthorizeOrgAccess(ctx, dbService, staleClaims, owner, "stale-new")
	r.Equal(OrgAccessDenied, denial, "a token minted for the old slug must not pass on the new one")

	freshClaims, err := svc.ValidateToken(ctx, resp.AccessToken)
	r.NoError(err)

	_, denial = AuthorizeOrgAccess(ctx, dbService, freshClaims, owner, "stale-new")
	r.Equal(OrgAccessGranted, denial)
}

// TestRefreshAfterRenameYieldsTheNewSlug pins the self-healing path for every
// OTHER session: Refresh derives the org from the token's organization UID, so
// a session that was live during the rename comes back scoped to the new slug
// without the user re-authenticating.
func TestRefreshAfterRenameYieldsTheNewSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbService, ctx := setupAuthTestService(t)
	org, owner := seedOwnedOrg(ctx, t, dbService, "refresh-old")

	refreshValue, err := svc.generateRefreshToken()
	r.NoError(err)

	refreshToken := models.NewUserToken(owner.UID, &org.UID, refreshValue, models.TokenTypeRefresh)
	expiry := time.Now().Add(24 * time.Hour)
	refreshToken.ExpiresAt = &expiry
	r.NoError(dbService.CreateUserToken(ctx, refreshToken))

	newSlug := "refresh-new"
	_, err = svc.UpdateOrgProfile(ctx, "refresh-old", owner.UID,
		UpdateOrgProfileRequest{Slug: &newSlug}, Context{})
	r.NoError(err)

	refreshed, err := svc.Refresh(ctx, refreshValue)
	r.NoError(err)

	claims, err := svc.ValidateToken(ctx, refreshed.AccessToken)
	r.NoError(err)
	r.Equal("refresh-new", claims.OrgSlug, "a pre-rename session must self-heal onto the new slug")
}

// TestCreateOrgClaimsASlugHeldOnlyAsAnAlias pins the release rule: an alias
// never blocks a real organization, and it stops resolving the instant the slug
// is claimed.
func TestCreateOrgClaimsASlugHeldOnlyAsAnAlias(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbService, ctx := setupAuthTestService(t)
	renamed, owner := seedOwnedOrg(ctx, t, dbService, "contested")

	newSlug := "moved-on"
	_, err := svc.UpdateOrgProfile(ctx, "contested", owner.UID,
		UpdateOrgProfileRequest{Slug: &newSlug}, Context{})
	r.NoError(err)

	// Control: the alias resolves before anybody claims the slug.
	aliased, err := dbService.GetOrganizationByPreviousSlug(ctx, "contested")
	r.NoError(err)
	r.Equal(renamed.UID, aliased.UID)

	claimant := models.NewUser("claimant@example.com")
	r.NoError(dbService.CreateUser(ctx, claimant))

	created, err := svc.CreateOrg(ctx, claimant.UID,
		CreateOrgRequest{Name: "Contested", Slug: "contested"}, Context{})
	r.NoError(err, "a slug held only as an alias must be claimable")
	r.NotEqual(renamed.UID, created.UID)

	_, err = dbService.GetOrganizationByPreviousSlug(ctx, "contested")
	r.Error(err, "claiming the slug must release the alias, so it can never resolve across tenants")

	live, err := dbService.GetOrganizationBySlug(ctx, "contested")
	r.NoError(err)
	r.Equal(created.UID, live.UID)
}

// TestDeletedOrgIsUnreachableThroughItsAlias is the cross-spec boundary with
// 2026-08-08-11 at the data layer: renaming then deleting must leave BOTH slugs
// dead, with the alias refusing to resurrect the org.
func TestDeletedOrgIsUnreachableThroughItsAlias(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbService, ctx := setupAuthTestService(t)
	_, owner := seedOwnedOrg(ctx, t, dbService, "gone-old")

	newSlug := "gone-new"
	_, err := svc.UpdateOrgProfile(ctx, "gone-old", owner.UID,
		UpdateOrgProfileRequest{Slug: &newSlug}, Context{})
	r.NoError(err)

	// Control: the alias resolves while the org is alive.
	_, err = dbService.GetOrganizationByPreviousSlug(ctx, "gone-old")
	r.NoError(err)

	r.NoError(svc.DeleteOrg(ctx, "gone-new", DeleteOrgRequest{Slug: "gone-new"}))

	_, err = dbService.GetOrganizationBySlug(ctx, "gone-new")
	r.Error(err)

	_, err = dbService.GetOrganizationBySlug(ctx, "gone-old")
	r.Error(err)

	_, err = dbService.GetOrganizationByPreviousSlug(ctx, "gone-old")
	r.Error(err, "a deleted org must never resolve through its previous slug")
}

// TestRenamingBackReleasesTheAlias covers the round trip: an org that renames
// back onto a slug it still holds as an alias must not end up shadowed by its
// own alias row.
func TestRenamingBackReleasesTheAlias(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbService, ctx := setupAuthTestService(t)
	org, owner := seedOwnedOrg(ctx, t, dbService, "roundtrip")

	away := "elsewhere"
	_, err := svc.UpdateOrgProfile(ctx, "roundtrip", owner.UID,
		UpdateOrgProfileRequest{Slug: &away}, Context{})
	r.NoError(err)

	back := "roundtrip"
	_, err = svc.UpdateOrgProfile(ctx, "elsewhere", owner.UID,
		UpdateOrgProfileRequest{Slug: &back}, Context{})
	r.NoError(err)

	live, err := dbService.GetOrganizationBySlug(ctx, "roundtrip")
	r.NoError(err)
	r.Equal(org.UID, live.UID)

	_, err = dbService.GetOrganizationByPreviousSlug(ctx, "roundtrip")
	r.Error(err, "the org's own alias must be released when it renames back")

	// The slug it left behind now aliases to the org.
	aliased, err := dbService.GetOrganizationByPreviousSlug(ctx, "elsewhere")
	r.NoError(err)
	r.Equal(org.UID, aliased.UID)
}

// TestUpdateOrgProfileValidation is the table of refusals: bad slug, taken
// slug, empty/oversized name and non-http logo URLs must all be rejected and
// must leave the organization untouched.
func TestUpdateOrgProfileValidation(t *testing.T) {
	t.Parallel()

	longName := strings.Repeat("x", maxOrgNameLength+1)

	tests := []struct {
		name    string
		req     func() UpdateOrgProfileRequest
		wantErr error
	}{
		{
			name:    "slug too short",
			req:     func() UpdateOrgProfileRequest { s := "ab"; return UpdateOrgProfileRequest{Slug: &s} },
			wantErr: ErrInvalidOrgSlug,
		},
		{
			name:    "slug with uppercase",
			req:     func() UpdateOrgProfileRequest { s := "NotLower"; return UpdateOrgProfileRequest{Slug: &s} },
			wantErr: ErrInvalidOrgSlug,
		},
		{
			name:    "slug with underscore",
			req:     func() UpdateOrgProfileRequest { s := "bad_slug"; return UpdateOrgProfileRequest{Slug: &s} },
			wantErr: ErrInvalidOrgSlug,
		},
		{
			name:    "slug taken by a live org",
			req:     func() UpdateOrgProfileRequest { s := "occupied"; return UpdateOrgProfileRequest{Slug: &s} },
			wantErr: ErrOrgSlugTaken,
		},
		{
			name:    "empty name",
			req:     func() UpdateOrgProfileRequest { n := "   "; return UpdateOrgProfileRequest{Name: &n} },
			wantErr: ErrInvalidOrgName,
		},
		{
			name:    "oversized name",
			req:     func() UpdateOrgProfileRequest { return UpdateOrgProfileRequest{Name: &longName} },
			wantErr: ErrInvalidOrgName,
		},
		{
			name: "relative logo url",
			req: func() UpdateOrgProfileRequest {
				u := "/pub/org-logos/00000000-0000-0000-0000-000000000000"

				return UpdateOrgProfileRequest{LogoURL: &u, LogoURLSet: true}
			},
			wantErr: ErrInvalidLogoURL,
		},
		{
			name: "javascript logo url",
			req: func() UpdateOrgProfileRequest {
				u := "javascript:alert(1)"

				return UpdateOrgProfileRequest{LogoURL: &u, LogoURLSet: true}
			},
			wantErr: ErrInvalidLogoURL,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, dbService, ctx := setupAuthTestService(t)
			_, owner := seedOwnedOrg(ctx, t, dbService, "subject")
			seedOwnedOrg(ctx, t, dbService, "occupied")

			_, err := svc.UpdateOrgProfile(ctx, "subject", owner.UID, testCase.req(), Context{})
			r.ErrorIs(err, testCase.wantErr)

			// The refusal must be total: nothing changed.
			unchanged, err := dbService.GetOrganizationBySlug(ctx, "subject")
			r.NoError(err)
			r.Equal("subject", unchanged.Name)
			r.Nil(unchanged.LogoURL)
		})
	}
}

// TestUpdateOrgProfileLogoURL covers setting, replacing and clearing an
// external logo URL, including the "absent means untouched" half of PATCH
// semantics that a plain pointer cannot express.
func TestUpdateOrgProfileLogoURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbService, ctx := setupAuthTestService(t)
	_, owner := seedOwnedOrg(ctx, t, dbService, "logo-org")

	logo := "https://cdn.example.com/logo.png"
	resp, err := svc.UpdateOrgProfile(ctx, "logo-org", owner.UID,
		UpdateOrgProfileRequest{LogoURL: &logo, LogoURLSet: true}, Context{})
	r.NoError(err)
	r.NotNil(resp.LogoURL)
	r.Equal(logo, *resp.LogoURL)

	// A request that does not mention logoUrl leaves it alone.
	name := "Renamed"
	resp, err = svc.UpdateOrgProfile(ctx, "logo-org", owner.UID,
		UpdateOrgProfileRequest{Name: &name}, Context{})
	r.NoError(err)
	r.NotNil(resp.LogoURL)
	r.Equal(logo, *resp.LogoURL)

	// An explicit null clears it.
	resp, err = svc.UpdateOrgProfile(ctx, "logo-org", owner.UID,
		UpdateOrgProfileRequest{LogoURL: nil, LogoURLSet: true}, Context{})
	r.NoError(err)
	r.Nil(resp.LogoURL)

	stored, err := dbService.GetOrganizationBySlug(ctx, "logo-org")
	r.NoError(err)
	r.Nil(stored.LogoURL)
}

// TestUpdateOrgProfileRequestPresenceDecoding pins the JSON layer that makes
// "clear the logo" expressible at all: absent, null and "" must be three
// distinguishable inputs at decode time.
func TestUpdateOrgProfileRequestPresenceDecoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantSet     bool
		wantNilLogo bool
		wantSlug    *string
	}{
		{name: "absent logo", body: `{"name":"X"}`, wantSet: false, wantNilLogo: true},
		{name: "null logo clears", body: `{"logoUrl":null}`, wantSet: true, wantNilLogo: true},
		{name: "empty logo clears", body: `{"logoUrl":""}`, wantSet: true, wantNilLogo: false},
		{name: "logo set", body: `{"logoUrl":"https://x/y.png"}`, wantSet: true, wantNilLogo: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			var req UpdateOrgProfileRequest
			r.NoError(json.Unmarshal([]byte(testCase.body), &req))
			r.Equal(testCase.wantSet, req.LogoURLSet)

			if testCase.wantNilLogo {
				r.Nil(req.LogoURL)
			} else {
				r.NotNil(req.LogoURL)
			}
		})
	}
}

// TestUpdateOrgProfileUnknownOrg pins that renaming a slug that does not
// resolve is a not-found rather than a silent no-op.
func TestUpdateOrgProfileUnknownOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestService(t)

	name := "Ghost"
	_, err := svc.UpdateOrgProfile(ctx, "never-existed", "someone",
		UpdateOrgProfileRequest{Name: &name}, Context{})
	r.ErrorIs(err, ErrOrganizationNotFound)
}

// TestUpdateOrgProfileSameSlugIsNotARename pins that submitting the current
// slug unchanged does not manufacture an alias on the org's own live slug —
// which would leave an alias shadowing a live organization.
func TestUpdateOrgProfileSameSlugIsNotARename(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbService, ctx := setupAuthTestService(t)
	_, owner := seedOwnedOrg(ctx, t, dbService, "stable")

	same := "stable"
	resp, err := svc.UpdateOrgProfile(ctx, "stable", owner.UID,
		UpdateOrgProfileRequest{Slug: &same}, Context{})
	r.NoError(err)
	r.Empty(resp.PreviousSlug)
	r.Empty(resp.AccessToken, "no rename means no re-minted session")

	_, err = dbService.GetOrganizationByPreviousSlug(ctx, "stable")
	r.Error(err)
}

// TestReclaimedSlugNeverResurrectsTheAlias closes the cross-tenant hole the
// alias release exists to prevent. Org A renames away from a slug; org B claims
// it (through the DB layer directly, the way every connector's zero-member
// bootstrap creates orgs, bypassing CreateOrg); B is later deleted. The slug
// must simply be free — it must NOT fall back to redirecting at A, months after
// somebody else owned it.
func TestReclaimedSlugNeverResurrectsTheAlias(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbService, ctx := setupAuthTestService(t)
	_, owner := seedOwnedOrg(ctx, t, dbService, "handover")

	away := "handover-new"
	_, err := svc.UpdateOrgProfile(ctx, "handover", owner.UID,
		UpdateOrgProfileRequest{Slug: &away}, Context{})
	r.NoError(err)

	// Control: the alias resolves right up until somebody claims the slug.
	_, err = dbService.GetOrganizationByPreviousSlug(ctx, "handover")
	r.NoError(err)

	// A connector-style creation path: straight to the DB layer, no CreateOrg.
	claimant := models.NewOrganization("handover", "Claimant")
	r.NoError(dbService.CreateOrganization(ctx, claimant))

	_, err = dbService.GetOrganizationByPreviousSlug(ctx, "handover")
	r.Error(err, "creating an org on the slug must release the alias, whatever the code path")

	r.NoError(dbService.DeleteOrganization(ctx, claimant.UID))

	_, err = dbService.GetOrganizationByPreviousSlug(ctx, "handover")
	r.Error(err, "a released alias must stay released once the claiming org is gone")

	_, err = dbService.GetOrganizationBySlug(ctx, "handover")
	r.Error(err, "the slug is simply free again")
}
