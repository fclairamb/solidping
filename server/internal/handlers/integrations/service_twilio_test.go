package integrations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
)

// This file lives in the internal `integrations` package (not
// `integrations_test`) specifically so it can reach the unexported
// verifyTwilioCredentials seam — the whole point of that seam is that no test
// anywhere may require network access to exercise the Twilio write path.

// validTwilioSID is a syntactically valid (but fake) Twilio Account SID
// shared by every test in this file.
const validTwilioSID = "AC00000000000000000000000000000001"

// baseTwilioSettings returns a minimal settings map that passes
// validateTwilioSettings on its own (no region).
func baseTwilioSettings() map[string]any {
	return map[string]any{
		"account_sid": validTwilioSID,
		"auth_token":  "tok-secret",
		"from_number": "+15551234567",
	}
}

// newTwilioTestService builds a Service backed by an in-memory SQLite DB and
// a disabled (plaintext-fallback) credentials service, plus one organization.
// appConfig is left nil, so verifyTwilioWrite's test-run bypass never
// triggers here — every test that expects CreateIntegration/UpdateIntegration
// to succeed for a Twilio connection MUST stub verifyTwilioCredentials
// itself, exactly like production code would need a real Twilio account.
func newTwilioTestService(t *testing.T, orgSlug string) (*Service, string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	org := models.NewOrganization(orgSlug, "Twilio Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return NewService(dbSvc, creds, nil, nil), org.Slug
}

// stubVerify overrides the verifyTwilioCredentials seam for the duration of
// the test and returns a function reporting how many times it was called.
func stubVerify(t *testing.T, fn func(ctx context.Context, sid, token, baseURL string) error) *int {
	t.Helper()

	calls := 0
	prev := verifyTwilioCredentials
	verifyTwilioCredentials = func(ctx context.Context, sid, token, baseURL string) error {
		calls++

		return fn(ctx, sid, token, baseURL)
	}
	t.Cleanup(func() { verifyTwilioCredentials = prev })

	return &calls
}

// TestValidateTwilioSettings_Region pins the SSRF-guard requirement from the
// spec's resolved open questions: region is validated by FORMAT, not an
// enumerated allowlist, so an unknown-but-well-formed region is accepted and
// a malformed one is a VALIDATION_ERROR. Calls validateTwilioSettings
// directly — no DB, no network, no service needed.
func TestValidateTwilioSettings_Region(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	withRegion := func(region string) models.JSONMap {
		m := models.JSONMap{}
		for k, v := range baseTwilioSettings() {
			m[k] = v
		}
		if region != "" {
			m["region"] = region
		}

		return m
	}

	r.NoError(validateTwilioSettings(withRegion("")), "empty region (default) must be accepted")
	r.NoError(validateTwilioSettings(withRegion("us1")))
	r.NoError(validateTwilioSettings(withRegion("ie1")))
	r.NoError(validateTwilioSettings(withRegion("br2")), "unknown-but-well-formed region must be accepted — no allowlist")

	r.ErrorIs(validateTwilioSettings(withRegion("evil.com")), ErrInvalidSettings)
	r.ErrorIs(validateTwilioSettings(withRegion("../x")), ErrInvalidSettings)
	r.ErrorIs(validateTwilioSettings(withRegion("US1")), ErrInvalidSettings)
}

// TestCreateIntegration_TwilioVerifiesCredentials proves a POST always
// verifies credentials before persisting: the seam is called with the
// resolved region's base URL, and a rejection leaves nothing persisted.
//
//nolint:paralleltest // mutates the package-level verifyTwilioCredentials seam.
func TestCreateIntegration_TwilioVerifiesCredentials(t *testing.T) {
	svc, orgSlug := newTwilioTestService(t, "twilio-create-verify")
	ctx := t.Context()

	t.Run("accepted persists and calls the seam with the resolved region base", func(t *testing.T) {
		r := require.New(t)

		var gotBaseURL string

		calls := stubVerify(t, func(_ context.Context, _, _, baseURL string) error {
			gotBaseURL = baseURL

			return nil
		})

		settings := baseTwilioSettings()
		settings["region"] = "ie1"

		resp, err := svc.CreateIntegration(ctx, orgSlug, CreateIntegrationRequest{
			Type:     "twilio",
			Name:     "ie1-conn",
			Settings: settings,
		})
		r.NoError(err)
		r.NotNil(resp)
		r.Equal(1, *calls)
		r.Equal("https://api.ie1.twilio.com", gotBaseURL)
	})

	t.Run("rejected is not persisted", func(t *testing.T) {
		r := require.New(t)

		before, err := svc.ListIntegrations(ctx, orgSlug, nil)
		r.NoError(err)
		countBefore := len(before.Data)

		stubVerify(t, func(context.Context, string, string, string) error {
			return twilio.ErrCredentialsRejected
		})

		resp, err := svc.CreateIntegration(ctx, orgSlug, CreateIntegrationRequest{
			Type:     "twilio",
			Name:     "rejected-conn",
			Settings: baseTwilioSettings(),
		})
		r.Error(err)
		r.ErrorIs(err, ErrInvalidSettings)
		r.Nil(resp)

		after, err := svc.ListIntegrations(ctx, orgSlug, nil)
		r.NoError(err)
		r.Len(after.Data, countBefore, "a rejected credential check must not persist the connection")
	})

	t.Run("unverifiable (network/5xx) is also refused, with a distinct message", func(t *testing.T) {
		r := require.New(t)

		stubVerify(t, func(context.Context, string, string, string) error {
			return twilio.ErrCredentialsUnverifiable
		})

		resp, err := svc.CreateIntegration(ctx, orgSlug, CreateIntegrationRequest{
			Type:     "twilio",
			Name:     "unverifiable-conn",
			Settings: baseTwilioSettings(),
		})
		r.Error(err)
		r.ErrorIs(err, ErrInvalidSettings)
		r.Nil(resp)
		r.Contains(err.Error(), "could not verify")
	})
}

// TestUpdateIntegration_TwilioVerificationOnlyOnCredentialFields proves the
// PATCH-time rule: only a PATCH that actually changes the account SID, auth
// token, or region relative to what's stored triggers a fresh verification
// call. Non-secret settings fields use PATCH's wholesale-replace semantics
// (credentials.MergePatch), so — matching how the dashboard form actually
// submits — every request here resends the full settings object; only the
// value that's expected to change differs from baseTwilioSettings().
//
//nolint:paralleltest // mutates the package-level verifyTwilioCredentials seam.
func TestUpdateIntegration_TwilioVerificationOnlyOnCredentialFields(t *testing.T) {
	svc, orgSlug := newTwilioTestService(t, "twilio-update-verify")
	ctx := t.Context()

	stubVerify(t, func(context.Context, string, string, string) error { return nil })

	created, err := svc.CreateIntegration(ctx, orgSlug, CreateIntegrationRequest{
		Type:     "twilio",
		Name:     "conn",
		Settings: baseTwilioSettings(),
	})
	require.NoError(t, err)

	t.Run("changing only from_number does not call the seam", func(t *testing.T) {
		r := require.New(t)

		calls := stubVerify(t, func(context.Context, string, string, string) error {
			return nil
		})

		settings := baseTwilioSettings()
		settings["from_number"] = "+15559998888"

		resp, err := svc.UpdateIntegration(ctx, orgSlug, created.UID, UpdateIntegrationRequest{
			Settings: settings,
		})
		r.NoError(err)
		r.NotNil(resp)
		r.Equal(0, *calls, "a PATCH that doesn't change SID/token/region must not call verifyTwilioCredentials")
	})

	t.Run("renaming the connection (top-level Name, no Settings) does not call the seam", func(t *testing.T) {
		r := require.New(t)

		calls := stubVerify(t, func(context.Context, string, string, string) error {
			return nil
		})

		newName := "renamed-conn"
		resp, err := svc.UpdateIntegration(ctx, orgSlug, created.UID, UpdateIntegrationRequest{
			Name: &newName,
		})
		r.NoError(err)
		r.NotNil(resp)
		r.Equal(newName, resp.Name)
		r.Equal(0, *calls, "renaming must not touch Settings at all, let alone call verifyTwilioCredentials")
	})

	t.Run("changing auth_token calls the seam and, on reject, leaves the connection unchanged", func(t *testing.T) {
		r := require.New(t)

		before, err := svc.GetIntegration(ctx, orgSlug, created.UID)
		r.NoError(err)

		calls := stubVerify(t, func(context.Context, string, string, string) error {
			return twilio.ErrCredentialsRejected
		})

		settings := baseTwilioSettings()
		settings["auth_token"] = "rotated-bad-token"

		resp, err := svc.UpdateIntegration(ctx, orgSlug, created.UID, UpdateIntegrationRequest{
			Settings: settings,
		})
		r.Error(err)
		r.ErrorIs(err, ErrInvalidSettings)
		r.Nil(resp)
		r.Equal(1, *calls)

		after, err := svc.GetIntegration(ctx, orgSlug, created.UID)
		r.NoError(err)
		r.Equal(before.Settings["account_sid"], after.Settings["account_sid"])
		r.Equal(before.UpdatedAt, after.UpdatedAt, "a rejected verification must not write the update")
	})

	t.Run("changing region calls the seam with the newly resolved base URL", func(t *testing.T) {
		r := require.New(t)

		var gotBaseURL string

		calls := stubVerify(t, func(_ context.Context, _, _, baseURL string) error {
			gotBaseURL = baseURL

			return nil
		})

		settings := baseTwilioSettings()
		settings["region"] = "au1"

		resp, err := svc.UpdateIntegration(ctx, orgSlug, created.UID, UpdateIntegrationRequest{
			Settings: settings,
		})
		r.NoError(err)
		r.NotNil(resp)
		r.Equal(1, *calls)
		r.Equal("https://api.au1.twilio.com", gotBaseURL)
		r.Equal("au1", resp.Settings["region"])
	})
}

// TestVerifyTwilioWrite_SkippedInTestRunMode confirms the RunMode=="test"
// bypass: dev-test / Playwright E2E create Twilio connections with
// placeholder credentials and no real Twilio account to check them against,
// so the live call must not happen in that mode. This test drives
// verifyTwilioWrite directly against a Service whose appConfig has
// RunMode=="test", with the seam stubbed to fail — if the bypass didn't
// work, the call would fail.
//
//nolint:paralleltest // mutates the package-level verifyTwilioCredentials seam.
func TestVerifyTwilioWrite_SkippedInTestRunMode(t *testing.T) {
	r := require.New(t)

	calls := stubVerify(t, func(context.Context, string, string, string) error {
		return twilio.ErrCredentialsRejected
	})

	svc := &Service{appConfig: &config.Config{RunMode: "test"}}
	err := svc.verifyTwilioWrite(t.Context(), &models.TwilioSettings{
		AccountSID: validTwilioSID,
		AuthToken:  "whatever",
	})
	r.NoError(err, "RunMode==test must skip the live verification call entirely")
	r.Equal(0, *calls)
}
