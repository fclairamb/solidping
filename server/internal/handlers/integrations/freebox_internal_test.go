package integrations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

// TestValidateFreeboxBaseURLSelfHosted covers the URL-contract half of spec
// 2026-09-25-31 with no appConfig at all — the shape unit tests across this
// package use (NewService(db, creds, nil, nil)) — so only freebox.ValidateBaseURL
// itself is exercised, never the SaaS-mode restriction.
func TestValidateFreeboxBaseURLSelfHosted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := &Service{}

	r.NoError(svc.validateFreeboxBaseURL(""))
	r.NoError(svc.validateFreeboxBaseURL(freebox.DefaultBaseURL))
	r.NoError(svc.validateFreeboxBaseURL("http://192.168.1.254"))

	err := svc.validateFreeboxBaseURL("https://evil.example")
	r.ErrorIs(err, ErrInvalidSettings)
}

// TestValidateFreeboxBaseURLSaaSMode covers the SaaS-mode restriction without
// ever dialing a network: the default always passes (it is not an
// "override"), and any non-default value — even one that would otherwise
// satisfy the URL contract — is rejected outright, because pairing (and every
// authenticated call that follows it) is meant to run from the member's own
// network or a private-location agent, never a shared SaaS worker.
func TestValidateFreeboxBaseURLSaaSMode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := &Service{appConfig: &config.Config{
		Deployment: config.DeploymentConfig{Mode: config.DeploymentModeSaaS},
	}}

	// The default is not an override: still allowed.
	r.NoError(svc.validateFreeboxBaseURL(""))
	r.NoError(svc.validateFreeboxBaseURL(freebox.DefaultBaseURL))

	// A private-range IP would pass the URL contract on its own, but SaaS
	// mode disables the override entirely.
	err := svc.validateFreeboxBaseURL("http://192.168.1.254")
	r.ErrorIs(err, ErrInvalidSettings)
	r.Contains(err.Error(), "not available on this deployment")
}

// TestValidateFreeboxBaseURLSelfHostedAllowsOverride is the mirror of the
// SaaS-mode test: an explicit self-hosted mode (not just a nil appConfig)
// still allows a valid override.
func TestValidateFreeboxBaseURLSelfHostedAllowsOverride(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := &Service{appConfig: &config.Config{
		Deployment: config.DeploymentConfig{Mode: config.DeploymentModeSelfHosted},
	}}

	r.NoError(svc.validateFreeboxBaseURL("http://192.168.1.254"))
}

// TestValidateFreeboxSettingsIgnoresOtherTypes confirms
// validateFreeboxSettings is a no-op for every connection type but freebox —
// mirroring how validateSenderURLSettings is scoped by senderURLSettingsKey.
func TestValidateFreeboxSettingsIgnoresOtherTypes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := &Service{}

	err := svc.validateFreeboxSettings(
		models.ConnectionTypeWebhook, models.JSONMap{"baseUrl": "https://evil.example"})
	r.NoError(err)
}
