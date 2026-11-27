package sysparamseal_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
	"github.com/fclairamb/solidping/server/internal/jmap"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

const embeddedPGPort = 5497

// TestSealSQLite and TestSealPostgres run the same suite on both dialects.
//
//nolint:paralleltest // the suite uses t.Setenv
func TestSealSQLite(t *testing.T) {
	raw, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, raw.Initialize(t.Context()))
	t.Cleanup(func() { _ = raw.Close() })

	runSuite(t, raw)
}

//nolint:paralleltest // the suite uses t.Setenv
func TestSealPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("embedded postgres: non-short only")
	}

	raw, err := postgres.NewEmbedded(t.Context(), "sysparamseal", embeddedPGPort, false, "", false, 0)
	require.NoError(t, err)
	require.NoError(t, raw.Initialize(t.Context()))
	t.Cleanup(func() { _ = raw.Close() })

	runSuite(t, raw)
}

// runSuite runs every case on its own keys of one database. The cases that
// read auth.jwt_secret run in sequence on purpose: they share that row.
func runSuite(t *testing.T, raw db.Service) {
	t.Helper()

	key := newMasterKey(t)

	t.Run("GeneratedJWTSecretIsSealed", func(t *testing.T) { testGeneratedJWTSecretIsSealed(t, raw, key) })
	t.Run("RemovedMasterKeyFailsBoot", func(t *testing.T) { testRemovedMasterKeyFailsBoot(t, raw) })
	t.Run("WrongMasterKeyFailsBoot", func(t *testing.T) { testWrongMasterKeyFailsBoot(t, raw) })
	t.Run("SecretParameterIsSealed", func(t *testing.T) { testSecretParameterIsSealed(t, raw, key) })
	t.Run("EmailInboxIsSealed", func(t *testing.T) { testEmailInboxIsSealed(t, raw, key) })
	t.Run("NonSecretParameterStaysPlain", func(t *testing.T) { testNonSecretParameterStaysPlain(t, raw, key) })
	t.Run("ReservedDEKKey", func(t *testing.T) { testReservedDEKKey(t, raw, key) })
	t.Run("MigrationSealsPlaintextJWTSecret", func(t *testing.T) { testMigrationSealsPlaintextJWT(t, raw, key) })
	t.Run("MigrationSealsPlaintextAppSettingKeys", func(t *testing.T) { testMigrationSealsAppSettings(t, raw, key) })
	runOrgSuite(t, raw, key)
}

func newMasterKey(t *testing.T) []byte {
	t.Helper()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	return key
}

func wrap(t *testing.T, raw db.Service, masterKey []byte) *sysparamseal.Service {
	t.Helper()

	sealer, err := credentials.NewSystemSealer(masterKey, raw)
	require.NoError(t, err)

	return sysparamseal.Wrap(raw, sealer, systemconfig.ShouldSealParameter)
}

// rawValue reads the stored JSON with a plain SELECT, bypassing the decorator.
func rawValue(t *testing.T, svc db.Service, key string) string {
	t.Helper()

	var value string
	require.NoError(t, svc.DB().NewRaw(
		"SELECT CAST(value AS TEXT) FROM parameters "+
			"WHERE key = ? AND organization_uid IS NULL AND deleted_at IS NULL",
		key,
	).Scan(context.Background(), &value))

	return value
}

func bootSystemConfig(t *testing.T, svc db.Service) (*config.Config, error) {
	t.Helper()
	t.Setenv("SP_AUTH_JWT_SECRET", "")

	cfg := &config.Config{}

	return cfg, systemconfig.NewService(svc, cfg).Initialize(t.Context())
}

