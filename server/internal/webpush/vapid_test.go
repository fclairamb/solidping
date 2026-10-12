package webpush_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
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

func TestGetOrCreateVAPIDKeys_UnmigratedDatabaseErrors(t *testing.T) {
	t.Parallel()

	svc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(t.Context(), webpush.Config{}, svc)
	require.Error(t, err)
	require.Empty(t, pub)
	require.Empty(t, priv)
}

func TestGetOrCreateVAPIDKeys_EnvKeysNeedNoDatabase(t *testing.T) {
	t.Parallel()

	svc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(t.Context(),
		webpush.Config{VAPIDPublicKey: "p", VAPIDPrivateKey: "q"}, svc)
	require.NoError(t, err)
	require.Equal(t, "p", pub)
	require.Equal(t, "q", priv)
}

var errBoom = errors.New("boom")

type failingCreateDB struct {
	db.Service
}

func (failingCreateDB) CreateAppSettingsIfAbsent(context.Context, map[string]string) error {
	return errBoom
}

func TestGetOrCreateVAPIDKeys_PersistFailureReturnsNoKeys(t *testing.T) {
	t.Parallel()

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(t.Context(), webpush.Config{},
		failingCreateDB{Service: newTestDB(t)})
	require.Error(t, err)
	require.Empty(t, pub)
	require.Empty(t, priv)
}

func TestGetOrCreateVAPIDKeys_PartialStateIsRepaired(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc := newTestDB(t)
	require.NoError(t, svc.SetAppSetting(ctx, "webpush.vapid_public_key", "orphan"))

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
	require.NoError(t, err)
	require.NotEmpty(t, pub)
	require.NotEmpty(t, priv)

	storedPub, err := svc.GetAppSetting(ctx, "webpush.vapid_public_key")
	require.NoError(t, err)
	storedPriv, err := svc.GetAppSetting(ctx, "webpush.vapid_private_key")
	require.NoError(t, err)
	require.Equal(t, pub, storedPub)
	require.Equal(t, priv, storedPriv)
}

func TestGetOrCreateVAPIDKeys_ConcurrentFirstCreation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, err := sqlite.New(ctx, sqlite.Config{DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(ctx))

	const workers = 8

	type pair struct {
		pub, priv string
		err       error
	}

	results := make([]pair, workers)

	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			pub, priv, gerr := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
			results[i] = pair{pub, priv, gerr}
		}()
	}

	wg.Wait()

	storedPub, err := svc.GetAppSetting(ctx, "webpush.vapid_public_key")
	require.NoError(t, err)
	storedPriv, err := svc.GetAppSetting(ctx, "webpush.vapid_private_key")
	require.NoError(t, err)

	for _, res := range results {
		require.NoError(t, res.err)
		require.Equal(t, storedPub, res.pub)
		require.Equal(t, storedPriv, res.priv)
	}
}
