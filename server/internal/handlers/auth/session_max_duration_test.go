package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestResolveSessionMaxDuration covers the resolution order from spec B.2:
// org parameter beats system parameter (cfg, already overlaid at startup)
// beats 0 (unlimited, today's behavior).
func TestResolveSessionMaxDuration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("session-cap-resolve", "Session Cap Resolve")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Neither org nor system value set: unlimited (0).
	r.Equal(time.Duration(0), svc.resolveSessionMaxDuration(ctx, org.UID))

	// System-wide default set (mirrors the systemconfig startup overlay of
	// auth.session_max_duration onto the LIVE fullCfg.Auth.SessionMaxDuration,
	// which is what resolveSessionMaxDuration reads), no org override yet:
	// system value wins.
	svc.fullCfg.Auth.SessionMaxDuration = 6 * time.Hour
	r.Equal(6*time.Hour, svc.resolveSessionMaxDuration(ctx, org.UID))

	// Org override set: org value wins over the system default.
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, string(systemconfig.KeySessionMaxDuration), 3600, false))
	r.Equal(time.Hour, svc.resolveSessionMaxDuration(ctx, org.UID))

	// An org with no override, and no orgUID at all, both still fall back
	// to the system value.
	otherOrg := models.NewOrganization("session-cap-other", "Session Cap Other")
	r.NoError(dbSvc.CreateOrganization(ctx, otherOrg))
	r.Equal(6*time.Hour, svc.resolveSessionMaxDuration(ctx, otherOrg.UID))
	r.Equal(6*time.Hour, svc.resolveSessionMaxDuration(ctx, ""))
}

// TestRefreshTokenExpiryCapsSlidingWindow covers spec B.3's hard-cap
// semantics: expires_at = min(now + RefreshTokenExpiry, login_time +
// session_max_duration), measured from login rather than from "now" so
// sliding can never outrun the cap. Each subtest gets its own Service (via
// setupAuthTestService) rather than sharing one and mutating cfg in place —
// Service embeds a sync.RWMutex, so copying/sharing one across parallel
// subtests would either race or trip go vet's copylocks check.
func TestRefreshTokenExpiryCapsSlidingWindow(t *testing.T) {
	t.Parallel()
	loginTime := time.Now().Add(-time.Hour) // logged in an hour ago

	t.Run("no cap configured: sliding window only", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		svc, _, ctx := setupAuthTestService(t)

		now := time.Now()
		got := svc.refreshTokenExpiry(ctx, "", loginTime, now)
		r.WithinDuration(now.Add(svc.cfg.RefreshTokenExpiry), got, time.Second)
	})

	t.Run("cap shorter than the sliding window: hard cap wins", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		svc, _, ctx := setupAuthTestService(t)
		svc.fullCfg.Auth.SessionMaxDuration = 2 * time.Hour // session cap: 2h from login

		now := time.Now()
		got := svc.refreshTokenExpiry(ctx, "", loginTime, now)
		r.WithinDuration(loginTime.Add(2*time.Hour), got, time.Second)
		r.True(got.Before(now.Add(svc.cfg.RefreshTokenExpiry)),
			"the hard cap must be tighter than the 7-day sliding window here")
	})

	t.Run("cap far in the future: sliding window still wins", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		svc, _, ctx := setupAuthTestService(t)
		svc.fullCfg.Auth.SessionMaxDuration = 365 * 24 * time.Hour // a year — way past the 7-day window

		now := time.Now()
		got := svc.refreshTokenExpiry(ctx, "", loginTime, now)
		r.WithinDuration(now.Add(svc.cfg.RefreshTokenExpiry), got, time.Second)
	})
}

