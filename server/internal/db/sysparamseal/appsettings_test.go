package sysparamseal_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

const (
	vapidPublicKey = "webpush.vapid_public_key"
	samlCertKey    = "saml.sp_certificate_pem"
	acmeKeyPEM     = "-----BEGIN RSA PRIVATE KEY-----\nacme-private-key-material\n-----END RSA PRIVATE KEY-----\n"
	acmeCertPEM    = "-----BEGIN CERTIFICATE-----\nacme-certificate\n-----END CERTIFICATE-----\n"
)

func newAppSettingsDB(t *testing.T) db.Service {
	t.Helper()

	raw, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, raw.Initialize(t.Context()))
	t.Cleanup(func() { _ = raw.Close() })

	return raw
}

func rawAppSetting(t *testing.T, raw db.Service, key string) string {
	t.Helper()

	value, err := raw.GetAppSetting(context.Background(), key)
	require.NoError(t, err)

	return value
}

// Regression: with a master key, the generated VAPID private key is sealed in
// the database row and the public key stays readable.
func TestVAPIDPrivateKeyIsSealed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	raw := newAppSettingsDB(t)
	svc := wrap(t, raw, newMasterKey(t))

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
	r.NoError(err)
	r.NotEmpty(priv)

	stored := rawAppSetting(t, raw, sysparamseal.AppSettingWebPushVAPIDPrivKey)
	r.NotContains(stored, priv, "the raw row must not hold the VAPID private key")
	_, sealed := sysparamseal.SealedAppSettingEnvelope(stored)
	r.True(sealed)
	r.Equal(pub, rawAppSetting(t, raw, vapidPublicKey), "the public key stays plaintext")

	// A restart reads the same pair back.
	pub2, priv2, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
	r.NoError(err)
	r.Equal(pub, pub2)
	r.Equal(priv, priv2)
}

// Regression: SetAppSetting seals the SAML SP private key, not its certificate.
func TestSAMLPrivateKeyIsSealed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	raw := newAppSettingsDB(t)
	svc := wrap(t, raw, newMasterKey(t))

	r.NoError(svc.SetAppSetting(ctx, sysparamseal.AppSettingSAMLSPPrivateKey, acmeKeyPEM))
	r.NoError(svc.SetAppSetting(ctx, samlCertKey, acmeCertPEM))

	stored := rawAppSetting(t, raw, sysparamseal.AppSettingSAMLSPPrivateKey)
	r.NotContains(stored, "PRIVATE KEY")
	r.NotContains(stored, "acme-private-key-material")
	r.Equal(acmeCertPEM, rawAppSetting(t, raw, samlCertKey))

	opened, err := svc.GetAppSetting(ctx, sysparamseal.AppSettingSAMLSPPrivateKey)
	r.NoError(err)
	r.Equal(acmeKeyPEM, opened)
}

// Migration: plaintext rows left by an older version are sealed at startup and
// read back byte-identical.
func TestSealPlaintextAppSettings(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	raw := newAppSettingsDB(t)

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, raw)
	r.NoError(err)
	r.NoError(raw.SetAppSetting(ctx, sysparamseal.AppSettingSAMLSPPrivateKey, acmeKeyPEM))
	r.NoError(raw.SetAppSetting(ctx, samlCertKey, acmeCertPEM))

	svc := wrap(t, raw, newMasterKey(t))

	keys, err := svc.PlaintextSecretAppSettingKeys(ctx)
	r.NoError(err)
	r.ElementsMatch(sysparamseal.SecretAppSettingKeys(), keys)

	sealed, err := svc.SealPlaintextAppSettings(ctx)
	r.NoError(err)
	r.Equal(2, sealed)

	again, err := svc.SealPlaintextAppSettings(ctx)
	r.NoError(err)
	r.Zero(again, "the pass is idempotent")

	keys, err = svc.PlaintextSecretAppSettingKeys(ctx)
	r.NoError(err)
	r.Empty(keys)

	r.NotContains(rawAppSetting(t, raw, sysparamseal.AppSettingWebPushVAPIDPrivKey), priv)
	r.NotContains(rawAppSetting(t, raw, sysparamseal.AppSettingSAMLSPPrivateKey), "PRIVATE KEY")
	r.Equal(pub, rawAppSetting(t, raw, vapidPublicKey))
	r.Equal(acmeCertPEM, rawAppSetting(t, raw, samlCertKey))

	pub2, priv2, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
	r.NoError(err)
	r.Equal(pub, pub2)
	r.Equal(priv, priv2)

	samlKey, err := svc.GetAppSetting(ctx, sysparamseal.AppSettingSAMLSPPrivateKey)
	r.NoError(err)
	r.Equal(acmeKeyPEM, samlKey)
}

// Failure path: a sealed row read with a wrong or missing master key fails,
// and no new key pair is written over it.
func TestSealedAppSettingWrongKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	raw := newAppSettingsDB(t)
	svc := wrap(t, raw, newMasterKey(t))

	_, _, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
	r.NoError(err)

	before := rawAppSetting(t, raw, sysparamseal.AppSettingWebPushVAPIDPrivKey)
	beforePub := rawAppSetting(t, raw, vapidPublicKey)

	for name, other := range map[string]*sysparamseal.Service{
		"wrong key": wrap(t, raw, newMasterKey(t)),
		"no key":    wrap(t, raw, nil),
	} {
		_, _, err = webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, other)
		r.ErrorIs(err, sysparamseal.ErrSealedUnavailable, name)

		_, err = other.GetAppSetting(ctx, sysparamseal.AppSettingWebPushVAPIDPrivKey)
		r.ErrorIs(err, sysparamseal.ErrSealedUnavailable, name)

		r.Equal(before, rawAppSetting(t, raw, sysparamseal.AppSettingWebPushVAPIDPrivKey), name)
		r.Equal(beforePub, rawAppSetting(t, raw, vapidPublicKey), name)
	}
}

// Positive control: without a master key both keys round-trip in plaintext.
func TestAppSettingsPlaintextWithoutKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	raw := newAppSettingsDB(t)
	svc := wrap(t, raw, nil)

	_, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
	r.NoError(err)
	r.Equal(priv, rawAppSetting(t, raw, sysparamseal.AppSettingWebPushVAPIDPrivKey))

	r.NoError(svc.SetAppSettings(ctx, map[string]string{sysparamseal.AppSettingSAMLSPPrivateKey: acmeKeyPEM}))
	r.Equal(acmeKeyPEM, rawAppSetting(t, raw, sysparamseal.AppSettingSAMLSPPrivateKey))

	sealed, err := svc.SealPlaintextAppSettings(ctx)
	r.NoError(err)
	r.Zero(sealed)

	keys, err := svc.PlaintextSecretAppSettingKeys(ctx)
	r.NoError(err)
	r.ElementsMatch(sysparamseal.SecretAppSettingKeys(), keys)
}

func TestSealedAppSettingEnvelope(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]bool{
		"":                          false,
		acmeKeyPEM:                  false,
		"BGx-base64url-key":         false,
		"{not json":                 false,
		`{"other":"x"}`:             false,
		`{"$sp_sealed":""}`:         false,
		`{"$sp_sealed":"envelope"}`: true,
	} {
		_, got := sysparamseal.SealedAppSettingEnvelope(value)
		require.Equal(t, want, got, value)
	}
}
