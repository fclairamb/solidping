package webpush_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

func newTestDB(t *testing.T) *sqlite.Service {
	t.Helper()

	svc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)

	err = svc.Initialize(t.Context())
	require.NoError(t, err)

	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

func TestGetOrCreateVAPIDKeys_GeneratesAndPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newTestDB(t)

	cfg := webpush.Config{} // no pre-set keys

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, cfg, db)
	require.NoError(t, err)
	require.NotEmpty(t, pub)
	require.NotEmpty(t, priv)

	// Verify persisted.
	storedPub, err := db.GetAppSetting(ctx, "webpush.vapid_public_key")
	require.NoError(t, err)
	require.Equal(t, pub, storedPub)

	storedPriv, err := db.GetAppSetting(ctx, "webpush.vapid_private_key")
	require.NoError(t, err)
	require.Equal(t, priv, storedPriv)
}

func TestGetOrCreateVAPIDKeys_Idempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newTestDB(t)

	cfg := webpush.Config{}

	pub1, priv1, err := webpush.GetOrCreateVAPIDKeys(ctx, cfg, db)
	require.NoError(t, err)

	pub2, priv2, err := webpush.GetOrCreateVAPIDKeys(ctx, cfg, db)
	require.NoError(t, err)

	require.Equal(t, pub1, pub2, "second call should return same public key")
	require.Equal(t, priv1, priv2, "second call should return same private key")
}

func TestGetOrCreateVAPIDKeys_UsesConfigValues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newTestDB(t)

	// Pre-generate a real keypair to use as config values.
	pubCfg, privCfg := realVAPIDKeys(t)

	cfg := webpush.Config{
		VAPIDPublicKey:  pubCfg,
		VAPIDPrivateKey: privCfg,
	}

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, cfg, db)
	require.NoError(t, err)
	require.Equal(t, pubCfg, pub)
	require.Equal(t, privCfg, priv)
}
