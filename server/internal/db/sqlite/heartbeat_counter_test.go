package sqlite

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func newCounterTestService(t *testing.T) (*Service, string, string) {
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

	return svc, org.UID, check.UID
}

// TestTryAdvanceHeartbeatCounterRejectsReplay is the replay-protection
// contract: only a strictly greater counter is accepted, and a rejected
// advance must not move the stored value.
func TestTryAdvanceHeartbeatCounterRejectsReplay(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc, orgUID, checkUID := newCounterTestService(t)

	// No row yet: the first beat is accepted at whatever counter it carries.
	_, ok, err := svc.GetHeartbeatCounter(ctx, orgUID, checkUID)
	r.NoError(err)
	r.False(ok)

	accepted, err := svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 100)
	r.NoError(err)
	r.True(accepted, "first counter must be accepted")

	// Positive control: a strictly greater counter still advances.
	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 101)
	r.NoError(err)
	r.True(accepted)

	// The replay itself: the SAME counter is refused, which is what makes even
	// the most recent captured datagram unusable.
	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 101)
	r.NoError(err)
	r.False(accepted, "an equal counter is a replay and must be refused")

	// An older counter is refused too.
	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 50)
	r.NoError(err)
	r.False(accepted)

	// And a refused advance leaves the stored value untouched — otherwise a
	// replay would rewind the check and re-open the whole window.
	stored, ok, err := svc.GetHeartbeatCounter(ctx, orgUID, checkUID)
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
	svc, orgUID, checkUID := newCounterTestService(t)

	org := models.NewOrganization("hb-counter-org-2", "HB Counter Org 2")
	r.NoError(svc.CreateOrganization(ctx, org))
	other := models.NewCheck(org.UID, "hb-counter-check-2", "heartbeat")
	r.NoError(svc.CreateCheck(ctx, other))

	accepted, err := svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 5000)
	r.NoError(err)
	r.True(accepted)

	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, org.UID, other.UID, 1)
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
	svc, orgUID, checkUID := newCounterTestService(t)

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

			ok, err := svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 7)
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
	svc, orgUID, checkUID := newCounterTestService(t)

	accepted, err := svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, math.MaxInt64)
	r.NoError(err)
	r.True(accepted)

	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, math.MaxInt64)
	r.NoError(err)
	r.False(accepted)
}

// TestHeartbeatCounterLivesInStateEntriesWithoutExpiry pins the storage
// contract the counter now depends on (spec 2026-09-01-06, "Resolved: where
// the counter lives").
//
// Two properties, both load-bearing:
//
//   - the row is the generic state entry `heartbeat_counter/<checkUID>`,
//     org-scoped, so an org delete cascades it away;
//   - `expires_at` is a SLIDING models.HeartbeatCounterTTL window, pushed out
//     by every accepted beat, so a device that is actually beating never
//     expires its own counter and only a check that went quiet for a week is
//     ever swept.
func TestHeartbeatCounterLivesInStateEntriesWithASlidingExpiry(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc, orgUID, checkUID := newCounterTestService(t)

	accepted, err := svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 42)
	r.NoError(err)
	r.True(accepted)

	entry, err := svc.GetStateEntry(ctx, &orgUID, "heartbeat_counter/"+checkUID)
	r.NoError(err)
	r.NotNil(entry, "the counter must be a plain state entry under the documented key")
	r.NotNil(entry.ExpiresAt, "a counter carries a TTL so dead checks get swept")
	r.WithinDuration(
		time.Now().Add(models.HeartbeatCounterTTL), *entry.ExpiresAt, time.Minute,
		"the TTL must be the shared constant, not an ad-hoc interval",
	)
	r.NotNil(entry.Value)
	r.EqualValues(42, (*entry.Value)["lastCounter"])

	firstExpiry := *entry.ExpiresAt

	// The sweeper is the actual threat: prove it leaves a live counter alone
	// rather than merely asserting the column.
	_, err = svc.DeleteExpiredStateEntries(ctx)
	r.NoError(err)

	stored, ok, err := svc.GetHeartbeatCounter(ctx, orgUID, checkUID)
	r.NoError(err)
	r.True(ok, "the expiry sweep must not remove a live counter")
	r.Equal(int64(42), stored)

	// The window SLIDES: a later accepted beat pushes it out again, which is
	// what stops a beating device from ever expiring itself.
	time.Sleep(1100 * time.Millisecond)

	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 43)
	r.NoError(err)
	r.True(accepted)

	entry, err = svc.GetStateEntry(ctx, &orgUID, "heartbeat_counter/"+checkUID)
	r.NoError(err)
	r.NotNil(entry.ExpiresAt)
	r.True(
		entry.ExpiresAt.After(firstExpiry),
		"an accepted beat must push the expiry out, got %s vs %s", entry.ExpiresAt, firstExpiry,
	)
}

