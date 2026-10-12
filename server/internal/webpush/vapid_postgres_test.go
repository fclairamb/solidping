package webpush_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// Distinct from every other embedded port claimed in the repo (15715 was the
// highest in use).
const portVAPIDPG = 15733

// TestGetOrCreateVAPIDKeysPostgres pins the VAPID key lifecycle on the real
// engine: an unmigrated database is an error (never an unstored key), a
// restart reads the stored pair back, and concurrent first creation converges
// on one stored pair. Concurrent goroutines draw separate pool connections, so
// they race the way separate processes do.
//
// Self-skips under -short and on any embedded-startup error, like its siblings.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestGetOrCreateVAPIDKeysPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	store, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portVAPIDPG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Before the migrations: no app_settings table, so no key at all.
	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, store)
	r.Error(err)
	r.Empty(pub)
	r.Empty(priv)

	if initErr := store.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	const processes = 8

	stores := make([]db.Service, 0, processes)
	for range processes {
		stores = append(stores, store)
	}

	assertConcurrentCreationConverges(t, store, stores)

	// A restart (fresh config, nothing from the environment) reads the
	// stored pair back.
	first, firstPriv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, store)
	r.NoError(err)

	second, secondPriv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, store)
	r.NoError(err)
	r.Equal(first, second)
	r.Equal(firstPriv, secondPriv)
}
