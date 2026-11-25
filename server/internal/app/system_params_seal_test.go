package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// TestStartupSealsPlaintextSystemSecrets covers the wiring of spec
// 2026-11-25-04: the startup auto-migrate pass seals a plaintext
// auth.jwt_secret left by a key-less install, without changing its value.
func TestStartupSealsPlaintextSystemSecrets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	raw, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(raw.Initialize(ctx))
	t.Cleanup(func() { _ = raw.Close() })

	const legacy = "acme-plaintext-jwt-secret-from-a-keyless-install"
	r.NoError(raw.SetSystemParameter(ctx, string(systemconfig.KeyJWTSecret), legacy, true))

	kek := make([]byte, 32)
	_, err = rand.Read(kek)
	r.NoError(err)

	cfg := &config.Config{}
	cfg.Encryption.MasterKey = base64.StdEncoding.EncodeToString(kek)
	cfg.Encryption.AutoMigrate = true

	sysParams, err := BuildSystemParamSealer(cfg, raw)
	r.NoError(err)
	r.True(sysParams.Enabled())

	creds, err := BuildCredentialsService(cfg, sysParams)
	r.NoError(err)

	registry := services.NewRegistry()
	registry.Credentials = creds

	server := &Server{config: cfg, dbService: sysParams, sysParams: sysParams, services: registry}
	r.NoError(server.MaybeAutoMigrateEncryption(ctx))

	stored, err := raw.GetSystemParameter(ctx, string(systemconfig.KeyJWTSecret))
	r.NoError(err)

	_, sealed := sysparamseal.SealedEnvelope(stored.Value)
	r.True(sealed, "the startup pass must seal the plaintext JWT secret")

	opened, err := sysParams.GetSystemParameter(ctx, string(systemconfig.KeyJWTSecret))
	r.NoError(err)
	r.Equal(legacy, opened.Value["value"])
}

// TestBuildSystemParamSealerWithoutKey keeps today's plaintext fallback.
func TestBuildSystemParamSealerWithoutKey(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	raw, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(raw.Initialize(ctx))
	t.Cleanup(func() { _ = raw.Close() })

	sysParams, err := BuildSystemParamSealer(&config.Config{}, raw)
	r.NoError(err)
	r.False(sysParams.Enabled())

	r.NoError(sysParams.SetSystemParameter(ctx, string(systemconfig.KeyJWTSecret), "acme-plain", true))

	keys, err := sysParams.PlaintextSecretKeys(ctx)
	r.NoError(err)
	r.Contains(keys, string(systemconfig.KeyJWTSecret))
}
