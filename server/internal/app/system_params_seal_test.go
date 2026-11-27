package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
	"github.com/fclairamb/solidping/server/internal/secretref"
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

	// A secret org parameter behind `${param:…}` (spec 2026-11-27-02).
	org := models.NewOrganization("acme-startup-seal", "Acme")
	r.NoError(raw.CreateOrganization(ctx, org))

	const legacyOrgSecret = "acme-plaintext-org-api-token"

	orgKey := paramkeys.StorageKey("api-token")
	r.NoError(raw.SetOrgParameter(ctx, org.UID, orgKey, legacyOrgSecret, true))

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

	storedOrg, err := raw.GetOrgParameter(ctx, org.UID, orgKey)
	r.NoError(err)

	_, orgSealed := sysparamseal.SealedEnvelope(storedOrg.Value)
	r.True(orgSealed, "the startup pass must seal the plaintext secret org parameter")

	resolved, err := secretref.LookupParam(ctx, sysParams, org.UID, "api-token")
	r.NoError(err)
	r.Equal(legacyOrgSecret, resolved, "the value a check resolves does not change")

	// A second boot seals nothing.
	again, err := sysParams.SealPlaintextOrgParameters(ctx)
	r.NoError(err)
	r.Zero(again)
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

	org := models.NewOrganization("acme-warn", "Acme")
	r.NoError(raw.CreateOrganization(ctx, org))
	r.NoError(raw.SetOrgParameter(ctx, org.UID, paramkeys.StorageKey("acme-private-name"), "acme-v", true))

	keyless, err := BuildSystemParamSealer(&config.Config{}, raw)
	r.NoError(err)

	server := &Server{sysParams: keyless}
	server.warnPlaintextSystemSecrets(ctx)
	r.Contains(logs.String(), "secret system parameters stored in plaintext")
	r.Contains(logs.String(), string(systemconfig.KeyJWTSecret))
	r.Contains(logs.String(), sysparamseal.AppSettingVAPIDPrivateKey)
	r.Contains(logs.String(), "secretOrgParameters=1")
	r.NotContains(logs.String(), "acme-private-name", "an org's own keys are counted, not named")

	// Org parameters alone are enough to warn.
	onlyOrg := newSealTestDB(t)
	orgOnly := models.NewOrganization("acme-warn-only", "Acme")
	r.NoError(onlyOrg.CreateOrganization(ctx, orgOnly))
	r.NoError(onlyOrg.SetOrgParameter(ctx, orgOnly.UID, paramkeys.StorageKey("k"), "v", true))

	onlyOrgSealer, err := BuildSystemParamSealer(&config.Config{}, onlyOrg)
	r.NoError(err)

	logs.Reset()
	(&Server{sysParams: onlyOrgSealer}).warnPlaintextSystemSecrets(ctx)
	r.Contains(logs.String(), "secretOrgParameters=1")
	logs.Reset()

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

var errSettingsRead = errors.New("settings read failed")

// failingSettingsDB reads system parameters normally but fails the app_settings
// bulk read, as a database that loses the table mid-boot would.
type failingSettingsDB struct {
	db.Service
}

func (failingSettingsDB) GetAppSettings(context.Context, []string) (map[string]string, error) {
	return nil, errSettingsRead
}

// TestSealPlaintextSystemParametersSurfacesAppSettingsFailure: a failed
// app_settings sweep stops the startup pass with a wrapped error.
func TestSealPlaintextSystemParametersSurfacesAppSettingsFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	raw := newSealTestDB(t)

	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	r.NoError(err)

	cfg := &config.Config{}
	cfg.Encryption.MasterKey = base64.StdEncoding.EncodeToString(kek)

	sysParams, err := BuildSystemParamSealer(cfg, failingSettingsDB{Service: raw})
	r.NoError(err)

	server := &Server{config: cfg, sysParams: sysParams}
	err = server.sealPlaintextSystemParameters(ctx)
	r.ErrorIs(err, errSettingsRead)
	r.ErrorContains(err, "auto-migrate app settings")
}

var errOrgsRead = errors.New("organizations read failed")

// failingOrgsDB fails the organization listing the org parameter sweep needs.
type failingOrgsDB struct {
	db.Service
}

func (failingOrgsDB) ListOrganizations(context.Context) ([]*models.Organization, error) {
	return nil, errOrgsRead
}

// TestSealPlaintextSystemParametersSurfacesOrgParamsFailure: a failed sweep of
// the secret org parameters fails the boot with a wrapped error.
func TestSealPlaintextSystemParametersSurfacesOrgParamsFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	r.NoError(err)

	cfg := &config.Config{}
	cfg.Encryption.MasterKey = base64.StdEncoding.EncodeToString(kek)

	sysParams, err := BuildSystemParamSealer(cfg, failingOrgsDB{Service: newSealTestDB(t)})
	r.NoError(err)

	err = (&Server{config: cfg, sysParams: sysParams}).sealPlaintextSystemParameters(ctx)
	r.ErrorIs(err, errOrgsRead)
	r.ErrorContains(err, "auto-migrate organization parameters")
}
