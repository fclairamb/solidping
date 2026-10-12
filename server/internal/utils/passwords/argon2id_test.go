package passwords

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// withFakeRelease swaps the memory release for a counter and shortens the delay.
// Not parallel-safe: it mutates package state.
func withFakeRelease(t *testing.T, delay time.Duration) *atomic.Int32 {
	t.Helper()

	var count atomic.Int32

	releaseMu.Lock()
	prevDelay, prevRelease := releaseDelay, release
	releaseDelay = delay
	release = func() { count.Add(1) }
	releaseMu.Unlock()

	t.Cleanup(func() {
		releaseMu.Lock()
		releaseDelay, release = prevDelay, prevRelease
		releaseMu.Unlock()
	})

	return &count
}

// cheapArgon2Policy keeps the derivations fast; scheduling does not depend on
// the memory size.
func cheapArgon2Policy() Policy {
	p := argon2DefaultPolicy()
	p.Argon2.Memory = 1024
	p.Argon2.Time = 1
	p.Argon2.Threads = 1

	return p
}

// TestReleaseScheduling: argon2id derivations schedule exactly one memory
// release after the last one in a burst ends; bcrypt schedules none
// (spec 2026-10-12-04).
//
//nolint:paralleltest // mutates the process-wide policy and release hooks
func TestReleaseScheduling(t *testing.T) {
	const delay = 30 * time.Millisecond

	// Minted before the fake release is installed: its own pending release
	// captured the real hook, and the verify below cancels it.
	withPolicy(t, cheapArgon2Policy())
	verifyHash, err := Hash("pw")
	require.NoError(t, err)

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"one argon2id hash", func(t *testing.T) {
			t.Helper()
			_, err := Hash("pw")
			require.NoError(t, err)
		}},
		{"one argon2id verify", func(t *testing.T) {
			t.Helper()
			require.True(t, Verify("pw", verifyHash))
		}},
		{"four overlapping hashes", func(t *testing.T) {
			t.Helper()
			var wg sync.WaitGroup

			errs := make(chan error, 4)
			for range 4 {
				wg.Go(func() {
					_, err := Hash("pw")
					errs <- err
				})
			}
			wg.Wait()
			close(errs)

			for err := range errs {
				require.NoError(t, err)
			}
		}},
		{"hash starting while the timer is armed", func(t *testing.T) {
			t.Helper()
			_, err := Hash("pw")
			require.NoError(t, err)
			time.Sleep(delay / 3)
			_, err = Hash("pw")
			require.NoError(t, err)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withPolicy(t, cheapArgon2Policy())
			count := withFakeRelease(t, delay)

			tt.run(t)

			require.Zero(t, count.Load(), "no release while the delay has not elapsed")
			require.Eventually(t, func() bool { return count.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
			time.Sleep(3 * delay)
			require.Equal(t, int32(1), count.Load(), "exactly one release per burst")
		})
	}

	t.Run("one bcrypt hash", func(t *testing.T) {
		withPolicy(t, bcryptPolicy(4))
		count := withFakeRelease(t, delay)

		_, err := Hash("pw")
		require.NoError(t, err)

		time.Sleep(4 * delay)
		require.Zero(t, count.Load(), "bcrypt never schedules a release")
	})
}
