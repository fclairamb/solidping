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

const (
	testKeyPublic  = "webpush.vapid_public_key"
	testKeyPrivate = "webpush.vapid_private_key"
)

// errStoreBroken is the failure the stub store below injects.
var errStoreBroken = errors.New("store broken")

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
	store := newTestDB(t)

	cfg := webpush.Config{} // no pre-set keys

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, cfg, store)
	require.NoError(t, err)
	require.NotEmpty(t, pub)
	require.NotEmpty(t, priv)

	// Verify persisted.
	storedPub, err := store.GetAppSetting(ctx, "webpush.vapid_public_key")
	require.NoError(t, err)
	require.Equal(t, pub, storedPub)

	storedPriv, err := store.GetAppSetting(ctx, "webpush.vapid_private_key")
	require.NoError(t, err)
	require.Equal(t, priv, storedPriv)
}

func TestGetOrCreateVAPIDKeys_Idempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestDB(t)

	cfg := webpush.Config{}

	pub1, priv1, err := webpush.GetOrCreateVAPIDKeys(ctx, cfg, store)
	require.NoError(t, err)

	pub2, priv2, err := webpush.GetOrCreateVAPIDKeys(ctx, cfg, store)
	require.NoError(t, err)

	require.Equal(t, pub1, pub2, "second call should return same public key")
	require.Equal(t, priv1, priv2, "second call should return same private key")
}

func TestGetOrCreateVAPIDKeys_UsesConfigValues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestDB(t)

	// Pre-generate a real keypair to use as config values.
	pubCfg, privCfg := realVAPIDKeys(t)

	cfg := webpush.Config{
		VAPIDPublicKey:  pubCfg,
		VAPIDPrivateKey: privCfg,
	}

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, cfg, store)
	require.NoError(t, err)
	require.Equal(t, pubCfg, pub)
	require.Equal(t, privCfg, priv)
}

// unmigratedDB is a SQLite database with no tables at all: what NewServer
// used to hand GetOrCreateVAPIDKeys before the migrations ran.
func unmigratedDB(t *testing.T) *sqlite.Service {
	t.Helper()

	svc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)

	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

// failingCreateStore reads as empty and fails every write.
type failingCreateStore struct {
	db.Service
}

func (failingCreateStore) GetAppSettings(_ context.Context, _ []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (failingCreateStore) CreateAppSettingsIfAbsent(_ context.Context, _ map[string]string) error {
	return errStoreBroken
}

func TestGetOrCreateVAPIDKeys_UnmigratedDatabase(t *testing.T) {
	t.Parallel()

	pubCfg, privCfg := realVAPIDKeys(t)

	cases := []struct {
		name    string
		cfg     webpush.Config
		wantErr bool
	}{
		{name: "no env keys fails instead of serving an unstored key", cfg: webpush.Config{}, wantErr: true},
		{
			name: "env keys need no database",
			cfg:  webpush.Config{VAPIDPublicKey: pubCfg, VAPIDPrivateKey: privCfg},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			pub, priv, err := webpush.GetOrCreateVAPIDKeys(t.Context(), tc.cfg, unmigratedDB(t))
			if tc.wantErr {
				r.Error(err)
				r.Empty(pub)
				r.Empty(priv)

				return
			}

			r.NoError(err)
			r.Equal(pubCfg, pub)
			r.Equal(privCfg, priv)
		})
	}
}

func TestGetOrCreateVAPIDKeys_PersistFailure(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	pub, priv, err := webpush.GetOrCreateVAPIDKeys(t.Context(), webpush.Config{}, failingCreateStore{})
	r.ErrorIs(err, errStoreBroken)
	r.Empty(pub)
	r.Empty(priv)
}

func TestGetOrCreateVAPIDKeys_PartialState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		stored string
	}{
		{name: "public key only", stored: testKeyPublic},
		{name: "private key only", stored: testKeyPrivate},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := t.Context()
			store := newTestDB(t)

			r.NoError(store.SetAppSetting(ctx, tc.stored, "orphan-half"))

			pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, store)
			r.NoError(err)
			r.NotEqual("orphan-half", pub)
			r.NotEqual("orphan-half", priv)

			storedPub, err := store.GetAppSetting(ctx, testKeyPublic)
			r.NoError(err)
			r.Equal(pub, storedPub)

			storedPriv, err := store.GetAppSetting(ctx, testKeyPrivate)
			r.NoError(err)
			r.Equal(priv, storedPriv)
		})
	}
}

// TestGetOrCreateVAPIDKeys_ConcurrentFirstCreation opens one connection pool
// per goroutine on the same file, like several processes booting together on
// a fresh database: they must all end up with the one stored pair.
func TestGetOrCreateVAPIDKeys_ConcurrentFirstCreation(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()

	const processes = 8

	stores := make([]*sqlite.Service, 0, processes)

	for i := range processes {
		store, err := sqlite.New(ctx, sqlite.Config{DataDir: dir})
		r.NoError(err)
		t.Cleanup(func() { _ = store.Close() })

		if i == 0 {
			r.NoError(store.Initialize(ctx))
		}

		stores = append(stores, store)
	}

	assertConcurrentCreationConverges(t, stores[0], toStores(stores))
}

func toStores[S db.Service](in []S) []db.Service {
	out := make([]db.Service, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}

	return out
}

// assertConcurrentCreationConverges runs GetOrCreateVAPIDKeys once per store,
// all at the same time, and checks every call returned the pair that ended up
// stored. Shared with the Postgres variant.
func assertConcurrentCreationConverges(t *testing.T, reader db.Service, stores []db.Service) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	type pair struct{ pub, priv string }

	results := make([]pair, len(stores))
	errs := make([]error, len(stores))

	var wg sync.WaitGroup

	for i, store := range stores {
		wg.Go(func() {
			pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, store)
			results[i] = pair{pub: pub, priv: priv}
			errs[i] = err
		})
	}

	wg.Wait()

	storedPub, err := reader.GetAppSetting(ctx, testKeyPublic)
	r.NoError(err)

	storedPriv, err := reader.GetAppSetting(ctx, testKeyPrivate)
	r.NoError(err)

	for i := range stores {
		r.NoError(errs[i], "process %d", i)
		r.Equal(pair{pub: storedPub, priv: storedPriv}, results[i], "process %d", i)
	}
}