func testGeneratedJWTSecretIsSealed(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	cfg, err := bootSystemConfig(t, wrap(t, raw, key))
	require.NoError(t, err)
	require.NotEmpty(t, cfg.Auth.JWTSecret)

	stored := rawValue(t, raw, string(systemconfig.KeyJWTSecret))
	require.NotContains(t, stored, cfg.Auth.JWTSecret)
	require.Contains(t, stored, sysparamseal.SealedMarkerKey)

	// A second boot reads the same key back.
	again, err := bootSystemConfig(t, wrap(t, raw, key))
	require.NoError(t, err)
	require.Equal(t, cfg.Auth.JWTSecret, again.Auth.JWTSecret)
}

func testRemovedMasterKeyFailsBoot(t *testing.T, raw db.Service) {
	t.Helper()

	before := rawValue(t, raw, string(systemconfig.KeyJWTSecret))

	_, err := bootSystemConfig(t, wrap(t, raw, nil))
	require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable)
	require.ErrorContains(t, err, "SP_ENCRYPTION_MASTER_KEY")

	// No new JWT secret was generated over the sealed one.
	require.Equal(t, before, rawValue(t, raw, string(systemconfig.KeyJWTSecret)))
}

func testWrongMasterKeyFailsBoot(t *testing.T, raw db.Service) {
	t.Helper()

	before := rawValue(t, raw, string(systemconfig.KeyJWTSecret))

	_, err := bootSystemConfig(t, wrap(t, raw, newMasterKey(t)))
	require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable)
	require.ErrorContains(t, err, "SP_ENCRYPTION_MASTER_KEY")
	require.Equal(t, before, rawValue(t, raw, string(systemconfig.KeyJWTSecret)))
}

func testSecretParameterIsSealed(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	ctx := t.Context()
	svc := wrap(t, raw, key)

	const secretKey, plain = "auth.oidc.client_secret", "acme-oidc-client-secret"

	require.NoError(t, svc.SetSystemParameter(ctx, secretKey, plain, true))
	require.NotContains(t, rawValue(t, raw, secretKey), plain)

	param, err := svc.GetSystemParameter(ctx, secretKey)
	require.NoError(t, err)
	require.Equal(t, plain, param.Value["value"])

	listed, err := svc.ListSystemParameters(ctx)
	require.NoError(t, err)

	found := false

	for _, p := range listed {
		if p.Key == secretKey {
			found = true

			require.Equal(t, plain, p.Value["value"])
		}
	}

	require.True(t, found)

	// A known secret key is sealed even when a caller passes secret=false.
	require.NoError(t, svc.SetSystemParameter(ctx, secretKey, plain, false))
	require.NotContains(t, rawValue(t, raw, secretKey), plain)

	// GetOrCreate seals the created value too.
	const telegramKey = "test.getorcreate.secret"

	created, isNew, err := svc.GetOrCreateSystemParameter(ctx, telegramKey, "acme-webhook-secret", true)
	require.NoError(t, err)
	require.True(t, isNew)
	require.Equal(t, "acme-webhook-secret", created.Value["value"])
	require.NotContains(t, rawValue(t, raw, telegramKey), "acme-webhook-secret")
}

func testEmailInboxIsSealed(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	ctx := t.Context()
	svc := wrap(t, raw, key)

	const password = "acme-inbox-password"

	value := map[string]any{
		"enabled": true, "username": "inbox@acme.test", "password": password,
	}

	// Saved with secret=false, as the generic CRUD may: still sealed.
	require.NoError(t, svc.SetSystemParameter(ctx, jmap.SystemParameterKey, value, false))
	require.NotContains(t, rawValue(t, raw, jmap.SystemParameterKey), password)

	param, err := svc.GetSystemParameter(ctx, jmap.SystemParameterKey)
	require.NoError(t, err)

	cfg, err := jmap.JSONMapToConfig(param.Value)
	require.NoError(t, err)
	require.Equal(t, password, cfg.Password)
	require.Equal(t, "inbox@acme.test", cfg.Username)
}

