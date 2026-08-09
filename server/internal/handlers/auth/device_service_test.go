package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// deviceTestUser wires an org + user + membership and returns claims for that
// pair, so each test can act as a real member of a real org.
func deviceTestUser(
	ctx context.Context, t *testing.T, dbSvc db.Service, orgSlug, email string,
) (*models.Organization, *models.User, *Claims) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization(orgSlug, orgSlug)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser(email)
	r.NoError(dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

	return org, user, &Claims{UserUID: user.UID, OrgSlug: org.Slug, Role: string(models.MemberRoleAdmin)}
}

// addDeviceTestOrg joins an existing user to a second org, for the multi-org
// consent cases.
func addDeviceTestOrg(
	ctx context.Context, t *testing.T, dbSvc db.Service, user *models.User, orgSlug string,
) *models.Organization {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization(orgSlug, orgSlug)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

	return org
}

// clearDevicePollStamp erases the poll timestamp so a test can poll again
// immediately without tripping the slow_down interval. The interval itself is
// covered explicitly by TestDeviceAuthSlowDown.
func clearDevicePollStamp(ctx context.Context, t *testing.T, dbSvc db.Service, deviceCode string) {
	t.Helper()

	req, err := dbSvc.GetDeviceAuthRequestByDeviceCode(ctx, deviceCode)
	require.NoError(t, err)
	require.NoError(t, dbSvc.TouchDeviceAuthPoll(ctx, req.UID, time.Now().Add(-time.Hour)))
}

func TestDeviceAuthorizationStart(t *testing.T) {
	t.Parallel()

	svc, _, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")

	tests := []struct {
		name           string
		clientName     string
		wantClientName string
		wantErr        error
	}{
		{name: "named client", clientName: "sp CLI on laptop", wantClientName: "sp CLI on laptop"},
		{name: "unnamed client falls back to a label", clientName: "", wantClientName: deviceDefaultClientName},
		{
			name: "oversized client name is rejected", clientName: string(make([]byte, 400)),
			wantErr: ErrDeviceClientNameTooLong,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			resp, err := svc.StartDeviceAuthorization(ctx, tc.clientName)
			if tc.wantErr != nil {
				r.ErrorIs(err, tc.wantErr)

				return
			}

			r.NoError(err)
			r.Len(resp.DeviceCode, deviceCodeBytes*2, "device code is 32 random bytes, hex encoded")
			r.Len(resp.UserCode, deviceUserCodeLength+1, "user code is displayed as XXXX-XXXX")
			r.Equal("https://ping.example.com/dash0/device", resp.VerificationURI)
			r.Equal(resp.VerificationURI+"?user_code="+resp.UserCode, resp.VerificationURIComplete)
			r.Equal(DevicePollIntervalSeconds, resp.Interval)
			r.Positive(resp.ExpiresIn)

			// The consent lookup finds it under any spelling of the code.
			info, err := svc.GetDeviceConsent(ctx, resp.UserCode)
			r.NoError(err)
			r.Equal(tc.wantClientName, info.ClientName)
			r.Equal(models.DeviceAuthStatusPending, info.Status)
		})
	}
}

// TestDeviceAuthLifecycle is the core guard: pending → approve → the PAT is
// delivered EXACTLY once, and every poll after that looks like an expired
// device code.
func TestDeviceAuthLifecycle(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")
	r := require.New(t)

	org, user, claims := deviceTestUser(ctx, t, dbSvc, "device-life", "device-life@example.com")

	start, err := svc.StartDeviceAuthorization(ctx, "sp CLI on laptop")
	r.NoError(err)

	// Poll before approval → authorization_pending, and no token leaks.
	token, err := svc.PollDeviceToken(ctx, start.DeviceCode)
	r.ErrorIs(err, ErrDeviceAuthorizationPending)
	r.Empty(token)

	// Approve, binding to the session's org.
	clearDevicePollStamp(ctx, t, dbSvc, start.DeviceCode)
	r.NoError(svc.RespondToDeviceConsent(ctx, claims, start.UserCode, org.Slug, true))

	// First poll delivers a usable PAT scoped to the approving user + org.
	token, err = svc.PollDeviceToken(ctx, start.DeviceCode)
	r.NoError(err)
	r.NotEmpty(token)

	patClaims, err := svc.ValidatePATToken(ctx, token)
	r.NoError(err)
	r.Equal(user.UID, patClaims.UserUID)
	r.Equal(org.Slug, patClaims.OrgSlug)

	stored, err := dbSvc.GetUserTokenByToken(ctx, token)
	r.NoError(err)
	r.Equal(models.TokenTypePAT, stored.Type)
	r.Equal("sp CLI on laptop", stored.Properties["name"], "the PAT is named after the requesting client")
	r.NotNil(stored.ExpiresAt)
	r.WithinDuration(time.Now().Add(DevicePATExpiry), *stored.ExpiresAt, time.Minute)

	// Second poll must NOT re-deliver — the row is gone, so it is
	// indistinguishable from an unknown device code.
	again, err := svc.PollDeviceToken(ctx, start.DeviceCode)
	r.ErrorIs(err, ErrDeviceExpiredToken)
	r.Empty(again)
}

