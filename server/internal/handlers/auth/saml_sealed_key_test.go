package auth

import (
	"crypto/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

func newSealedSAMLService(t *testing.T, raw db.Service, masterKey []byte) *SAMLService {
	t.Helper()

	sealer, err := credentials.NewSystemSealer(masterKey, raw)
	require.NoError(t, err)

	cfg := &config.Config{
		SAML:   config.SAMLConfig{Enabled: true},
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
	}

	return NewSAMLService(sysparamseal.Wrap(raw, sealer, systemconfig.ShouldSealParameter), cfg, nil)
}

func newSAMLMasterKey(t *testing.T) []byte {
	t.Helper()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	return key
}

// TestSAMLSPPrivateKeySealed covers spec 2026-11-26-06 for the SAML SP key:
// sealed at rest with a master key, never regenerated over a sealed row that
// cannot be opened, plaintext (with working metadata) without a master key.
func TestSAMLSPPrivateKeySealed(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	newRaw := func(t *testing.T) db.Service {
		t.Helper()

		raw, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
		require.NoError(t, err)
		require.NoError(t, raw.Initialize(ctx))
		t.Cleanup(func() { _ = raw.Close() })

		return raw
	}

	t.Run("sealed with a master key", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		raw := newRaw(t)
		masterKey := newSAMLMasterKey(t)

		key, cert, err := newSealedSAMLService(t, raw, masterKey).spKeyPair(ctx)
		r.NoError(err)

		storedKey, err := raw.GetAppSetting(ctx, appSettingKeySPPrivateKey)
		r.NoError(err)
		r.NotContains(storedKey, "PRIVATE KEY", "the raw row must not hold the PEM")

		_, sealed := sysparamseal.SealedAppSettingEnvelope(storedKey)
		r.True(sealed)

		storedCert, err := raw.GetAppSetting(ctx, appSettingKeySPCertificate)
		r.NoError(err)
		r.True(strings.HasPrefix(storedCert, "-----BEGIN CERTIFICATE-----"), "the certificate stays plaintext")

		// A restart with the same key reads the same pair back.
		key2, cert2, err := newSealedSAMLService(t, raw, masterKey).spKeyPair(ctx)
		r.NoError(err)
		r.True(key.Equal(key2))
		r.True(cert.Equal(cert2))

		// A wrong or missing master key fails, and nothing is regenerated.
		for name, other := range map[string][]byte{"wrong key": newSAMLMasterKey(t), "no key": nil} {
			_, _, err = newSealedSAMLService(t, raw, other).spKeyPair(ctx)
			r.ErrorIs(err, sysparamseal.ErrSealedUnavailable, name)

			after, getErr := raw.GetAppSetting(ctx, appSettingKeySPPrivateKey)
			r.NoError(getErr)
			r.Equal(storedKey, after, name)

			afterCert, getErr := raw.GetAppSetting(ctx, appSettingKeySPCertificate)
			r.NoError(getErr)
			r.Equal(storedCert, afterCert, name)
		}
	})

	t.Run("plaintext without a master key", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		raw := newRaw(t)
		svc := newSealedSAMLService(t, raw, nil)

		_, _, err := svc.spKeyPair(ctx)
		r.NoError(err)

		storedKey, err := raw.GetAppSetting(ctx, appSettingKeySPPrivateKey)
		r.NoError(err)
		r.Contains(storedKey, "PRIVATE KEY")

		metadata, err := svc.Metadata(ctx)
		r.NoError(err)
		r.Contains(string(metadata), "X509Certificate")
	})
}
