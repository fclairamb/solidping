package sqlite

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTLSStorageTestService(t *testing.T) *Service {
	t.Helper()

	svc, err := New(t.Context(), Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, svc.Initialize(t.Context()))
	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

func TestTLSStorage_RoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := newTLSStorageTestService(t)
	ctx := t.Context()

	const key = "certificates/acme-v02/status.acme.com/status.acme.com.crt"

	// Missing key: Load/Stat report not-found, Exists is false.
	_, err := svc.TLSStorageLoad(ctx, key)
	r.ErrorIs(err, sql.ErrNoRows)

	_, err = svc.TLSStorageStat(ctx, key)
	r.ErrorIs(err, sql.ErrNoRows)

	exists, err := svc.TLSStorageExists(ctx, key)
	r.NoError(err)
	r.False(exists)

	// Store then load.
	payload := []byte("-----BEGIN CERTIFICATE-----\x00binary\xff-----END-----")
	r.NoError(svc.TLSStorageStore(ctx, key, payload))

	loaded, err := svc.TLSStorageLoad(ctx, key)
	r.NoError(err)
	r.Equal(payload, loaded, "binary payloads must round-trip byte-exact")

	exists, err = svc.TLSStorageExists(ctx, key)
	r.NoError(err)
	r.True(exists)

	info, err := svc.TLSStorageStat(ctx, key)
	r.NoError(err)
	r.Equal(key, info.Key)
	r.Equal(int64(len(payload)), info.Size)
	r.WithinDuration(time.Now(), info.ModifiedAt, time.Minute)

	// Overwrite.
	r.NoError(svc.TLSStorageStore(ctx, key, []byte("second")))
	loaded, err = svc.TLSStorageLoad(ctx, key)
	r.NoError(err)
	r.Equal([]byte("second"), loaded)

	// Delete, then it is gone again.
	r.NoError(svc.TLSStorageDelete(ctx, key))
	_, err = svc.TLSStorageLoad(ctx, key)
	r.ErrorIs(err, sql.ErrNoRows)

	// Deleting a missing key is not an error (certmagic only requires absence).
	r.NoError(svc.TLSStorageDelete(ctx, key))
}

func TestTLSStorage_ListAndPrefixDelete(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := newTLSStorageTestService(t)
	ctx := t.Context()

	keys := []string{
		"acme/ca/users/default/default.json",
		"certificates/ca/a.example.com/a.example.com.crt",
		"certificates/ca/a.example.com/a.example.com.key",
		"certificates/ca/b.example.com/b.example.com.crt",
		// A neighbor whose name merely STARTS with the prefix must not be
		// swept up by the key-range scan.
		"certificates-backup/ca/a.example.com/a.example.com.crt",
	}
	for _, key := range keys {
		r.NoError(svc.TLSStorageStore(ctx, key, []byte(key)))
	}

	all, err := svc.TLSStorageList(ctx, "")
	r.NoError(err)
	r.Len(all, len(keys))

	under, err := svc.TLSStorageList(ctx, "certificates")
	r.NoError(err)
	r.Len(under, 3)
	for _, info := range under {
		r.Contains(info.Key, "certificates/")
		r.Positive(info.Size)
	}

	nested, err := svc.TLSStorageList(ctx, "certificates/ca/a.example.com")
	r.NoError(err)
	r.Len(nested, 2)

	// Prefix delete removes the subtree and nothing else.
	r.NoError(svc.TLSStorageDelete(ctx, "certificates/ca/a.example.com"))

	under, err = svc.TLSStorageList(ctx, "certificates")
	r.NoError(err)
	r.Len(under, 1)
	r.Equal("certificates/ca/b.example.com/b.example.com.crt", under[0].Key)

	neighbor, err := svc.TLSStorageExists(ctx, "certificates-backup/ca/a.example.com/a.example.com.crt")
	r.NoError(err)
	r.True(neighbor, "a sibling prefix must survive a prefix delete")
}

func TestTLSStorage_LockContentionAndExpiry(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := newTLSStorageTestService(t)
	ctx := t.Context()

	const key = "issue_cert_status.acme.com"

	// First holder wins.
	got, err := svc.TLSStorageAcquireLock(ctx, key, "owner-a", time.Now().Add(time.Minute))
	r.NoError(err)
	r.True(got)

	// A second owner is refused while the lease is live.
	got, err = svc.TLSStorageAcquireLock(ctx, key, "owner-b", time.Now().Add(time.Minute))
	r.NoError(err)
	r.False(got)

	// The holder can extend its own lease; a non-holder cannot.
	refreshed, err := svc.TLSStorageRefreshLock(ctx, key, "owner-a", time.Now().Add(2*time.Minute))
	r.NoError(err)
	r.True(refreshed)

	refreshed, err = svc.TLSStorageRefreshLock(ctx, key, "owner-b", time.Now().Add(2*time.Minute))
	r.NoError(err)
	r.False(refreshed)

	// Releasing as a non-holder must not free someone else's lock.
	r.NoError(svc.TLSStorageReleaseLock(ctx, key, "owner-b"))
	got, err = svc.TLSStorageAcquireLock(ctx, key, "owner-b", time.Now().Add(time.Minute))
	r.NoError(err)
	r.False(got)

	// The real holder releases; the lock is then free.
	r.NoError(svc.TLSStorageReleaseLock(ctx, key, "owner-a"))
	got, err = svc.TLSStorageAcquireLock(ctx, key, "owner-b", time.Now().Add(time.Minute))
	r.NoError(err)
	r.True(got)

	// An EXPIRED lease is reclaimable — a crashed issuance must not wedge
	// renewals forever.
	r.NoError(svc.TLSStorageReleaseLock(ctx, key, "owner-b"))
	got, err = svc.TLSStorageAcquireLock(ctx, key, "owner-dead", time.Now().Add(-time.Second))
	r.NoError(err)
	r.True(got)

	got, err = svc.TLSStorageAcquireLock(ctx, key, "owner-c", time.Now().Add(time.Minute))
	r.NoError(err)
	r.True(got, "an expired lease must be reclaimable")

	// The dead owner's refresh now fails, so its refresher stops.
	refreshed, err = svc.TLSStorageRefreshLock(ctx, key, "owner-dead", time.Now().Add(time.Minute))
	r.NoError(err)
	r.False(refreshed)
}

func TestTLSStorage_ConcurrentLockHasOneWinner(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := newTLSStorageTestService(t)
	ctx := t.Context()

	const (
		key     = "issue_cert_race.example.com"
		racers  = 8
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

			ok, err := svc.TLSStorageAcquireLock(ctx, key, "owner-"+string(rune('a'+idx)), time.Now().Add(leaseIn))

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				// SQLite serializes writers; a busy error is a test-harness
				// artifact, not a correctness failure, so record it rather
				// than counting it as a winner.
				errs = append(errs, err)

				return
			}

			if ok {
				winners++
			}
		}(i)
	}

	wg.Wait()

	for _, err := range errs {
		r.NotErrorIs(err, sql.ErrNoRows)
	}

	r.Equal(1, winners, "exactly one goroutine may hold the lock")
}