func testNonSecretParameterStaysPlain(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	ctx := t.Context()
	svc := wrap(t, raw, key)

	paramKey := string(systemconfig.KeyLocalLoginEnabled)

	require.NoError(t, svc.SetSystemParameter(ctx, paramKey, true, false))

	var stored map[string]any
	require.NoError(t, json.Unmarshal([]byte(rawValue(t, raw, paramKey)), &stored))
	require.Equal(t, map[string]any{"value": true}, stored)

	param, err := svc.GetSystemParameter(ctx, paramKey)
	require.NoError(t, err)
	require.Equal(t, true, param.Value["value"])
}

func testReservedDEKKey(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	ctx := t.Context()
	svc := wrap(t, raw, key)

	require.ErrorIs(t,
		svc.SetSystemParameter(ctx, credentials.SystemDEKParameterKey, "x", true), sysparamseal.ErrReservedKey)
	require.ErrorIs(t, svc.DeleteSystemParameter(ctx, credentials.SystemDEKParameterKey), sysparamseal.ErrReservedKey)
}

func testMigrationSealsPlaintextJWT(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	ctx := t.Context()

	// A pre-existing plaintext row, as an install without a master key wrote it.
	const legacySecret = "acme-legacy-plaintext-jwt-signing-key-0123456789"

	require.NoError(t, raw.SetSystemParameter(ctx, string(systemconfig.KeyJWTSecret), legacySecret, true))

	// The transition still reads it before the sweep.
	before, err := bootSystemConfig(t, wrap(t, raw, key))
	require.NoError(t, err)
	require.Equal(t, legacySecret, before.Auth.JWTSecret)

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "acme-user"}).
		SignedString([]byte(before.Auth.JWTSecret))
	require.NoError(t, err)

	svc := wrap(t, raw, key)

	keys, err := svc.PlaintextSecretKeys(ctx)
	require.NoError(t, err)
	require.Contains(t, keys, string(systemconfig.KeyJWTSecret))

	sealed, err := svc.SealPlaintextRows(ctx)
	require.NoError(t, err)
	require.Positive(t, sealed)
	require.NotContains(t, rawValue(t, raw, string(systemconfig.KeyJWTSecret)), legacySecret)

	// Idempotent.
	again, err := svc.SealPlaintextRows(ctx)
	require.NoError(t, err)
	require.Zero(t, again)

	keys, err = svc.PlaintextSecretKeys(ctx)
	require.NoError(t, err)
	require.Empty(t, keys)

	// After the restart, the token signed before still validates.
	after, err := bootSystemConfig(t, wrap(t, raw, key))
	require.NoError(t, err)

	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return []byte(after.Auth.JWTSecret), nil },
		jwt.WithValidMethods([]string{"HS256"}))
	require.NoError(t, err)
	require.True(t, parsed.Valid)
}

func TestShouldSealParameter(t *testing.T) {
	t.Parallel()

	require.Equal(t, jmap.SystemParameterKey, string(systemconfig.KeyEmailInbox))
	require.True(t, systemconfig.ShouldSealParameter(jmap.SystemParameterKey, false))
	require.True(t, systemconfig.ShouldSealParameter(string(systemconfig.KeyJWTSecret), false))
	require.True(t, systemconfig.ShouldSealParameter("auth.oidc.client_secret", false))
	require.True(t, systemconfig.ShouldSealParameter("custom.anything", true))
	require.False(t, systemconfig.ShouldSealParameter(string(systemconfig.KeyLocalLoginEnabled), false))
}

