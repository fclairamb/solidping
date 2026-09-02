package postgres

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Distinct from every other _postgres_test.go embedded port in the repo (see
// the port-numbering note in postgres_headroom_postgres_test.go).
const portHeartbeatCounter = 15505

// TestTryAdvanceHeartbeatCounter_ConcurrentReplay_Postgres fires many
// concurrent beats carrying the SAME counter at a real PostgreSQL backend and
// asserts exactly one is accepted.
//
// This is the property SQLite's single-connection tests cannot prove, and it is
// the whole security value of SP2: if two concurrent copies of one captured
// datagram could both be accepted, an attacker replaying a beat in a tight loop
// would keep a dead device looking alive.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestTryAdvanceHeartbeatCounter_ConcurrentReplay_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portHeartbeatCounter, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("hb-ctr-org", "HB Counter Org")
	r.NoError(svc.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "hb-ctr-check", "heartbeat")
	r.NoError(svc.CreateCheck(ctx, check))

	const goroutines = 40

	var (
		accepted atomic.Int64
		wg       sync.WaitGroup
		start    = make(chan struct{})
	)

	for range goroutines {
		wg.Add(1)

		go func() {
			defer wg.Done()
			<-start

			ok, advErr := svc.TryAdvanceHeartbeatCounter(ctx, org.UID, check.UID, 4294967297)
			if advErr == nil && ok {
				accepted.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	r.Equal(int64(1), accepted.Load(), "exactly one of the identical concurrent beats may win")

	stored, ok, err := svc.GetHeartbeatCounter(ctx, org.UID, check.UID)
	r.NoError(err)
	r.True(ok)
	r.Equal(int64(4294967297), stored)

	// Positive control: a strictly greater counter still advances, so the
	// assertion above is not passing because every write failed.
	advanced, err := svc.TryAdvanceHeartbeatCounter(ctx, org.UID, check.UID, 4294967298)
	r.NoError(err)
	r.True(advanced)

	// And the negative once more, serially: an older counter is refused.
	advanced, err = svc.TryAdvanceHeartbeatCounter(ctx, org.UID, check.UID, 1)
	r.NoError(err)
	r.False(advanced)
}

// TestHeartbeatCounterStateEntryContract_Postgres pins the storage contract on
// the engine that actually runs in production (spec 2026-09-01-06, "Resolved:
// where the counter lives").
//
// Three properties, each one a way the counter could silently stop protecting
// anything: it must be the documented state entry, it must never carry an
// `expires_at` (the sweeper would delete it and re-open the replay window),
// and a soft-deleted row must keep gating the advance (the unique constraint
// has no deleted_at predicate, so the row still owns the slot — reading it as
// "absent" would let anyone who can delete the entry rewind the check).
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestHeartbeatCounterStateEntryContract_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portHeartbeatCounter, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("hb-ctr-state-org", "HB Counter State Org")
	r.NoError(svc.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "hb-ctr-state-check", "heartbeat")
	r.NoError(svc.CreateCheck(ctx, check))

	key := "heartbeat_counter/" + check.UID

	advanced, err := svc.TryAdvanceHeartbeatCounter(ctx, org.UID, check.UID, 42)
	r.NoError(err)
	r.True(advanced)

	entry, err := svc.GetStateEntry(ctx, &org.UID, key)
	r.NoError(err)
	r.NotNil(entry, "the counter must be a plain state entry under the documented key")
	r.Nil(entry.ExpiresAt, "a counter must never expire")
	r.NotNil(entry.Value)
	r.EqualValues(42, (*entry.Value)["lastCounter"])

	// The sweeper is the actual threat, so run it rather than only asserting
	// on the column.
	_, err = svc.DeleteExpiredStateEntries(ctx)
	r.NoError(err)

	stored, ok, err := svc.GetHeartbeatCounter(ctx, org.UID, check.UID)
	r.NoError(err)
	r.True(ok, "the expiry sweep must not remove a counter")
	r.Equal(int64(42), stored)

	// Soft delete: the slot is still occupied, so the guard must still hold.
	deleted, err := svc.DeleteStateEntry(ctx, &org.UID, key)
	r.NoError(err)
	r.True(deleted)

	advanced, err = svc.TryAdvanceHeartbeatCounter(ctx, org.UID, check.UID, 42)
	r.NoError(err)
	r.False(advanced, "a soft-deleted entry must still refuse a replayed counter")

	// Positive control: a legitimate beat still advances, and un-deletes the
	// row rather than leaving live state hidden behind deleted_at.
	advanced, err = svc.TryAdvanceHeartbeatCounter(ctx, org.UID, check.UID, 43)
	r.NoError(err)
	r.True(advanced)

	entry, err = svc.GetStateEntry(ctx, &org.UID, key)
	r.NoError(err)
	r.NotNil(entry, "a winning advance restores the entry")
	r.Nil(entry.ExpiresAt)
}
