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

			ok, advErr := svc.TryAdvanceHeartbeatCounter(ctx, check.UID, 4294967297)
			if advErr == nil && ok {
				accepted.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	r.Equal(int64(1), accepted.Load(), "exactly one of the identical concurrent beats may win")

	stored, ok, err := svc.GetHeartbeatCounter(ctx, check.UID)
	r.NoError(err)
	r.True(ok)
	r.Equal(int64(4294967297), stored)

	// Positive control: a strictly greater counter still advances, so the
	// assertion above is not passing because every write failed.
	advanced, err := svc.TryAdvanceHeartbeatCounter(ctx, check.UID, 4294967298)
	r.NoError(err)
	r.True(advanced)

	// And the negative once more, serially: an older counter is refused.
	advanced, err = svc.TryAdvanceHeartbeatCounter(ctx, check.UID, 1)
	r.NoError(err)
	r.False(advanced)
}