// testMigrationSealsAppSettings covers spec 2026-11-26-06: the plaintext SAML
// SP and VAPID private keys are sealed at startup, read back byte-identical,
// and the public halves stay as they were.
func testMigrationSealsAppSettings(t *testing.T, raw db.Service, key []byte) {
	t.Helper()

	ctx := t.Context()

	const (
		spCertKey   = "saml.sp_certificate_pem"
		vapidPubKey = "webpush.vapid_public_key"
		spPEM       = "-----BEGIN RSA PRIVATE KEY-----\nacme-legacy-sp-key\n-----END RSA PRIVATE KEY-----\n"
		spCert      = "-----BEGIN CERTIFICATE-----\nacme-legacy-cert\n-----END CERTIFICATE-----\n"
		vapidPriv   = "acme-legacy-vapid-private-key-base64url"
		vapidPub    = "acme-legacy-vapid-public-key-base64url"
	)

	legacy := map[string]string{
		sysparamseal.AppSettingSAMLSPPrivateKey: spPEM,
		spCertKey:                               spCert,
		sysparamseal.AppSettingVAPIDPrivateKey:  vapidPriv,
		vapidPubKey:                             vapidPub,
	}
	require.NoError(t, raw.SetAppSettings(ctx, legacy))

	allKeys := []string{
		sysparamseal.AppSettingSAMLSPPrivateKey, spCertKey, sysparamseal.AppSettingVAPIDPrivateKey, vapidPubKey,
	}
	svc := wrap(t, raw, key)

	// The transition still reads the plaintext rows.
	before, err := svc.GetAppSettings(ctx, allKeys)
	require.NoError(t, err)
	require.Equal(t, legacy, before)

	plain, err := svc.PlaintextAppSettingKeys(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{sysparamseal.AppSettingSAMLSPPrivateKey, sysparamseal.AppSettingVAPIDPrivateKey}, plain)

	sealed, err := svc.SealPlaintextAppSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, sealed)

	stored, err := raw.GetAppSettings(ctx, allKeys)
	require.NoError(t, err)
	require.NotContains(t, stored[sysparamseal.AppSettingSAMLSPPrivateKey], "acme-legacy-sp-key")
	require.NotContains(t, stored[sysparamseal.AppSettingVAPIDPrivateKey], vapidPriv)
	require.Contains(t, stored[sysparamseal.AppSettingVAPIDPrivateKey], sysparamseal.SealedMarkerKey)
	require.Equal(t, spCert, stored[spCertKey])
	require.Equal(t, vapidPub, stored[vapidPubKey])

	// Idempotent.
	again, err := svc.SealPlaintextAppSettings(ctx)
	require.NoError(t, err)
	require.Zero(t, again)

	plain, err = svc.PlaintextAppSettingKeys(ctx)
	require.NoError(t, err)
	require.Empty(t, plain)

	// After the restart, the keys read back byte-identical.
	after, err := wrap(t, raw, key).GetAppSettings(ctx, allKeys)
	require.NoError(t, err)
	require.Equal(t, legacy, after)

	single, err := wrap(t, raw, key).GetAppSetting(ctx, sysparamseal.AppSettingSAMLSPPrivateKey)
	require.NoError(t, err)
	require.Equal(t, spPEM, single)

	// A wrong master key or none fails the read, never "missing".
	for name, other := range map[string][]byte{"wrong key": newMasterKey(t), "no key": nil} {
		_, err = wrap(t, raw, other).GetAppSettings(ctx, allKeys)
		require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable, name)

		_, err = wrap(t, raw, other).GetAppSetting(ctx, sysparamseal.AppSettingVAPIDPrivateKey)
		require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable, name)

		// The non-secret halves still read.
		cert, certErr := wrap(t, raw, other).GetAppSetting(ctx, spCertKey)
		require.NoError(t, certErr, name)
		require.Equal(t, spCert, cert)
	}

	// Without a master key, writes stay plaintext and the sweep is a no-op.
	noKey := wrap(t, raw, nil)
	require.NoError(t, noKey.SetAppSetting(ctx, sysparamseal.AppSettingVAPIDPrivateKey, vapidPriv))
	storedPlain, err := raw.GetAppSetting(ctx, sysparamseal.AppSettingVAPIDPrivateKey)
	require.NoError(t, err)
	require.Equal(t, vapidPriv, storedPlain)

	count, err := noKey.SealPlaintextAppSettings(ctx)
	require.NoError(t, err)
	require.Zero(t, count)

	noKeyPlain, err := noKey.PlaintextAppSettingKeys(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{sysparamseal.AppSettingVAPIDPrivateKey}, noKeyPlain)
}
