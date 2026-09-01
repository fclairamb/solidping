package sqlite

import (
	"math"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func newCounterTestService(t *testing.T) (*Service, string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	org := models.NewOrganization("hb-counter-org", "HB Counter Org")
	r.NoError(svc.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "hb-counter-check", "heartbeat")
	r.NoError(svc.CreateCheck(ctx, check))

	return svc, check.UID
}

// TestTryAdvanceHeartbeatCounterRejectsReplay is the replay-protection
// contract: only a strictly greater counter is accepted, and a rejected
// advance must not move the stored value.
func TestTryAdvanceHeartbeatCounterRejectsReplay(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc, checkUID := newCounterTestService(t)

	// No row yet: the first beat is accepted at whatever counter it carries.
	_, ok, err := svc.GetHeartbeatCounter(ctx, checkUID)
	r.NoError(err)
	r.False(ok)

	accepted, err := svc.TryAdvanceHeartbeatCounter(ctx, checkUID, 100)
	r.NoError(err)
	r.True(accepted, "first counter must be accepted")

	// Positive control: a strictly greater counter still advances.
	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, checkUID, 101)
	r.NoError(err)
	r.True(accepted)

	// The replay itself: the SAME counter is refused, which is what makes even
	// the most recent captured datagram unusable.
	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, checkUID, 101)
	r.NoError(err)
	r.False(accepted, "an equal counter is a replay and must be refused")

	// An older counter is refused too.
	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, checkUID, 50)
	r.NoError(err)
	r.False(accepted)

	// And a refused advance leaves the stored value untouched — otherwise a
	// replay would rewind the check and re-open the whole window.
	stored, ok, err := svc.GetHeartbeatCounter(ctx, checkUID)
	r.NoError(err)
	r.True(ok)
	r.Equal(int64(101), stored)
}

// TestTryAdvanceHeartbeatCounterIsPerCheck proves one device's counter never
// gates another check's beats.
func TestTryAdvanceHeartbeatCounterIsPerCheck(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc, checkUID := newCounterTestService(t)

	org := models.NewOrganization("hb-counter-org-2", "HB Counter Org 2")
	r.NoError(svc.CreateOrganization(ctx, org))
	other := models.NewCheck(org.UID, "hb-counter-check-2", "heartbeat")
	r.NoError(svc.CreateCheck(ctx, other))

	accepted, err := svc.TryAdvanceHeartbeatCounter(ctx, checkUID, 5000)
	r.NoError(err)
	r.True(accepted)

	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, other.UID, 1)
	r.NoError(err)
	r.True(accepted, "a different check starts from nothing")
}

// TestTryAdvanceHeartbeatCounterConcurrentSameValue proves the strictly-greater
// test is done by the database, not by a read-then-write: a device that retries
// the same datagram concurrently must be accepted exactly once.
func TestTryAdvanceHeartbeatCounterConcurrentSameValue(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc, checkUID := newCounterTestService(t)

	const racers = 8

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted int
	)

	wg.Add(racers)

	for range racers {
		go func() {
			defer wg.Done()

			ok, err := svc.TryAdvanceHeartbeatCounter(ctx, checkUID, 7)
			if err == nil && ok {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	r.Equal(1, accepted, "exactly one of the identical concurrent beats may win")
}

// TestTryAdvanceHeartbeatCounterHandlesMaxInt64 pins the top of the range the
// wire protocol admits (the parser refuses anything above it).
func TestTryAdvanceHeartbeatCounterHandlesMaxInt64(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc, checkUID := newCounterTestService(t)

	accepted, err := svc.TryAdvanceHeartbeatCounter(ctx, checkUID, math.MaxInt64)
	r.NoError(err)
	r.True(accepted)

	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, checkUID, math.MaxInt64)
	r.NoError(err)
	r.False(accepted)
}