// TestHeartbeatCounterKeepsGatingOnceSwept is the security half of the TTL.
//
// The sweep is a SOFT delete and the advance deliberately ignores deleted_at,
// so even a counter that outlived its window keeps refusing replays. Without
// that, letting counters expire would hand an attacker a way to rewind a check
// simply by waiting — which matters most for a clockless (ts=0) device, where
// this counter is the ONLY replay protection.
func TestHeartbeatCounterKeepsGatingOnceSwept(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc, orgUID, checkUID := newCounterTestService(t)

	accepted, err := svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 900)
	r.NoError(err)
	r.True(accepted)

	// Backdate the window so the sweeper actually bites this row.
	key := "heartbeat_counter/" + checkUID
	past := time.Now().Add(-time.Hour)
	_, err = svc.DB().NewUpdate().
		Model((*models.StateEntry)(nil)).
		Set("expires_at = ?", past).
		Where("key = ?", key).
		Exec(ctx)
	r.NoError(err)

	swept, err := svc.DeleteExpiredStateEntries(ctx)
	r.NoError(err)
	r.Positive(swept, "the fixture must actually be swept, or this proves nothing")

	// The generic reader now sees nothing — that is the sweep working.
	entry, err := svc.GetStateEntry(ctx, &orgUID, key)
	r.NoError(err)
	r.Nil(entry, "a swept counter is soft-deleted for the generic reader")

	// ...but the guard still gates. Replay must still fail.
	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 900)
	r.NoError(err)
	r.False(accepted, "a swept counter must still refuse a replayed value")

	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 899)
	r.NoError(err)
	r.False(accepted, "a swept counter must still refuse a lower value")

	// Positive control: a genuinely higher counter is still accepted, so the
	// assertions above are not just "everything is refused".
	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 901)
	r.NoError(err)
	r.True(accepted, "a real device must still be able to advance")
}

// TestHeartbeatCounterSurvivesASoftDeletedStateEntry is the fail-safe
// direction of the `unique (organization_uid, key)` constraint carrying no
// deleted_at predicate: a soft-deleted row still owns the slot, so it must
// still gate the advance. If a soft delete read as "no counter", anyone able
// to delete the entry could rewind the check and replay every old datagram.
func TestHeartbeatCounterSurvivesASoftDeletedStateEntry(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	svc, orgUID, checkUID := newCounterTestService(t)

	accepted, err := svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 900)
	r.NoError(err)
	r.True(accepted)

	deleted, err := svc.DeleteStateEntry(ctx, &orgUID, "heartbeat_counter/"+checkUID)
	r.NoError(err)
	r.True(deleted)

	// The generic reader hides it, which is exactly why the counter has its
	// own reader: this is the divergence the accessor exists to close.
	entry, err := svc.GetStateEntry(ctx, &orgUID, "heartbeat_counter/"+checkUID)
	r.NoError(err)
	r.Nil(entry)

	stored, ok, err := svc.GetHeartbeatCounter(ctx, orgUID, checkUID)
	r.NoError(err)
	r.True(ok, "the guard still holds a value, so the reader must report it")
	r.Equal(int64(900), stored)

	// The replay a soft delete must NOT unlock.
	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 900)
	r.NoError(err)
	r.False(accepted, "a soft-deleted entry must still refuse a replayed counter")

	// Positive control: a legitimate beat still advances, and un-deletes the
	// row rather than leaving live state hidden behind deleted_at.
	accepted, err = svc.TryAdvanceHeartbeatCounter(ctx, orgUID, checkUID, 901)
	r.NoError(err)
	r.True(accepted)

	entry, err = svc.GetStateEntry(ctx, &orgUID, "heartbeat_counter/"+checkUID)
	r.NoError(err)
	r.NotNil(entry, "a winning advance restores the entry")
	r.NotNil(entry.ExpiresAt, "a winning advance also refreshes the TTL")
	r.WithinDuration(
		time.Now().Add(models.HeartbeatCounterTTL), *entry.ExpiresAt, time.Minute,
	)
}
