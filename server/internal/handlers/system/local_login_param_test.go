package system

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// The lock-out guard (spec 2026-11-21-02): local login cannot be switched off
// through the API while no SSO provider is enabled.
func TestSetParameterRefusesLocalLoginOffWithoutSSO(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	key := string(systemconfig.KeyLocalLoginEnabled)
	cfg := &config.Config{}

	svc := NewService(dbSvc)
	svc.SetAppConfig(cfg)

	_, err = svc.SetParameter(ctx, key, false, false)
	r.ErrorIs(err, ErrInvalidParameter)
	r.ErrorIs(err, systemconfig.ErrNoSSOProvider)

	stored, err := dbSvc.GetSystemParameter(ctx, key)
	r.NoError(err)
	r.Nil(stored, "the refused value is not persisted")

	_, err = svc.SetParameter(ctx, key, true, false)
	r.NoError(err, "turning it on is always allowed")

	// Saved through the same service, the provider is live at once (spec
	// 2026-11-25-02): no restart between the two saves.
	for k, v := range map[systemconfig.ParameterKey]any{
		systemconfig.KeyOIDCIssuerURL:    "https://idp.acme.com",
		systemconfig.KeyOIDCClientID:     "id",
		systemconfig.KeyOIDCClientSecret: "s",
		systemconfig.KeyOIDCEnabled:      true,
	} {
		_, err = svc.SetParameter(ctx, string(k), v, k == systemconfig.KeyOIDCClientSecret)
		r.NoError(err)
	}

	_, err = svc.SetParameter(ctx, key, false, false)
	r.NoError(err, "allowed once an SSO provider is enabled")
}
