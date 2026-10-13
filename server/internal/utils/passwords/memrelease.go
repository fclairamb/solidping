package passwords

import (
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// defaultReleaseDelay is how long the process waits after the last argon2id
// derivation ends before handing the freed memory back to the OS.
const defaultReleaseDelay = time.Second

// Each argon2id derivation allocates its whole memory block (64 MiB with the
// default profile). Once it returns, the block is garbage, but the GC goal it
// inflated (live heap ~75 MB → goal ~150 MB) and the pages it touched stay
// resident for minutes on an idle instance. A plain runtime.GC() resets the goal
// but does not return the pages; debug.FreeOSMemory() does both (measured:
// RssAnon 163 → 37 MB at once). So after a burst of derivations, once none is in
// flight, one release is scheduled. Spec 2026-10-12-04.
//
//nolint:gochecknoglobals // process-wide release scheduling, injectable by tests
var (
	// releaseDelay is the quiet period after the last derivation before release runs.
	releaseDelay = defaultReleaseDelay
	// release gives the freed argon2 memory back to the OS.
	release = debug.FreeOSMemory

	argon2InFlight atomic.Int32
	releaseMu      sync.Mutex
	releaseTimer   *time.Timer
)

// argon2Started records the start of a derivation. It stops a pending release so
// the timer never fires in the middle of a burst.
func argon2Started() {
	releaseMu.Lock()
	defer releaseMu.Unlock()

	argon2InFlight.Add(1)

	if releaseTimer != nil {
		releaseTimer.Stop()
		releaseTimer = nil
	}
}

// argon2Finished records the end of a derivation. When it was the last one in
// flight, a single release is (re)armed for releaseDelay later.
func argon2Finished() {
	releaseMu.Lock()
	defer releaseMu.Unlock()

	if argon2InFlight.Add(-1) > 0 {
		return
	}

	if releaseTimer != nil {
		releaseTimer.Stop()
	}

	releaseFn := release

	var timer *time.Timer

	timer = time.AfterFunc(releaseDelay, func() {
		releaseMu.Lock()
		// A derivation that started after this timer was armed stops it and
		// clears it; Stop can lose that race once the func is already running,
		// so re-check under the lock before releasing.
		stale := releaseTimer != timer || argon2InFlight.Load() > 0
		if !stale {
			releaseTimer = nil
		}
		releaseMu.Unlock()

		if !stale {
			releaseFn()
		}
	})
	releaseTimer = timer
}
