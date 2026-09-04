package credentials_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// countingStore records how many times the DEK was read back from storage, and
// can be told to swap the stored key or to fail a save round-trip. The load
// count is the whole point: the cache invalidation must be provably bounded.
type countingStore struct {
	mu     sync.Mutex
	data   map[string][]byte
	loads  int
	corupt bool // when set, LoadDEK hands back an unopenable value
}

func newCountingStore() *countingStore {
	return &countingStore{data: map[string][]byte{}}
}

func (s *countingStore) LoadDEK(_ context.Context, orgUID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loads++

	if s.corupt {
		return []byte(`{"v":1,"alg":"AES-256-GCM","nonce":"AAAAAAAAAAAAAAAA","ct":"AAAA"}`), true, nil
	}

	v, ok := s.data[orgUID]
	if !ok {
		return nil, false, nil
	}

	return v, true, nil
}

func (s *countingStore) SaveDEK(_ context.Context, orgUID string, wrapped []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[orgUID] = wrapped

	return nil
}

func (s *countingStore) loadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.loads
}

func (s *countingStore) replace(orgUID string, wrapped []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[orgUID] = wrapped
}

// TestDecryptDropsStaleCachedDEKAndReloadsOnce is the resolved-open-question
// test: a single decrypt failure must trigger EXACTLY one cold reload, and the
// retry must then succeed. The absence of this invalidation is why the bad DEK
// write stayed invisible to the process that made it.
func TestDecryptDropsStaleCachedDEKAndReloadsOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	const org = "org-stale"

	store := newCountingStore()
	key := newKey(t)

	// Service A warms its cache with the DEK it generated.
	svcA, err := credentials.NewService(key, store)
	r.NoError(err)
	r.NoError(svcA.EnsureOrgKey(ctx, org))

	// Someone else rotates the org's DEK behind A's back: B generates a fresh
	// one into an empty store and encrypts under it, then the row is put back.
	rotated := newCountingStore()
	svcB, err := credentials.NewService(key, rotated)
	r.NoError(err)

	envelope, err := svcB.EncryptForOrg(ctx, org, map[string]any{"password": "s3cr3t"})
	r.NoError(err)

	rotatedDEK, _, err := rotated.LoadDEK(ctx, org)
	r.NoError(err)
	store.replace(org, rotatedDEK)

	before := store.loadCount()

	got, err := svcA.DecryptForOrg(ctx, org, envelope)
	r.NoError(err, "a stale cached DEK must be dropped and reloaded, not surfaced as a failure")
	r.Equal("s3cr3t", got["password"])
	r.Equal(before+1, store.loadCount(), "exactly one cold reload, no more")
}

// TestDecryptSurfacesPersistentFailureAfterOneReload is the other half of the
// bound: when the reload changes nothing, the error must surface instead of the
// service reloading forever.
func TestDecryptSurfacesPersistentFailureAfterOneReload(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	const org = "org-persistent"

	store := newCountingStore()
	key := newKey(t)

	svc, err := credentials.NewService(key, store)
	r.NoError(err)
	r.NoError(svc.EnsureOrgKey(ctx, org))

	// An envelope this org's DEK can never open.
	foreign, err := credentials.NewService(newKey(t), newCountingStore())
	r.NoError(err)

	envelope, err := foreign.EncryptForOrg(ctx, org, map[string]any{"password": "nope"})
	r.NoError(err)

	before := store.loadCount()

	_, err = svc.DecryptForOrg(ctx, org, envelope)
	r.Error(err, "a genuinely unopenable envelope must still fail")
	r.Equal(before+1, store.loadCount(), "one retry only — never a reload loop")

	// A second call is likewise bounded: one reload per call, not per attempt.
	before = store.loadCount()
	_, err = svc.DecryptForOrg(ctx, org, envelope)
	r.Error(err)
	r.Equal(before+1, store.loadCount())
}

// TestEnsureOrgKeyFailsWhenTheStoredKeyCannotBeReloaded pins Proposal item 2: a
// storage-shape regression must break the FIRST encrypt rather than caching a
// key nobody else can read. Its positive control is every other test in this
// package, which round-trips through a store that does work.
func TestEnsureOrgKeyFailsWhenTheStoredKeyCannotBeReloaded(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	store := newCountingStore()
	store.corupt = true

	svc, err := credentials.NewService(newKey(t), store)
	r.NoError(err)

	// No row exists yet, so this generates, saves — and must refuse to cache.
	_, err = svc.EncryptForOrg(ctx, "org-badstore", map[string]any{"password": "x"})
	r.Error(err, "a key that cannot be read back must not be cached and used")
	r.ErrorIs(err, credentials.ErrOrgKeyUnavailable)
	r.Equal(0, svc.DEKCacheLen(), "nothing may be cached after a failed round-trip")
}

// TestEnsureOrgKeyMarksLoadFailuresAsOrgKeyUnavailable keeps the failure
// taxonomy honest: everything that goes wrong while OBTAINING the org key is
// tagged, so callers can stop telling operators to re-save the check.
func TestEnsureOrgKeyMarksLoadFailuresAsOrgKeyUnavailable(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	boom := errors.New("database is down")
	svc, err := credentials.NewService(newKey(t), failingStore{err: boom})
	r.NoError(err)

	err = svc.EnsureOrgKey(t.Context(), "org-1")
	r.ErrorIs(err, credentials.ErrOrgKeyUnavailable)
	r.ErrorIs(err, boom)
}

type failingStore struct{ err error }

func (s failingStore) LoadDEK(context.Context, string) ([]byte, bool, error) {
	return nil, false, s.err
}

func (s failingStore) SaveDEK(context.Context, string, []byte) error { return s.err }
