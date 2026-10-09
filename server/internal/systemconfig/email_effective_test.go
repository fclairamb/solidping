package systemconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func TestEffectiveEmailConfig_EnvOverDBFresh(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	base := config.EmailConfig{Host: "startup.acme.com", Port: 587}

	// Env password, no DB password: the password comes from the env.
	t.Setenv("SP_EMAIL_PASSWORD", "from-env")
	r.NoError(dbSvc.SetSystemParameter(ctx, "email.host", "db1.acme.com", false))

	cfg, err := EffectiveEmailConfig(ctx, dbSvc, &base)
	r.NoError(err)
	r.Equal("from-env", cfg.Password)
	r.Equal("db1.acme.com", cfg.Host)

	// A later DB change is visible without a restart.
	r.NoError(dbSvc.SetSystemParameter(ctx, "email.host", "db2.acme.com", false))

	cfg, err = EffectiveEmailConfig(ctx, dbSvc, &base)
	r.NoError(err)
	r.Equal("db2.acme.com", cfg.Host)

	// Env wins over DB for the same field.
	t.Setenv("SP_EMAIL_HOST", "env.acme.com")

	cfg, err = EffectiveEmailConfig(ctx, dbSvc, &base)
	r.NoError(err)
	r.Equal("env.acme.com", cfg.Host)
	r.Equal("startup.acme.com", base.Host, "base must not be mutated")
}
