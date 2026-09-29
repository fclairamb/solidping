package agentws

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Spec 2026-09-28-03 §1: stale → dead only when a second consecutive probe
// cycle passes with no observation at all. These drive the decision directly,
// with sensor sequence numbers instead of wall-clock time.

// TestProbeFailedCountsObservationsWhileTheProbeWasOutstanding pins the cycle
// boundary: a frame that lands while an unanswered probe is still outstanding
// belongs to that probe's cycle. Snapshotting the stale marker when the probe
// FAILED (instead of when it was sent) discarded it, and killed a peer whose
// frames were only a little over one interval apart.
func TestProbeFailedCountsObservationsWhileTheProbeWasOutstanding(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var keep keepalive

	// Probe 1 goes out at seq 0; a frame lands while it is outstanding (seq 1);
	// no pong: stale.
	r.False(keep.probeFailed(0, 1))
	r.True(keep.stale)

	// Probe 2 goes out at seq 1; nothing new is observed; no pong. The frame
	// seen during probe 1 proves the peer alive in that cycle: only ONE cycle
	// has been silent, so this is stale again, not dead.
	r.False(keep.probeFailed(1, 1), "a frame observed while probe 1 was outstanding must count")
	r.True(keep.stale)

	// Probe 3 goes out at seq 1; still nothing: two consecutive silent cycles.
	r.True(keep.probeFailed(1, 1))
}

func TestProbeFailedSilentPeerDiesOnTheSecondMiss(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var keep keepalive

	r.False(keep.probeFailed(7, 7), "the first miss is stale, never dead")
	r.True(keep.probeFailed(7, 7), "the second silent cycle is dead")
}

func TestProbeFailedAnyObservationSinceTheStaleProbeKeepsItAlive(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var keep keepalive

	r.False(keep.probeFailed(3, 3))
	// A frame after probe 1 was sent (seq 4), then probe 2 goes out and fails.
	r.False(keep.probeFailed(4, 4))
	r.False(keep.probeFailed(4, 5), "observed after probe 2 was sent")
	// Seq 5 was observed while probe 3 was outstanding: that cycle was not
	// silent either.
	r.False(keep.probeFailed(5, 5))
	// Probe 5 sent at seq 5, nothing since probe 4 went out: dead.
	r.True(keep.probeFailed(5, 5))
}

func TestProbeAnsweredClearsStale(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var keep keepalive

	r.False(keep.probeFailed(2, 2))
	keep.probeAnswered()
	r.False(keep.stale)
	r.False(keep.probeFailed(2, 2), "after an answer, a miss starts a new stale cycle")
}
