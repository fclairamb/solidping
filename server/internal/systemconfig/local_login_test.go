package systemconfig

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

type fakeParamReader struct {
	value any
	set   bool
}

func (f fakeParamReader) GetSystemParameter(_ context.Context, _ string) (*models.Parameter, error) {
	if !f.set {
		return nil, nil //nolint:nilnil // mirrors db.Service: nil, nil means absent
	}

	return &models.Parameter{Value: models.JSONMap{"value": f.value}}, nil
}

func ssoConfig(localLogin bool) *config.Config {
	return &config.Config{
		Auth:   config.AuthConfig{LocalLoginEnabled: localLogin},
		GitHub: config.GitHubOAuthConfig{Enabled: true, ClientID: "id", ClientSecret: "secret"},
	}
}

func TestResolveLocalLogin(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	// Startup config only.
	require.Equal(t, LocalLoginState{Configured: true, Enabled: true}, ResolveLocalLogin(ctx, nil, ssoConfig(true)))
	require.Equal(t, LocalLoginState{Configured: false, Enabled: false}, ResolveLocalLogin(ctx, nil, ssoConfig(false)))

	// The DB parameter wins over the startup config.
	state := ResolveLocalLogin(ctx, fakeParamReader{value: false, set: true}, ssoConfig(true))
	require.False(t, state.Enabled)
	state = ResolveLocalLogin(ctx, fakeParamReader{value: "true", set: true}, ssoConfig(false))
	require.True(t, state.Enabled)

	// No SSO provider: configured off, enforced on.
	state = ResolveLocalLogin(ctx, fakeParamReader{value: false, set: true}, &config.Config{})
	require.False(t, state.Configured)
	require.True(t, state.Enabled)
	require.True(t, state.FailedOpen())
}

func TestResolveLocalLogin_EnvWins(t *testing.T) {
	t.Setenv(config.EnvLocalLoginEnabled, "false")

	state := ResolveLocalLogin(t.Context(), fakeParamReader{value: true, set: true}, ssoConfig(true))
	require.False(t, state.Enabled)
}

func TestValidateLocalLoginParameter(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateLocalLoginParameter(&config.Config{}, true))
	require.NoError(t, ValidateLocalLoginParameter(nil, "true"))
	require.ErrorIs(t, ValidateLocalLoginParameter(&config.Config{}, false), ErrNoSSOProvider)
	require.ErrorIs(t, ValidateLocalLoginParameter(nil, "false"), ErrNoSSOProvider)
	require.NoError(t, ValidateLocalLoginParameter(ssoConfig(true), false))
}

//nolint:paralleltest // swaps the default slog logger.
func TestWarnLocalLoginFailOpen(t *testing.T) {
	var buf bytes.Buffer

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))

	t.Cleanup(func() { slog.SetDefault(previous) })

	state := WarnLocalLoginFailOpen(t.Context(), nil, &config.Config{})
	require.True(t, state.FailedOpen())
	require.Contains(t, buf.String(), "keeping local login ON")

	buf.Reset()
	WarnLocalLoginFailOpen(t.Context(), nil, ssoConfig(false))
	require.Empty(t, buf.String(), "no warning when an SSO provider enforces it")
}

func TestLocalLoginAndGroupParameters(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Auth: config.AuthConfig{LocalLoginEnabled: true}}

	inputs := map[ParameterKey][2]string{
		KeyLocalLoginEnabled:    {config.EnvLocalLoginEnabled, "false"},
		KeyOIDCGroupsClaim:      {"SP_OIDC_GROUPS_CLAIM", " roles "},
		KeyOIDCSuperAdminGroups: {"SP_OIDC_SUPER_ADMIN_GROUPS", "a, b"},
		KeyOIDCOrgAdminGroups:   {"SP_OIDC_ORG_ADMIN_GROUPS", "c"},
	}

	seen := 0

	for _, def := range getKnownParameters() {
		input, ok := inputs[def.Key]
		if !ok {
			continue
		}

		seen++

		require.Equal(t, input[0], def.EnvVar)
		def.ApplyFunc(cfg, input[1])
	}

	require.Equal(t, len(inputs), seen)
	require.False(t, cfg.Auth.LocalLoginEnabled)
	require.Equal(t, "roles", cfg.OIDC.GroupsClaim)
	require.Equal(t, []string{"a", "b"}, cfg.OIDC.SuperAdminGroupList())
	require.Equal(t, []string{"c"}, cfg.OIDC.OrgAdminGroupList())
}
