package integrations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/smsfactor"
)

// Internal package for the same reason as service_twilio_test.go: the
// verifySMSFactorCredentials seam keeps every test off the network.

func baseSMSFactorSettings() map[string]any {
	return map[string]any{
		"token":      "sf-token",
		"sender":     "ACME",
		"to_numbers": []any{"+33612345678"},
	}
}

// stubSMSFactorVerify overrides the verifySMSFactorCredentials seam for the
// duration of the test and returns how many times it was called.
func stubSMSFactorVerify(t *testing.T, fn func(ctx context.Context, token string) error) *int {
	t.Helper()

	calls := 0
	prev := verifySMSFactorCredentials
	verifySMSFactorCredentials = func(ctx context.Context, token string) error {
		calls++

		return fn(ctx, token)
	}
	t.Cleanup(func() { verifySMSFactorCredentials = prev })

	return &calls
}

func TestValidateSMSFactorSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(map[string]any)
		wantErr bool
	}{
		{name: "complete", mutate: nil},
		{name: "no sender", mutate: func(m map[string]any) { delete(m, "sender") }},
		{name: "no recipients", mutate: func(m map[string]any) { delete(m, "to_numbers") }},
		{name: "sender with a space", mutate: func(m map[string]any) { m["sender"] = "Acme Ops" }},
		{name: "missing token", mutate: func(m map[string]any) { delete(m, "token") }, wantErr: true},
		{name: "blank token", mutate: func(m map[string]any) { m["token"] = "  " }, wantErr: true},
		{name: "sender too long", mutate: func(m map[string]any) { m["sender"] = "AcmeAlerting" }, wantErr: true},
		{name: "sender with punctuation", mutate: func(m map[string]any) { m["sender"] = "acme.com" }, wantErr: true},
		{
			name: "recipient not E.164", mutate: func(m map[string]any) { m["to_numbers"] = []any{"0612345678"} },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			settings := baseSMSFactorSettings()
			if tt.mutate != nil {
				tt.mutate(settings)
			}

			err := validateConnectionType(models.ConnectionTypeSMSFactor, settings)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidSettings)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

//nolint:paralleltest // mutates the package-level verifySMSFactorCredentials seam.
func TestCreateIntegration_SMSFactorVerifiesToken(t *testing.T) {
	svc, orgSlug := newTwilioTestService(t, "sf-create")
	ctx := t.Context()

	t.Run("accepted persists, secret is not echoed", func(t *testing.T) {
		r := require.New(t)

		var gotToken string

		calls := stubSMSFactorVerify(t, func(_ context.Context, token string) error {
			gotToken = token

			return nil
		})

		resp, err := svc.CreateIntegration(ctx, orgSlug, CreateIntegrationRequest{
			Type: "smsfactor", Name: "sf", Settings: baseSMSFactorSettings(),
		})
		r.NoError(err)
		r.NotNil(resp)
		r.Equal(1, *calls)
		r.Equal("sf-token", gotToken)
		r.Equal("ACME", resp.Settings["sender"])
		r.NotContains(resp.Settings, "token", "the API token is a secret and never returned")
	})

	t.Run("rejected is not persisted", func(t *testing.T) {
		r := require.New(t)

		before, err := svc.ListIntegrations(ctx, orgSlug, nil)
		r.NoError(err)

		stubSMSFactorVerify(t, func(context.Context, string) error { return smsfactor.ErrTokenRejected })

		resp, err := svc.CreateIntegration(ctx, orgSlug, CreateIntegrationRequest{
			Type: "smsfactor", Name: "sf-bad", Settings: baseSMSFactorSettings(),
		})
		r.ErrorIs(err, ErrInvalidSettings)
		r.Contains(err.Error(), "rejected")
		r.Nil(resp)

		after, err := svc.ListIntegrations(ctx, orgSlug, nil)
		r.NoError(err)
		r.Len(after.Data, len(before.Data))
	})

	t.Run("unverifiable is refused with a distinct message", func(t *testing.T) {
		r := require.New(t)

		stubSMSFactorVerify(t, func(context.Context, string) error { return smsfactor.ErrTokenUnverifiable })

		resp, err := svc.CreateIntegration(ctx, orgSlug, CreateIntegrationRequest{
			Type: "smsfactor", Name: "sf-down", Settings: baseSMSFactorSettings(),
		})
		r.ErrorIs(err, ErrInvalidSettings)
		r.Contains(err.Error(), "could not verify")
		r.Nil(resp)
	})

	t.Run("invalid settings never reach the seam", func(t *testing.T) {
		r := require.New(t)

		calls := stubSMSFactorVerify(t, func(context.Context, string) error { return nil })

		settings := baseSMSFactorSettings()
		settings["to_numbers"] = []any{"not-a-number"}

		_, err := svc.CreateIntegration(ctx, orgSlug, CreateIntegrationRequest{
			Type: "smsfactor", Name: "sf-invalid", Settings: settings,
		})
		r.ErrorIs(err, ErrInvalidSettings)
		r.Equal(0, *calls)
	})
}

//nolint:paralleltest // mutates the package-level verifySMSFactorCredentials seam.
func TestUpdateIntegration_SMSFactorVerifiesOnlyOnTokenChange(t *testing.T) {
	svc, orgSlug := newTwilioTestService(t, "sf-update")
	ctx := t.Context()

	stubSMSFactorVerify(t, func(context.Context, string) error { return nil })

	created, err := svc.CreateIntegration(ctx, orgSlug, CreateIntegrationRequest{
		Type: "smsfactor", Name: "sf", Settings: baseSMSFactorSettings(),
	})
	require.NoError(t, err)

	t.Run("changing only the sender does not call the seam", func(t *testing.T) {
		r := require.New(t)

		calls := stubSMSFactorVerify(t, func(context.Context, string) error { return nil })

		settings := baseSMSFactorSettings()
		delete(settings, "token")
		settings["sender"] = "AcmeOps"

		resp, err := svc.UpdateIntegration(ctx, orgSlug, created.UID, UpdateIntegrationRequest{Settings: settings})
		r.NoError(err)
		r.Equal("AcmeOps", resp.Settings["sender"])
		r.Equal(0, *calls)
	})

	t.Run("an invalid merged sender is refused", func(t *testing.T) {
		r := require.New(t)

		settings := baseSMSFactorSettings()
		delete(settings, "token")
		settings["sender"] = "way too long sender"

		_, err := svc.UpdateIntegration(ctx, orgSlug, created.UID, UpdateIntegrationRequest{Settings: settings})
		r.ErrorIs(err, ErrInvalidSettings)
	})

	t.Run("a new token is verified and refused on rejection", func(t *testing.T) {
		r := require.New(t)

		calls := stubSMSFactorVerify(t, func(context.Context, string) error { return smsfactor.ErrTokenRejected })

		settings := baseSMSFactorSettings()
		settings["token"] = "rotated-bad"

		_, err := svc.UpdateIntegration(ctx, orgSlug, created.UID, UpdateIntegrationRequest{Settings: settings})
		r.ErrorIs(err, ErrInvalidSettings)
		r.Equal(1, *calls)
	})
}

//nolint:paralleltest // mutates the package-level verifySMSFactorCredentials seam.
func TestVerifySMSFactorWrite_SkippedInTestRunMode(t *testing.T) {
	r := require.New(t)

	calls := stubSMSFactorVerify(t, func(context.Context, string) error { return smsfactor.ErrTokenRejected })

	svc := &Service{appConfig: &config.Config{RunMode: "test"}}
	r.NoError(svc.verifySMSFactorWrite(t.Context(), &models.SMSFactorSettings{Token: "placeholder"}))
	r.Equal(0, *calls)
}

func TestSMSFactorMalformedSettings(t *testing.T) {
	t.Parallel()

	malformed := models.JSONMap{"token": 42}
	svc := &Service{}

	require.ErrorIs(t, validateSMSFactorSettings(malformed), ErrInvalidSettings)
	require.ErrorIs(t, svc.verifySMSFactorOnCreate(t.Context(), malformed), ErrInvalidSettings)

	valid := map[string]any{"token": "tok"}

	// Merged settings are validated first; the existing ones are parsed next.
	require.ErrorIs(t, svc.verifySMSFactorOnUpdate(t.Context(), map[string]any{"token": 42}, valid), ErrInvalidSettings)
	require.ErrorIs(t, svc.verifySMSFactorOnUpdate(t.Context(), valid, map[string]any{"token": 42}), ErrInvalidSettings)
}