func TestDeviceAuthDeny(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")
	r := require.New(t)

	org, user, claims := deviceTestUser(ctx, t, dbSvc, "device-deny", "device-deny@example.com")

	start, err := svc.StartDeviceAuthorization(ctx, "sp CLI")
	r.NoError(err)

	r.NoError(svc.RespondToDeviceConsent(ctx, claims, start.UserCode, org.Slug, false))

	token, err := svc.PollDeviceToken(ctx, start.DeviceCode)
	r.ErrorIs(err, ErrDeviceAccessDenied)
	r.Empty(token)

	// A denial mints nothing.
	tokens, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypePAT)
	r.NoError(err)
	r.Empty(tokens, "denying must never mint a PAT")

	// Deciding twice is a conflict, not a silent overwrite.
	err = svc.RespondToDeviceConsent(ctx, claims, start.UserCode, org.Slug, true)
	r.Error(err, "a resolved request cannot be decided again")
}

func TestDeviceAuthExpiry(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")
	r := require.New(t)

	org, _, claims := deviceTestUser(ctx, t, dbSvc, "device-exp", "device-exp@example.com")

	// A request that expired a minute ago is invisible everywhere.
	expired := models.NewDeviceAuthRequest("sp CLI", "expired-device-code", "PQRS2345", time.Now().Add(-time.Minute))
	r.NoError(dbSvc.CreateDeviceAuthRequest(ctx, expired))

	_, err := svc.GetDeviceConsent(ctx, "PQRS-2345")
	r.ErrorIs(err, ErrDeviceAuthNotFound)

	err = svc.RespondToDeviceConsent(ctx, claims, "PQRS-2345", org.Slug, true)
	r.ErrorIs(err, ErrDeviceAuthNotFound)

	_, err = svc.PollDeviceToken(ctx, "expired-device-code")
	r.ErrorIs(err, ErrDeviceExpiredToken)
}

func TestDeviceAuthUnknownCodes(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")
	outer := require.New(t)

	org, _, claims := deviceTestUser(ctx, t, dbSvc, "device-unknown", "device-unknown@example.com")

	tests := []struct {
		name     string
		userCode string
	}{
		{name: "never issued", userCode: "ZZZZ-9999"},
		{name: "empty", userCode: ""},
		{name: "only separators", userCode: "----"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			_, err := svc.GetDeviceConsent(ctx, tc.userCode)
			r.ErrorIs(err, ErrDeviceAuthNotFound)

			err = svc.RespondToDeviceConsent(ctx, claims, tc.userCode, org.Slug, true)
			r.ErrorIs(err, ErrDeviceAuthNotFound)
		})
	}

	_, err := svc.PollDeviceToken(ctx, "not-a-real-device-code")
	outer.ErrorIs(err, ErrDeviceExpiredToken)
}

// TestDeviceAuthSlowDown pins the interval enforcement: a second poll inside
// the advertised interval is refused with slow_down and does not consume the
// request.
func TestDeviceAuthSlowDown(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")
	r := require.New(t)

	org, _, claims := deviceTestUser(ctx, t, dbSvc, "device-slow", "device-slow@example.com")

	start, err := svc.StartDeviceAuthorization(ctx, "sp CLI")
	r.NoError(err)

	_, err = svc.PollDeviceToken(ctx, start.DeviceCode)
	r.ErrorIs(err, ErrDeviceAuthorizationPending)

	// Immediately again: too fast.
	_, err = svc.PollDeviceToken(ctx, start.DeviceCode)
	r.ErrorIs(err, ErrDeviceSlowDown)

	// slow_down must not eat the approval: once the client backs off, the flow
	// still completes.
	r.NoError(svc.RespondToDeviceConsent(ctx, claims, start.UserCode, org.Slug, true))

	_, err = svc.PollDeviceToken(ctx, start.DeviceCode)
	r.ErrorIs(err, ErrDeviceSlowDown, "still inside the interval")

	clearDevicePollStamp(ctx, t, dbSvc, start.DeviceCode)

	token, err := svc.PollDeviceToken(ctx, start.DeviceCode)
	r.NoError(err)
	r.NotEmpty(token)
}

