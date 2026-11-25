package config

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSSOReadsTheBootStructUntilLiveSSOIsEnabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &Config{}
	cfg.Google = GoogleOAuthConfig{Enabled: true, ClientID: "id", ClientSecret: "secret"}
	r.True(cfg.SSO().RoutesActive(SSOProviderGoogle))

	cfg.PublishSSO(&SSOConfig{})
	r.True(cfg.SSO().RoutesActive(SSOProviderGoogle), "publish is a no-op before EnableLiveSSO")
}

func TestPublishSSOReplacesTheSnapshot(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &Config{}
	cfg.OIDC = OIDCOAuthConfig{DisplayName: "Boot"}
	cfg.EnableLiveSSO()

	r.Equal("Boot", cfg.SSOBase().OIDC.DisplayName)
	r.Empty(cfg.SSOProviders(), "no snapshot yet: the boot struct, OIDC not configured")

	cfg.PublishSSO(&SSOConfig{OIDC: OIDCOAuthConfig{
		Enabled: true, IssuerURL: "https://idp.acme.com", ClientID: "id", ClientSecret: "s", DisplayName: "Acme",
	}})

	r.Equal([]SSOProvider{{Name: "Acme", Type: "oidc"}}, cfg.SSOProviders())
	r.True(cfg.SSO().RoutesActive(SSOProviderOIDC))
	r.False(cfg.OIDC.Enabled, "the boot struct is never mutated")
	r.Equal("Boot", cfg.SSOBase().OIDC.DisplayName, "the base stays the startup config")
}

func TestRoutesActiveOIDCNeedsTheClientSecret(t *testing.T) {
	t.Parallel()

	snapshot := &SSOConfig{OIDC: OIDCOAuthConfig{Enabled: true, IssuerURL: "https://idp.acme.com", ClientID: "id"}}
	require.False(t, snapshot.RoutesActive(SSOProviderOIDC))

	snapshot.OIDC.ClientSecret = "s"
	require.True(t, snapshot.RoutesActive(SSOProviderOIDC))
	require.False(t, snapshot.RoutesActive("unknown"))
}

func TestRoutesActiveSAMLNeedsOnlyEnabled(t *testing.T) {
	t.Parallel()

	require.True(t, (&SSOConfig{SAML: SAMLConfig{Enabled: true}}).RoutesActive(SSOProviderSAML),
		"the SP metadata is served before an IdP is configured")
}

// A snapshot older than the TTL is re-resolved in the background: this is how
// the other instances of a multi-instance deployment catch up.
func TestStaleSSOSnapshotIsReloaded(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &Config{}
	cfg.EnableLiveSSO()
	cfg.PublishSSO(&SSOConfig{})

	var calls atomic.Int32

	cfg.SetSSOReloader(func(context.Context) (*SSOConfig, error) {
		calls.Add(1)

		return &SSOConfig{Google: GoogleOAuthConfig{Enabled: true, ClientID: "id", ClientSecret: "s"}}, nil
	})

	_ = cfg.SSO()
	r.Zero(calls.Load(), "a fresh snapshot is not reloaded")

	cfg.sso.loadedAt.Store(time.Now().Add(-2 * SSOConfigRefreshTTL).UnixNano())
	_ = cfg.SSO()

	r.Eventually(func() bool { return cfg.SSO().RoutesActive(SSOProviderGoogle) }, 2*time.Second, 5*time.Millisecond)
	r.Equal(int32(1), calls.Load())
}

func TestFailedSSOReloadKeepsTheSnapshot(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &Config{}
	cfg.EnableLiveSSO()
	cfg.PublishSSO(&SSOConfig{Google: GoogleOAuthConfig{Enabled: true, ClientID: "id"}})

	done := make(chan struct{})

	cfg.SetSSOReloader(func(context.Context) (*SSOConfig, error) {
		defer close(done)

		return nil, errors.New("db down")
	})

	cfg.sso.loadedAt.Store(time.Now().Add(-2 * SSOConfigRefreshTTL).UnixNano())
	_ = cfg.SSO()
	<-done

	r.True(cfg.SSO().RoutesActive(SSOProviderGoogle))
}
