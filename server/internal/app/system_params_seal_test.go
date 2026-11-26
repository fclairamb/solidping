package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
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

	// The SAML SP and VAPID private keys of spec 2026-11-26-06.
	legacySettings := map[string]string{
		sysparamseal.AppSettingSAMLSPPrivateKey: "-----BEGIN RSA PRIVATE KEY-----\nacme\n-----END RSA PRIVATE KEY-----\n",
		sysparamseal.AppSettingVAPIDPrivateKey:  "acme-plaintext-vapid-private-key",
	}
	r.NoError(raw.SetAppSettings(ctx, legacySettings))

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

	for key, value := range legacySettings {
		storedSetting, getErr := raw.GetAppSetting(ctx, key)
		r.NoError(getErr)
		r.NotEqual(value, storedSetting)

		_, settingSealed := sysparamseal.SealedAppSettingEnvelope(storedSetting)
		r.True(settingSealed, "the startup pass must seal %s", key)

		openedSetting, openErr := sysParams.GetAppSetting(ctx, key)
		r.NoError(openErr)
		r.Equal(value, openedSetting)
	}
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

func newSealTestDB(t *testing.T) *sqlite.Service {
	t.Helper()

	ctx := context.Background()

	raw, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, raw.Initialize(ctx))
	t.Cleanup(func() { _ = raw.Close() })

	return raw
}

// TestBuildSystemParamSealerRejectsBadMasterKey: a malformed or unreadable
// master key fails the build instead of silently disabling encryption.
func TestBuildSystemParamSealerRejectsBadMasterKey(t *testing.T) {
	t.Parallel()

	raw := newSealTestDB(t)

	cfg := &config.Config{}
	cfg.Encryption.MasterKey = base64.StdEncoding.EncodeToString([]byte("too-short"))
	_, err := BuildSystemParamSealer(cfg, raw)
	require.ErrorIs(t, err, credentials.ErrInvalidKey)

	cfg = &config.Config{}
	cfg.Encryption.MasterKeyFile = filepath.Join(t.TempDir(), "missing")
	_, err = BuildSystemParamSealer(cfg, raw)
	require.ErrorContains(t, err, "read master key file")
}

// TestNewServerRejectsBadMasterKey: the boot stops on a bad master key.
func TestNewServerRejectsBadMasterKey(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Database.Type = "sqlite-memory"
	cfg.Encryption.MasterKey = base64.StdEncoding.EncodeToString([]byte("too-short"))

	_, err := NewServer(context.Background(), cfg)
	require.ErrorIs(t, err, credentials.ErrInvalidKey)
	require.ErrorContains(t, err, "init system parameter encryption")
}

// TestSealPlaintextSystemParameters covers the no-op and failure paths of the
// startup sweep.
func TestSealPlaintextSystemParameters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	// No decorator installed: nothing to do.
	r.NoError((&Server{}).sealPlaintextSystemParameters(ctx))

	raw := newSealTestDB(t)
	r.NoError(raw.SetSystemParameter(ctx, string(systemconfig.KeyJWTSecret), "acme-plain", true))

	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	r.NoError(err)

	cfg := &config.Config{}
	cfg.Encryption.MasterKey = base64.StdEncoding.EncodeToString(kek)

	sysParams, err := BuildSystemParamSealer(cfg, raw)
	r.NoError(err)

	server := &Server{config: cfg, sysParams: sysParams}
	r.NoError(server.sealPlaintextSystemParameters(ctx))

	// The sweep is idempotent.
	r.NoError(server.sealPlaintextSystemParameters(ctx))

	// A closed database fails the sweep and the error is wrapped.
	r.NoError(raw.Close())
	err = server.sealPlaintextSystemParameters(ctx)
	r.ErrorContains(err, "auto-migrate system parameters")
}

// TestMaybeAutoMigrateEncryptionStopsOnSealFailure: the startup pass surfaces a
// failed system parameter sweep.
func TestMaybeAutoMigrateEncryptionStopsOnSealFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	raw := newSealTestDB(t)

	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	r.NoError(err)

	cfg := &config.Config{}
	cfg.Encryption.MasterKey = base64.StdEncoding.EncodeToString(kek)
	cfg.Encryption.AutoMigrate = true

	sysParams, err := BuildSystemParamSealer(cfg, raw)
	r.NoError(err)

	creds, err := BuildCredentialsService(cfg, sysParams)
	r.NoError(err)

	registry := services.NewRegistry()
	registry.Credentials = creds

	server := &Server{config: cfg, dbService: sysParams, sysParams: sysParams, services: registry}

	r.NoError(raw.Close())
	r.ErrorContains(server.MaybeAutoMigrateEncryption(ctx), "auto-migrate system parameters")
}

// TestWarnPlaintextSystemSecrets: the warning names the plaintext secrets of a
// key-less install, and stays silent with a key or on a broken database.
//
//nolint:paralleltest // swaps the process-wide default logger
func TestWarnPlaintextSystemSecrets(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	var logs bytes.Buffer

	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))

	// No decorator: silent.
	(&Server{}).warnPlaintextSystemSecrets(ctx)
	r.Empty(logs.String())

	raw := newSealTestDB(t)
	r.NoError(raw.SetSystemParameter(ctx, string(systemconfig.KeyJWTSecret), "acme-plain", true))
	r.NoError(raw.SetAppSetting(ctx, sysparamseal.AppSettingVAPIDPrivateKey, "acme-plain-vapid"))

	keyless, err := BuildSystemParamSealer(&config.Config{}, raw)
	r.NoError(err)

	server := &Server{sysParams: keyless}
	server.warnPlaintextSystemSecrets(ctx)
	r.Contains(logs.String(), "secret system parameters stored in plaintext")
	r.Contains(logs.String(), string(systemconfig.KeyJWTSecret))
	r.Contains(logs.String(), sysparamseal.AppSettingVAPIDPrivateKey)

	// With a master key the warning does not apply.
	logs.Reset()

	kek := make([]byte, 32)
	_, err = rand.Read(kek)
	r.NoError(err)

	cfg := &config.Config{}
	cfg.Encryption.MasterKey = base64.StdEncoding.EncodeToString(kek)

	keyed, err := BuildSystemParamSealer(cfg, raw)
	r.NoError(err)

	(&Server{sysParams: keyed}).warnPlaintextSystemSecrets(ctx)
	r.Empty(logs.String())

	// A list failure is swallowed (a warning must never fail the boot).
	r.NoError(raw.Close())
	server.warnPlaintextSystemSecrets(ctx)
	r.NotContains(logs.String(), "stored in plaintext")

	// Nothing to name once the table holds no plaintext secret.
	clean, err := BuildSystemParamSealer(&config.Config{}, newSealTestDB(t))
	r.NoError(err)

	logs.Reset()
	(&Server{sysParams: clean}).warnPlaintextSystemSecrets(ctx)
	r.Empty(logs.String())
}
