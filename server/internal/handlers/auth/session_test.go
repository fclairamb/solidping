package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestRefreshUID verifies claims minted by login and by refresh both carry
// the issuing refresh-token row's UID, and that a PAT's validated claims do
// not (acceptance criterion 4).
func TestRefreshUID(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("refreshuid-org", "Refresh UID Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("refreshuid@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	loginResp, err := svc.Login(ctx, "refreshuid-org", "refreshuid@example.com", "testpass1234", Context{})
	r.NoError(err)
	r.NotEmpty(loginResp.RefreshToken)

	loginClaims, err := svc.ValidateToken(ctx, loginResp.AccessToken)
	r.NoError(err)
	r.NotEmpty(loginClaims.RefreshUID, "access token minted at login must carry refreshUid")

	// The refreshUid must match the row that was actually created for this login.
	refreshRow, err := dbSvc.GetUserTokenByToken(ctx, loginResp.RefreshToken)
	r.NoError(err)
	r.Equal(refreshRow.UID, loginClaims.RefreshUID)

	// Refreshing must mint a new access token bound to the SAME row (no rotation).
	refreshResp, err := svc.Refresh(ctx, loginResp.RefreshToken)
	r.NoError(err)

	refreshClaims, err := svc.ValidateToken(ctx, refreshResp.AccessToken)
	r.NoError(err)
	r.Equal(refreshRow.UID, refreshClaims.RefreshUID, "refresh must carry the same refreshUid as login")

	// A PAT's validated claims must NOT carry a refreshUid.
	createResp, err := svc.CreatePAT(ctx, "refreshuid-org", user.UID, CreateTokenRequest{Name: "test-pat"})
	r.NoError(err)

	patClaims, err := svc.ValidateToken(ctx, createResp.Token)
	r.NoError(err)
	r.Empty(patClaims.RefreshUID, "PAT-validated claims must not carry a refreshUid")
}

// TestRefreshSlidingExpiry verifies a successful refresh extends the
// session row's expires_at to now + refresh_token_expiry, and that the
// write is gated by the same hourly granularity used for last_active_at
// elsewhere (no write amplification on rapid-fire refreshes).
func TestRefreshSlidingExpiry(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("sliding-org", "Sliding Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("sliding@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	loginResp, err := svc.Login(ctx, "sliding-org", "sliding@example.com", "testpass1234", Context{})
	r.NoError(err)

	refreshRow, err := dbSvc.GetUserTokenByToken(ctx, loginResp.RefreshToken)
	r.NoError(err)
	originalExpiresAt := *refreshRow.ExpiresAt

	// Force the row's last_active_at far enough in the past that the
	// granularity guard allows a write on the next refresh.
	longAgo := time.Now().Add(-2 * time.Hour)
	r.NoError(dbSvc.UpdateUserToken(ctx, refreshRow.UID, models.UserTokenUpdate{LastActiveAt: &longAgo}))

	_, err = svc.Refresh(ctx, loginResp.RefreshToken)
	r.NoError(err)

	afterFirstRefresh, err := dbSvc.GetUserToken(ctx, refreshRow.UID)
	r.NoError(err)
	r.True(afterFirstRefresh.ExpiresAt.After(originalExpiresAt),
		"a refresh past the granularity window must extend expires_at")
	r.WithinDuration(time.Now().Add(svc.cfg.RefreshTokenExpiry), *afterFirstRefresh.ExpiresAt, 5*time.Second)

	extendedExpiresAt := *afterFirstRefresh.ExpiresAt

	// A second refresh immediately after (well within the hourly
	// granularity window) must NOT write expires_at/last_active_at again.
	_, err = svc.Refresh(ctx, loginResp.RefreshToken)
	r.NoError(err)

	afterSecondRefresh, err := dbSvc.GetUserToken(ctx, refreshRow.UID)
	r.NoError(err)
	r.Equal(extendedExpiresAt, *afterSecondRefresh.ExpiresAt,
		"a refresh within the granularity window must not write expires_at again")
}

// TestRefreshRevokedTokenFailsImmediately verifies revocation is immediate:
// deleting a session row makes the very next refresh attempt fail.
func TestRefreshRevokedTokenFailsImmediately(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("revoke-org", "Revoke Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("revoke@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	loginResp, err := svc.Login(ctx, "revoke-org", "revoke@example.com", "testpass1234", Context{})
	r.NoError(err)

	refreshRow, err := dbSvc.GetUserTokenByToken(ctx, loginResp.RefreshToken)
	r.NoError(err)

	r.NoError(svc.RevokeToken(ctx, user.UID, refreshRow.UID))

	_, err = svc.Refresh(ctx, loginResp.RefreshToken)
	r.ErrorIs(err, ErrInvalidToken, "refreshing a revoked session must fail immediately")
}

// TestSwitchOrgThenRefreshKeepsSwitchedOrg is the org-stability acceptance
// criterion: switch-org mints a new refresh token scoped to the target org,
// and a subsequent refresh (using that new token, as the client is expected
// to persist it) must reproduce the switched-to org, never the login-time
// org.
func TestSwitchOrgThenRefreshKeepsSwitchedOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	orgA := models.NewOrganization("stability-a", "Stability A")
	orgB := models.NewOrganization("stability-b", "Stability B")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("stability@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(orgA.UID, user.UID, models.MemberRoleAdmin)))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(orgB.UID, user.UID, models.MemberRoleUser)))

	loginResp, err := svc.Login(ctx, "stability-a", "stability@example.com", "testpass1234", Context{})
	r.NoError(err)
	r.Equal("stability-a", loginResp.Organization.Slug)

	switchResp, err := svc.SwitchOrg(ctx, user.UID, "stability-b", Context{})
	r.NoError(err)
	r.Equal("stability-b", switchResp.Organization.Slug)
	r.NotEmpty(switchResp.RefreshToken)
	r.NotEqual(loginResp.RefreshToken, switchResp.RefreshToken,
		"switch-org must mint a NEW refresh token rather than mutate the login-time one")

	// The client is expected to persist the NEW refresh token from the
	// switch-org response. Refreshing with it must keep org B.
	refreshResp, err := svc.Refresh(ctx, switchResp.RefreshToken)
	r.NoError(err)
	r.Equal("stability-b", refreshResp.Organization.Slug,
		"a refresh after switch-org must reproduce the switched-to org, not flip back")

	// The OLD (login-time) refresh token must still independently work and
	// still resolve org A — it was never touched by the switch.
	oldRefreshResp, err := svc.Refresh(ctx, loginResp.RefreshToken)
	r.NoError(err)
	r.Equal("stability-a", oldRefreshResp.Organization.Slug)

	// refreshUid must be internally consistent: the claims minted by the
	// switch-org response carry the NEW row's uid, not the old one.
	switchClaims, err := svc.ValidateToken(ctx, switchResp.AccessToken)
	r.NoError(err)
	newRow, err := dbSvc.GetUserTokenByToken(ctx, switchResp.RefreshToken)
	r.NoError(err)
	r.Equal(newRow.UID, switchClaims.RefreshUID)
}

// TestSessionCapPrunesLeastRecentlyActive is acceptance criterion 6: an
// 11th concurrent login must prune the least-recently-active session,
// keeping the 10 newest.
func TestSessionCapPrunesLeastRecentlyActive(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("cap-org", "Cap Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("cap@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	var refreshTokens []string

	for i := range maxActiveSessions {
		resp, loginErr := svc.Login(ctx, "cap-org", "cap@example.com", "testpass1234", Context{})
		r.NoError(loginErr)
		refreshTokens = append(refreshTokens, resp.RefreshToken)

		// Space out LastActiveAt so ordering is deterministic: give each
		// row a distinct, strictly increasing activity timestamp.
		row, getErr := dbSvc.GetUserTokenByToken(ctx, resp.RefreshToken)
		r.NoError(getErr)
		activeAt := time.Now().Add(time.Duration(i) * time.Minute)
		r.NoError(dbSvc.UpdateUserToken(ctx, row.UID, models.UserTokenUpdate{LastActiveAt: &activeAt}))
	}

	sessions, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
	r.NoError(err)
	r.Len(sessions, maxActiveSessions, "exactly at the cap: nothing pruned yet")

	// The 11th login must trigger pruning of the single oldest session
	// (refreshTokens[0], which has the earliest LastActiveAt).
	resp11, err := svc.Login(ctx, "cap-org", "cap@example.com", "testpass1234", Context{})
	r.NoError(err)
	refreshTokens = append(refreshTokens, resp11.RefreshToken)

	sessions, err = dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
	r.NoError(err)
	r.Len(sessions, maxActiveSessions, "an 11th login must prune down to the cap, not grow past it")

	// The oldest (first-created, first-active) session must be gone…
	_, err = dbSvc.GetUserTokenByToken(ctx, refreshTokens[0])
	r.Error(err, "the least-recently-active session must have been pruned")

	// …and refreshing with it must now fail.
	_, err = svc.Refresh(ctx, refreshTokens[0])
	r.ErrorIs(err, ErrInvalidToken)

	// …while the 10 newest (including the 11th login) all survive.
	for i := 1; i <= maxActiveSessions; i++ {
		row, getErr := dbSvc.GetUserTokenByToken(ctx, refreshTokens[i])
		r.NoError(getErr, "session %d should have survived the cap", i)
		r.Nil(row.DeletedAt)
	}
}
