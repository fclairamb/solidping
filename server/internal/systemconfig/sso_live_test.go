package systemconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func TestIsSSOParameterKey(t *testing.T) {
	t.Parallel()

	require.True(t, IsSSOParameterKey(string(KeyOIDCEnabled)))
	require.True(t, IsSSOParameterKey(string(KeySAMLIDPMetadataURL)))
	require.False(t, IsSSOParameterKey("email.host"))
}

// RefreshSSOConfig applies env > DB > startup config, like Initialize, and a
// deleted row falls back to the startup value.
func TestRefreshSSOConfigPrecedence(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{}
	cfg.GitLab.BaseURL = "https://gitlab.acme.com"
	cfg.OIDC.DisplayName = "Startup"

	t.Setenv("SP_OIDC_CLIENT_ID", "env-client")

	r.NoError(dbSvc.SetSystemParameter(ctx, string(KeyOIDCClientID), "db-client", false))
	r.NoError(dbSvc.SetSystemParameter(ctx, string(KeyOIDCDisplayName), "From DB", false))
	r.NoError(dbSvc.SetSystemParameter(ctx, string(KeyOIDCEnabled), true, false))
	r.NoError(RefreshSSOConfig(ctx, dbSvc, cfg))

	sso := cfg.SSO()
	r.Equal("env-client", sso.OIDC.ClientID, "env wins over the DB")
	r.Equal("From DB", sso.OIDC.DisplayName, "the DB wins over the startup config")
	r.True(sso.OIDC.Enabled)
	r.Equal("https://gitlab.acme.com", sso.GitLab.BaseURL, "a field with no parameter keeps the startup value")
	r.False(cfg.OIDC.Enabled, "the running struct is not mutated")

	r.NoError(dbSvc.DeleteSystemParameter(ctx, string(KeyOIDCDisplayName)))
	r.NoError(RefreshSSOConfig(ctx, dbSvc, cfg))
	r.Equal("Startup", cfg.SSO().OIDC.DisplayName)
}
