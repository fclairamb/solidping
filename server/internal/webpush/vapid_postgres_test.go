package webpush_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// Distinct from every other embedded port (157xx is taken by other packages;
// this one and the next use 15800-15801).
const portVAPIDPostgres = 15800

// TestVAPIDKeysPostgres covers the unmigrated, repeat and concurrent cases on
// a real Postgres. Self-skips under -short.
//
//nolint:paralleltest // shares embedded-postgres resources with sibling tests
func TestVAPIDKeysPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portVAPIDPostgres, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	// Unmigrated: error, no keys.
	pub, priv, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
	r.Error(err)
	r.Empty(pub)
	r.Empty(priv)

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	// Repeat calls with fresh configs return the same pair.
	pub1, priv1, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
	r.NoError(err)
	pub2, priv2, err := webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
	r.NoError(err)
	r.Equal(pub1, pub2)
	r.Equal(priv1, priv2)
}

//nolint:paralleltest // see above
func TestVAPIDKeysPostgresConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portVAPIDPostgres + 1, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	const workers = 8

	pubs := make([]string, workers)
	errs := make([]error, workers)

	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			pubs[i], _, errs[i] = webpush.GetOrCreateVAPIDKeys(ctx, webpush.Config{}, svc)
		}()
	}

	wg.Wait()

	stored, err := svc.GetAppSetting(ctx, "webpush.vapid_public_key")
	r.NoError(err)

	for i := range workers {
		r.NoError(errs[i])
		r.Equal(stored, pubs[i])
	}
}
