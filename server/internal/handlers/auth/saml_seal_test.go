package auth

import (
	"crypto/rand"
	"encoding/xml"
	"testing"

	"github.com/crewjam/saml"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
)

// Spec 2026-11-26-06: the SAML SP private key is sealed in app_settings when
// a master key is set; the SP certificate stays plaintext.

func samlSealRawDB(t *testing.T) *sqlite.Service {
	t.Helper()

	raw, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, raw.Initialize(t.Context()))
	t.Cleanup(func() { _ = raw.Close() })

	return raw
}

func samlSealMasterKey(t *testing.T) []byte {
	t.Helper()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	return key
}

func samlSealService(t *testing.T, raw db.Service, masterKey []byte) *SAMLService {
	t.Helper()

	sealer, err := credentials.NewSystemSealer(masterKey, raw)
	require.NoError(t, err)

	dbService := sysparamseal.Wrap(raw, sealer, nil)
	cfg := &config.Config{
		SAML:   config.SAMLConfig{Enabled: true},
		Auth:   config.AuthConfig{JWTSecret: "test-jwt-secret"},
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
	}

	return NewSAMLService(dbService, cfg, NewService(dbService, cfg.Auth, cfg, nil, nil))
}

func requireSAMLMetadata(t *testing.T, svc *SAMLService) {
	t.Helper()

	data, err := svc.Metadata(t.Context())
	require.NoError(t, err)

	var descriptor saml.EntityDescriptor
	require.NoError(t, xml.Unmarshal(data, &descriptor))
	require.Len(t, descriptor.SPSSODescriptors, 1)
}

func TestSAMLSPPrivateKeySealedWithMasterKey(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	raw := samlSealRawDB(t)
	key := samlSealMasterKey(t)

	svc := samlSealService(t, raw, key)
	requireSAMLMetadata(t, svc)

	spKey, spCert, err := svc.spKeyPair(ctx)
	require.NoError(t, err)

	storedKey, err := raw.GetAppSetting(ctx, appSettingKeySPPrivateKey)
	require.NoError(t, err)
	require.NotContains(t, storedKey, "PRIVATE KEY")
	_, isSealed := sysparamseal.SealedAppSettingEnvelope(storedKey)
	require.True(t, isSealed)

	storedCert, err := raw.GetAppSetting(ctx, appSettingKeySPCertificate)
	require.NoError(t, err)
	require.Contains(t, storedCert, "CERTIFICATE")

	// A restart (fresh in-process cache) with the same master key loads the
	// same pair.
	again := samlSealService(t, raw, key)
	againKey, againCert, err := again.spKeyPair(ctx)
	require.NoError(t, err)
	require.True(t, spKey.Equal(againKey))
	require.True(t, spCert.Equal(againCert))
}

func TestSAMLSealedSPKeyUnreadableDoesNotRegenerate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	raw := samlSealRawDB(t)

	_, _, err := samlSealService(t, raw, samlSealMasterKey(t)).spKeyPair(ctx)
	require.NoError(t, err)

	keys := []string{appSettingKeySPPrivateKey, appSettingKeySPCertificate}
	before, err := raw.GetAppSettings(ctx, keys)
	require.NoError(t, err)

	for name, masterKey := range map[string][]byte{"wrong key": samlSealMasterKey(t), "no key": nil} {
		_, _, err = samlSealService(t, raw, masterKey).spKeyPair(ctx)
		require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable, name)
	}

	after, err := raw.GetAppSettings(ctx, keys)
	require.NoError(t, err)
	require.Equal(t, before, after, "no new SP key pair is generated over the sealed one")
}

func TestSAMLSPKeyPlaintextWithoutMasterKey(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	raw := samlSealRawDB(t)

	svc := samlSealService(t, raw, nil)
	requireSAMLMetadata(t, svc)

	spKey, _, err := svc.spKeyPair(ctx)
	require.NoError(t, err)

	storedKey, err := raw.GetAppSetting(ctx, appSettingKeySPPrivateKey)
	require.NoError(t, err)
	require.Contains(t, storedKey, "PRIVATE KEY")

	againKey, _, err := samlSealService(t, raw, nil).spKeyPair(ctx)
	require.NoError(t, err)
	require.True(t, spKey.Equal(againKey))
}
