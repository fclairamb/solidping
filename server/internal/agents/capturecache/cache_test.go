package capturecache_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents/capturecache"
)

// blob builds a distinguishable payload of the given size.
func blob(size int, marker byte) []byte {
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = marker
	}

	return buf
}

// TestPutTakeRoundTrip is the positive control every negative below depends
// on: what goes in comes back out, byte for byte, under the id Put minted.
func TestPutTakeRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cache := capturecache.NewDefault()

	want := blob(1024, 'a')

	id := cache.Put(want)
	r.NotEmpty(id, "a capture inside the budget must be stored")

	got, ok := cache.Take(id)
	r.True(ok)
	r.Equal(want, got)
}

// TestTakeIsOneShot pins the "never retried forever" property at its source:
// the bytes leave the cache on the first read, so a failed upload has nothing
// to retry with.
func TestTakeIsOneShot(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cache := capturecache.NewDefault()

	id := cache.Put(blob(64, 'a'))
	r.NotEmpty(id)

	_, ok := cache.Take(id)
	r.True(ok)

	_, ok = cache.Take(id)
	r.False(ok, "a capture must be served at most once")
	r.Zero(cache.Len())
	r.Zero(cache.Bytes())
}

// TestTakeUnknownIDMisses covers the id the server could echo back after the
// entry is gone (evicted, expired, or simply never real).
func TestTakeUnknownIDMisses(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cache := capturecache.NewDefault()

	_, ok := cache.Take("no-such-capture")
	r.False(ok)

	_, ok = cache.Take("")
	r.False(ok)
}

// TestBoundedByEntryCount is the count half of the spec's "bounded by count AND
// bytes": five small captures into a cache of three leave the newest three, and
// the two OLDEST are the ones gone.
func TestBoundedByEntryCount(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cache := capturecache.New(3, 1<<20, time.Minute)

	ids := make([]string, 0, 5)
	for i := range 5 {
		id := cache.Put(blob(16, byte('a'+i)))
		r.NotEmpty(id)

		ids = append(ids, id)
	}

	r.Equal(3, cache.Len(), "the count bound must hold")

	for _, gone := range ids[:2] {
		_, ok := cache.Take(gone)
		r.False(ok, "the oldest captures must be the evicted ones")
	}

	for _, kept := range ids[2:] {
		_, ok := cache.Take(kept)
		r.True(ok, "the newest captures must survive")
	}
}

// TestBoundedByBytes is the byte half, and it is a REAL negative: the entry
// count bound is never reached here (4 entries allowed, 3 stored), so only the
// byte budget can be what evicts.
func TestBoundedByBytes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cache := capturecache.New(10, 300, time.Minute)

	first := cache.Put(blob(200, 'a'))
	second := cache.Put(blob(200, 'b'))
	r.NotEmpty(first)
	r.NotEmpty(second)

	r.Equal(1, cache.Len(), "the byte budget must have evicted the first capture")
	r.Equal(200, cache.Bytes())

	_, ok := cache.Take(first)
	r.False(ok, "the OLDEST capture is the one the byte budget drops")

	_, ok = cache.Take(second)
	r.True(ok)
}

// TestPutRefusesOversizedCapture pins the degradation the marker fields already
// express: a blob bigger than the whole budget is not stored, and returning ""
// tells the caller to advertise no capture at all. The critical negative is
// that it does NOT evict the captures already held to make room for something
// that could never fit.
func TestPutRefusesOversizedCapture(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cache := capturecache.New(4, 1000, time.Minute)

	kept := cache.Put(blob(100, 'a'))
	r.NotEmpty(kept)

	r.Empty(cache.Put(blob(1001, 'b')), "a capture past the whole budget must be refused")
	r.Empty(cache.Put(nil), "an empty capture must be refused")

	r.Equal(1, cache.Len())

	_, ok := cache.Take(kept)
	r.True(ok, "the refused oversized capture must not have evicted the held one")
}

// TestTTLExpiry drives the clock: a capture older than the TTL is gone even
// though neither bound was ever reached.
func TestTTLExpiry(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var (
		mu  sync.Mutex
		now = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	)

	cache := capturecache.New(10, 1<<20, time.Minute, capturecache.WithClock(func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		return now
	}))

	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()

		now = now.Add(d)
	}

	old := cache.Put(blob(64, 'a'))
	r.NotEmpty(old)

	advance(30 * time.Second)

	fresh := cache.Put(blob(64, 'b'))
	r.NotEmpty(fresh)
	r.Equal(2, cache.Len())

	// 61s after the first Put, 31s after the second: only the first is stale.
	advance(31 * time.Second)

	_, ok := cache.Take(old)
	r.False(ok, "a capture past its TTL must be gone")

	_, ok = cache.Take(fresh)
	r.True(ok, "a capture inside its TTL must survive")
}

// TestConcurrentPutTake is the race-detector's business: the cache is written
// from the worker loop and read from the frame handler's goroutine.
func TestConcurrentPutTake(t *testing.T) {
	t.Parallel()

	cache := capturecache.New(4, 1<<20, time.Minute)

	var wg sync.WaitGroup

	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 20 {
				if id := cache.Put(blob(128, 'x')); id != "" {
					cache.Take(id)
				}

				cache.Len()
				cache.Bytes()
			}
		}()
	}

	wg.Wait()
}