// TestDeviceAuthOrgBinding covers the multi-org decision (spec resolved
// question 1): the PAT is scoped to the org SELECTED at approval, not to the
// approving session's org.
func TestDeviceAuthOrgBinding(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://ping.example.com")

	sessionOrg, user, claims := deviceTestUser(ctx, t, dbSvc, "device-org-a", "device-org@example.com")
	otherOrg := addDeviceTestOrg(ctx, t, dbSvc, user, "device-org-b")

	// Nothing to do with the approver: a stranger's org they are not in.
	strangerOrg := models.NewOrganization("device-org-c", "device-org-c")
	require.NoError(t, dbSvc.CreateOrganization(ctx, strangerOrg))

	tests := []struct {
		name     string
		orgInput string
		wantOrg  string
		wantErr  bool
	}{
		{name: "explicit second org wins over the session org", orgInput: otherOrg.Slug, wantOrg: otherOrg.Slug},
		{name: "empty selection falls back to the session org", orgInput: "", wantOrg: sessionOrg.Slug},
		{name: "an org the user does not belong to is refused", orgInput: strangerOrg.Slug, wantErr: true},
		{name: "an unknown org is refused", orgInput: "no-such-org", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			start, err := svc.StartDeviceAuthorization(ctx, "sp CLI")
			r.NoError(err)

			err = svc.RespondToDeviceConsent(ctx, claims, start.UserCode, tc.orgInput, true)
			if tc.wantErr {
				r.Error(err)

				// A refused approval leaves the request pending and mints nothing.
				_, pollErr := svc.PollDeviceToken(ctx, start.DeviceCode)
				r.ErrorIs(pollErr, ErrDeviceAuthorizationPending)

				return
			}

			r.NoError(err)

			// The stored request records the SELECTED org, not the session's.
			stored, err := dbSvc.GetDeviceAuthRequestByDeviceCode(ctx, start.DeviceCode)
			r.NoError(err)
			r.NotNil(stored.OrganizationUID)

			clearDevicePollStamp(ctx, t, dbSvc, start.DeviceCode)

			token, err := svc.PollDeviceToken(ctx, start.DeviceCode)
			r.NoError(err)

			patClaims, err := svc.ValidatePATToken(ctx, token)
			r.NoError(err)
			r.Equal(tc.wantOrg, patClaims.OrgSlug, "the PAT is scoped to the org selected at approval")
		})
	}
}

func TestDeviceUserCodeFormatting(t *testing.T) {
	t.Parallel()

	outer := require.New(t)

	tests := []struct {
		name      string
		input     string
		normalize string
		display   string
	}{
		{name: "canonical", input: "WDJP4KXR", normalize: "WDJP4KXR", display: "WDJP-4KXR"},
		{name: "dashed lowercase", input: "wdjp-4kxr", normalize: "WDJP4KXR"},
		{name: "spaced", input: " WDJP 4KXR ", normalize: "WDJP4KXR"},
		{name: "ambiguous chars are not in the alphabet", input: "O0I1L", normalize: ""},
		{name: "empty", input: "", normalize: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			r.Equal(tc.normalize, NormalizeDeviceUserCode(tc.input))

			if tc.display != "" {
				r.Equal(tc.display, FormatDeviceUserCode(tc.input))
			}
		})
	}

	// A code of the wrong length is passed through untouched rather than
	// mangled into a misleading display form.
	outer.Equal("SHORT", FormatDeviceUserCode("SHORT"))

	// Generated codes only ever use the ambiguity-free alphabet.
	for range 50 {
		code, err := generateDeviceUserCode()
		outer.NoError(err)
		outer.Len(code, deviceUserCodeLength)

		for _, char := range code {
			outer.Contains(deviceUserCodeAlphabet, string(char))
		}
	}
}