// TestLoginWithOrgSessionCapNeverExceedsIt is the end-to-end acceptance test
// for B.3: an org with a short session_max_duration override mints a refresh
// row that never extends (even after sliding refreshes) past
// login_time + session_max_duration.
func TestLoginWithOrgSessionCapNeverExceedsIt(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("capped-org", "Capped Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// A 2-hour hard cap, far shorter than the 7-day sliding window the test
	// service is configured with.
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, string(systemconfig.KeySessionMaxDuration), 7200, false))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("capped@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	loginTime := time.Now()
	loginResp, err := svc.Login(ctx, "capped-org", "capped@example.com", "testpass1234", Context{})
	r.NoError(err)

	refreshRow, err := dbSvc.GetUserTokenByToken(ctx, loginResp.RefreshToken)
	r.NoError(err)
	r.WithinDuration(loginTime.Add(2*time.Hour), *refreshRow.ExpiresAt, 5*time.Second,
		"mint must cap expires_at at login_time + session_max_duration, not login_time + 7 days")

	// Force the row's last_active_at far enough in the past that the
	// granularity guard (session_test.go's TestRefreshSlidingExpiry pattern)
	// allows a write on the next refresh, then refresh well past the cap.
	longAgo := time.Now().Add(-2 * time.Hour)
	r.NoError(dbSvc.UpdateUserToken(ctx, refreshRow.UID, models.UserTokenUpdate{LastActiveAt: &longAgo}))

	_, err = svc.Refresh(ctx, loginResp.RefreshToken)
	r.NoError(err)

	afterRefresh, err := dbSvc.GetUserToken(ctx, refreshRow.UID)
	r.NoError(err)
	r.WithinDuration(loginTime.Add(2*time.Hour), *afterRefresh.ExpiresAt, 5*time.Second,
		"sliding must never extend expires_at past login_time + session_max_duration")
}

// TestOrgWithoutSessionCapFollowsSystemParameter verifies the fallback leg
// end-to-end: an org with no override inherits the system-wide value.
func TestOrgWithoutSessionCapFollowsSystemParameter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	svc.fullCfg.Auth.SessionMaxDuration = 3 * time.Hour // system default, no org override

	org := models.NewOrganization("uncapped-org", "Uncapped Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	user := models.NewUser("uncapped@example.com")
	user.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	loginTime := time.Now()
	loginResp, err := svc.Login(ctx, "uncapped-org", "uncapped@example.com", "testpass1234", Context{})
	r.NoError(err)

	refreshRow, err := dbSvc.GetUserTokenByToken(ctx, loginResp.RefreshToken)
	r.NoError(err)
	r.WithinDuration(loginTime.Add(3*time.Hour), *refreshRow.ExpiresAt, 5*time.Second,
		"an org with no override must follow the system-wide session_max_duration")
}

// TestOrgSettingsSessionMaxDurationRoundTrip is the API-surface leg of
// B.4: the org override round-trips through GetOrgSettings/UpdateOrgSettings
// (the same service methods the org settings PATCH the dash0 UI calls), and
// a non-positive value clears it back to "inherit".
func TestOrgSettingsSessionMaxDurationRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("settings-cap-org", "Settings Cap Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// No override set yet: nil, not zero — the settings page must be able
	// to tell "not set" apart from "explicitly set to 0".
	settings, err := svc.GetOrgSettings(ctx, "settings-cap-org")
	r.NoError(err)
	r.Nil(settings.SessionMaxDurationSeconds)

	// Setting the override round-trips through Get.
	seconds := 14400 // 4 hours
	settings, err = svc.UpdateOrgSettings(ctx, "settings-cap-org", UpdateOrgSettingsRequest{
		SessionMaxDurationSeconds: &seconds,
	})
	r.NoError(err)
	r.NotNil(settings.SessionMaxDurationSeconds)
	r.Equal(seconds, *settings.SessionMaxDurationSeconds)

	settings, err = svc.GetOrgSettings(ctx, "settings-cap-org")
	r.NoError(err)
	r.NotNil(settings.SessionMaxDurationSeconds)
	r.Equal(seconds, *settings.SessionMaxDurationSeconds)

	// A request that omits the field entirely must not touch the override.
	pattern := ""
	settings, err = svc.UpdateOrgSettings(ctx, "settings-cap-org", UpdateOrgSettingsRequest{
		RegistrationEmailPattern: &pattern,
	})
	r.NoError(err)
	r.NotNil(settings.SessionMaxDurationSeconds)
	r.Equal(seconds, *settings.SessionMaxDurationSeconds)

	// A non-positive value clears the override back to "inherit".
	cleared := 0
	settings, err = svc.UpdateOrgSettings(ctx, "settings-cap-org", UpdateOrgSettingsRequest{
		SessionMaxDurationSeconds: &cleared,
	})
	r.NoError(err)
	r.Nil(settings.SessionMaxDurationSeconds)
}
