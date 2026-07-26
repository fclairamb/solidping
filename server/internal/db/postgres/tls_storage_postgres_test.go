package postgres

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// portTLSStorage is distinct from every other _postgres_test.go embedded port
// in this package (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portTLSStorage = 15463

// TestTLSStorage_Postgres proves the certmagic-backing key-value store behaves
// identically on a REAL Postgres backend: byte-exact binary round-trips through
// bytea, key-range prefix listing and subtree delete, and the conditional
// lock upsert under genuine concurrency (the property SQLite's single-writer
// tests cannot prove). Self-skips under -short and on any embedded-startup
// error.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestTLSStorage_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{Embedded: true, Port: portTLSStorage, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	// The embedded server runs with max_connections=10 (3 of them reserved for
	// superusers) and NewEmbedded does not apply pool limits, so bound the pool
	// here: the lock race below wants real concurrent writers, not 16 raw
	// connections and a "too many clients" failure that proves nothing.
	s.DB().SetMaxOpenConns(4)

	const key = "certificates/acme-v02/status.acme.com/status.acme.com.key"

	_, err = s.TLSStorageLoad(ctx, key)
	r.ErrorIs(err, sql.ErrNoRows)

	// A PEM-ish payload with embedded NUL and high bytes: bytea must not mangle
	// it (a text column would).
	payload := []byte{0x00, 0x01, 0xFF, 'k', 'e', 'y', 0x00}
	r.NoError(s.TLSStorageStore(ctx, key, payload))

	loaded, err := s.TLSStorageLoad(ctx, key)
	r.NoError(err)
	r.Equal(payload, loaded)

	info, err := s.TLSStorageStat(ctx, key)
	r.NoError(err)
	r.Equal(int64(len(payload)), info.Size)
	r.WithinDuration(time.Now(), info.ModifiedAt, time.Minute)

	exists, err := s.TLSStorageExists(ctx, key)
	r.NoError(err)
	r.True(exists)

	// Prefix listing and subtree delete.
	siblings := []string{
		"certificates/acme-v02/status.acme.com/status.acme.com.crt",
		"certificates/acme-v02/other.acme.com/other.acme.com.crt",
		"certificates-backup/acme-v02/status.acme.com/status.acme.com.crt",
	}
	for _, sibling := range siblings {
		r.NoError(s.TLSStorageStore(ctx, sibling, []byte(sibling)))
	}

	under, err := s.TLSStorageList(ctx, "certificates")
	r.NoError(err)
	r.Len(under, 3, "the certificates-backup neighbor must not be included")

	nested, err := s.TLSStorageList(ctx, "certificates/acme-v02/status.acme.com")
	r.NoError(err)
	r.Len(nested, 2)

	r.NoError(s.TLSStorageDelete(ctx, "certificates/acme-v02/status.acme.com"))

	under, err = s.TLSStorageList(ctx, "certificates")
	r.NoError(err)
	r.Len(under, 1)

	backupAlive, err := s.TLSStorageExists(ctx, "certificates-backup/acme-v02/status.acme.com/status.acme.com.crt")
	r.NoError(err)
	r.True(backupAlive)

	testTLSStorageLocksPostgres(ctx, t, s)
}

// testTLSStorageLocksPostgres exercises the Locker contract on real Postgres:
// exactly one winner under contention, owner-scoped refresh/release, and
// reclaim of an expired lease.
func testTLSStorageLocksPostgres(ctx context.Context, t *testing.T, s *Service) {
	t.Helper()
	r := require.New(t)

	const (
		key     = "issue_cert_status.acme.com"
		racers  = 16
		leaseIn = time.Minute
	)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		errs    []error
	)

	wg.Add(racers)

	for i := range racers {
		go func(idx int) {
			defer wg.Done()

			ok, err := s.TLSStorageAcquireLock(ctx, key, "owner-"+string(rune('a'+idx)), time.Now().Add(leaseIn))

			mu.Lock()
			defer mu.Unlock()

			// Assertions belong on the test goroutine (testifylint go-require),
			// so collect and check after the wait.
			if err != nil {
				errs = append(errs, err)

				return
			}

			if ok {
				winners++
			}
		}(i)
	}

	wg.Wait()
	r.Empty(errs, "concurrent lock attempts must not error")
	r.Equal(1, winners, "the conditional upsert must admit exactly one holder")

	// An expired lease is reclaimable; a live one is not.
	r.NoError(s.TLSStorageReleaseLock(ctx, key, "owner-a"))

	taken, err := s.TLSStorageAcquireLock(ctx, key, "owner-stale", time.Now().Add(-time.Second))
	r.NoError(err)

	if taken {
		reclaimed, reclaimErr := s.TLSStorageAcquireLock(ctx, key, "owner-fresh", time.Now().Add(leaseIn))
		r.NoError(reclaimErr)
		r.True(reclaimed, "an expired lease must be reclaimable")

		blocked, blockErr := s.TLSStorageAcquireLock(ctx, key, "owner-other", time.Now().Add(leaseIn))
		r.NoError(blockErr)
		r.False(blocked, "a live lease must block other owners")

		stillMine, refreshErr := s.TLSStorageRefreshLock(ctx, key, "owner-fresh", time.Now().Add(2*leaseIn))
		r.NoError(refreshErr)
		r.True(stillMine)

		notMine, refreshErr := s.TLSStorageRefreshLock(ctx, key, "owner-stale", time.Now().Add(2*leaseIn))
		r.NoError(refreshErr)
		r.False(notMine)
	}
}
