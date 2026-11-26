package webpush_test

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// Spec 2026-11-26-06: the VAPID private key is sealed in app_settings when a
// master key is set.

func sealMasterKey(t *testing.T) []byte {
	t.Helper()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	return key
}

func sealedDB(t *testing.T, raw db.Service, masterKey []byte) db.Service {
	t.Helper()

	sealer, err := credentials.NewSystemSealer(masterKey, raw)
	require.NoError(t, err)

	return sysparamseal.Wrap(raw, sealer, nil)
}

func sendWith(t *testing.T, pub, priv string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	opts := webpush.Options{VAPIDPublicKey: pub, VAPIDPrivateKey: priv, Subject: "mailto:ops@acme.test"}
	require.NoError(t, webpush.Send(t.Context(), opts, fakeSubscription(t, srv.URL), webpush.Message{Title: "t"}))
}

func TestVAPIDPrivateKeySealedWithMasterKey(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	raw := newTestDB(t)
	key := sealMasterKey(t)

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, sealedDB(t, raw, key))
	require.NoError(t, err)

	// The raw row holds no key material; the public key stays plaintext.
	storedPriv, err := raw.GetAppSetting(ctx, testKeyPrivate)
	require.NoError(t, err)
	require.NotContains(t, storedPriv, priv)
	_, isSealed := sysparamseal.SealedAppSettingEnvelope(storedPriv)
	require.True(t, isSealed)

	storedPub, err := raw.GetAppSetting(ctx, testKeyPublic)
	require.NoError(t, err)
	require.Equal(t, pub, storedPub)

	// A restart with the same master key reads the same pair back.
	pub2, priv2, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, sealedDB(t, raw, key))
	require.NoError(t, err)
	require.Equal(t, pub, pub2)
	require.Equal(t, priv, priv2)

	sendWith(t, pub2, priv2)
}

func TestVAPIDSealedKeyUnreadableDoesNotRegenerate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	raw := newTestDB(t)

	_, _, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, sealedDB(t, raw, sealMasterKey(t)))
	require.NoError(t, err)

	before, err := raw.GetAppSettings(ctx, []string{testKeyPublic, testKeyPrivate})
	require.NoError(t, err)

	for name, masterKey := range map[string][]byte{"wrong key": sealMasterKey(t), "no key": nil} {
		_, _, err = webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, sealedDB(t, raw, masterKey))
		require.ErrorIs(t, err, sysparamseal.ErrSealedUnavailable, name)
	}

	after, err := raw.GetAppSettings(ctx, []string{testKeyPublic, testKeyPrivate})
	require.NoError(t, err)
	require.Equal(t, before, after, "no new key pair is generated over the sealed one")
}

func TestVAPIDPlaintextWithoutMasterKey(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	raw := newTestDB(t)

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, sealedDB(t, raw, nil))
	require.NoError(t, err)

	storedPriv, err := raw.GetAppSetting(ctx, testKeyPrivate)
	require.NoError(t, err)
	require.Equal(t, priv, storedPriv)

	pub2, priv2, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, sealedDB(t, raw, nil))
	require.NoError(t, err)
	require.Equal(t, pub, pub2)
	require.Equal(t, priv, priv2)

	sendWith(t, pub2, priv2)
}
