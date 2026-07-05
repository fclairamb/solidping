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

// TestLogoutOtherSessions verifies "sign out other sessions" deletes every
// refresh-type row except the caller's own, reports the correct count, and
// leaves the caller's own session fully usable afterwards.
func TestLogoutOtherSessions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("logout-others-org", "Logout Others Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("logout-others@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	// Three independent "sessions" (separate logins, e.g. three devices).
	current, err := svc.Login(ctx, "logout-others-org", "logout-others@example.com", "testpass1234", Context{})
	r.NoError(err)
	other1, err := svc.Login(ctx, "logout-others-org", "logout-others@example.com", "testpass1234", Context{})
	r.NoError(err)
	other2, err := svc.Login(ctx, "logout-others-org", "logout-others@example.com", "testpass1234", Context{})
	r.NoError(err)

	currentClaims, err := svc.ValidateToken(ctx, current.AccessToken)
	r.NoError(err)
	r.NotEmpty(currentClaims.RefreshUID)

	resp, err := svc.LogoutOtherSessions(ctx, user.UID, currentClaims.RefreshUID)
	r.NoError(err)
	r.True(resp.Success)
	r.Equal(2, resp.TokensDeleted, "must delete exactly the two other sessions")

	// The caller's own session must survive and keep refreshing.
	_, err = svc.Refresh(ctx, current.RefreshToken)
	r.NoError(err, "the caller's own session must not be touched by sign-out-others")

	// Both other sessions must be gone.
	_, err = svc.Refresh(ctx, other1.RefreshToken)
	r.ErrorIs(err, ErrInvalidToken)
	_, err = svc.Refresh(ctx, other2.RefreshToken)
	r.ErrorIs(err, ErrInvalidToken)

	remaining, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
	r.NoError(err)
	r.Len(remaining, 1)
	r.Equal(currentClaims.RefreshUID, remaining[0].UID)
}

// TestGetUserTokensTypeFilterAndIsCurrent covers acceptance criteria 4/5/9:
// the `type=refresh` filter returns only session rows (never PATs or
// oauth_refresh grants), each carries createdWith metadata, and isCurrent
// is flagged on exactly the caller's own row.
func TestGetUserTokensTypeFilterAndIsCurrent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("filter-org", "Filter Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("filter@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	loginCtx := Context{UserAgent: "test-agent/1.0", RemoteAddr: "203.0.113.5"}
	current, err := svc.Login(ctx, "filter-org", "filter@example.com", "testpass1234", loginCtx)
	r.NoError(err)
	// Second session, exercised only to prove the listing returns >1 row
	// and that isCurrent distinguishes between them.
	_, err = svc.Login(ctx, "filter-org", "filter@example.com", "testpass1234", Context{})
	r.NoError(err)

	_, err = svc.CreatePAT(ctx, "filter-org", user.UID, CreateTokenRequest{Name: "a-pat"})
	r.NoError(err)

	currentClaims, err := svc.ValidateToken(ctx, current.AccessToken)
	r.NoError(err)

	resp, err := svc.GetUserTokens(ctx, "filter-org", user.UID, "refresh", currentClaims.RefreshUID)
	r.NoError(err)
	r.Len(resp.Data, 2, "type=refresh must return only the two sessions, never the PAT")

	var currentCount int

	for _, tok := range resp.Data {
		r.Equal("refresh", tok.Type)

		if tok.UID == currentClaims.RefreshUID {
			currentCount++
			r.True(tok.IsCurrent, "the caller's own session row must be flagged isCurrent")
			r.NotNil(tok.CreatedWith)
			r.Equal("password", tok.CreatedWith.Method)
			r.Equal("test-agent/1.0", tok.CreatedWith.UserAgent)
			r.Equal("203.0.113.5", tok.CreatedWith.RemoteAddr)
			r.NotNil(tok.LastActiveAt)
		} else {
			r.False(tok.IsCurrent, "a non-caller session must not be flagged isCurrent")
		}
	}

	r.Equal(1, currentCount, "exactly one row must be isCurrent")

	// type=pat must never include a refresh (session) row.
	patResp, err := svc.GetUserTokens(ctx, "filter-org", user.UID, "pat", currentClaims.RefreshUID)
	r.NoError(err)
	r.Len(patResp.Data, 1)
	r.Equal("pat", patResp.Data[0].Type)
	r.False(patResp.Data[0].IsCurrent, "a PAT must never be flagged isCurrent")
}

// TestLogoutOtherSessionsEmptyRefreshUIDDeletesAll documents why the
// handler must reject signOutOthers when claims.RefreshUID is empty (e.g. a
// PAT hitting /logout) BEFORE calling into the service: LogoutOtherSessions
// itself has no row to match uid == "" against, so an empty
// currentRefreshUID spares nothing and deletes every session. The handler
// validation (see Handler.Logout) is the only thing standing between this
// and an accidental full sign-out.
func TestLogoutOtherSessionsEmptyRefreshUIDDeletesAll(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("empty-refreshuid-org", "Empty RefreshUID Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("empty-refreshuid@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	_, err = svc.Login(ctx, "empty-refreshuid-org", "empty-refreshuid@example.com", "testpass1234", Context{})
	r.NoError(err)
	_, err = svc.Login(ctx, "empty-refreshuid-org", "empty-refreshuid@example.com", "testpass1234", Context{})
	r.NoError(err)

	resp, err := svc.LogoutOtherSessions(ctx, user.UID, "")
	r.NoError(err)
	r.Equal(2, resp.TokensDeleted, "an empty currentRefreshUID spares nothing — the handler must reject this case before calling in")
}
