package checkerdef_test

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// The two tests below are the negative controls for spec 2026-08-15-11's
// central non-goal: ADVERTISED CAPABILITY NEVER GATES EXECUTION. They live
// beside the probe rather than beside the aggregation on purpose — the property
// they defend is that the run-time path has no knowledge of the advertised
// value at all, so the only way to break them is to introduce that coupling.

// TestAdvertisedNoDoesNotBlockARealIPv6Run is AC7. A region may advertise "no"
// (its workers last reported before an IPv6 route was added, or the heartbeat
// is simply stale) while the host in fact has IPv6. The check must run and
// pass: the pre-flight probe is the authority, and it is the only thing
// consulted here.
func TestAdvertisedNoDoesNotBlockARealIPv6Run(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// The host really does have IPv6 — the live route lookup succeeds.
	liveProbe := func(_ checkerdef.IPVersion, _ net.IP) error { return nil }

	ip, err := checkerdef.SelectIPAddrWithProbe(
		"example.com",
		[]net.IPAddr{{IP: net.ParseIP("1.2.3.4")}, {IP: net.ParseIP("2001:db8::5")}},
		checkerdef.IPVersionIPv6,
		liveProbe,
	)

	r.NoError(err, "a stale advertised capability must never block a run")
	r.Equal("2001:db8::5", ip.String())

	// Positive control: the SAME inputs with a probe that reports no route do
	// fail, so the assertion above is about the run-time probe being the only
	// authority — not about a probe that never runs.
	deadProbe := func(_ checkerdef.IPVersion, _ net.IP) error {
		return errWorkerNoEgress(errors.New("network is unreachable")) //nolint:err113 // fake probe diagnostic
	}

	_, deadErr := checkerdef.SelectIPAddrWithProbe(
		"example.com",
		[]net.IPAddr{{IP: net.ParseIP("1.2.3.4")}, {IP: net.ParseIP("2001:db8::5")}},
		checkerdef.IPVersionIPv6,
		deadProbe,
	)
	r.ErrorIs(deadErr, checkerdef.ErrWorkerNoEgress)
}

// TestLosingIPv6BetweenHeartbeatsStillReportsWorkerNoEgress is AC8, pinned
// against regression. A worker whose route disappears since its last capability
// report must still produce ErrWorkerNoEgress — which the status mapper turns
// into StatusError ("our worker could not send this"), NOT StatusDown ("your
// target is broken"). Advertising "yes" must not let a real egress failure be
// reported as the customer's outage.
func TestLosingIPv6BetweenHeartbeatsStillReportsWorkerNoEgress(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// The route went away after the last heartbeat said "yes".
	goneProbe := func(version checkerdef.IPVersion, ip net.IP) error {
		if version != checkerdef.IPVersionIPv6 {
			return nil
		}

		return errors.New( //nolint:err113 // fake probe diagnostic, mirrors the kernel string
			"dial udp6 " + ip.String() + ": connect: " + syscall.ENETUNREACH.Error(),
		)
	}

	// Wrapped exactly as the production probe wraps it, so the sentinel and the
	// status mapping are the real ones.
	wrapped := func(version checkerdef.IPVersion, ip net.IP) error {
		if err := goneProbe(version, ip); err != nil {
			return errWorkerNoEgress(err)
		}

		return nil
	}

	_, err := checkerdef.SelectIPAddrWithProbe(
		"example.com",
		[]net.IPAddr{{IP: net.ParseIP("2001:db8::5")}},
		checkerdef.IPVersionIPv6,
		wrapped,
	)

	r.Error(err)
	r.ErrorIs(err, checkerdef.ErrWorkerNoEgress)
	r.NotErrorIs(err, checkerdef.ErrNoAddressForFamily,
		"a missing route is not the target missing an AAAA record")

	// The verdict, not just the sentinel: this must never surface as DOWN.
	r.Equal(checkerdef.StatusError, checkerdef.ResolveFailureStatus(err, checkerdef.StatusDown))
}

// errWorkerNoEgress mirrors DialEgressProbe's wrapping so the test asserts on
// the real sentinel rather than a lookalike.
func errWorkerNoEgress(cause error) error {
	return errors.Join(checkerdef.ErrWorkerNoEgress, cause)
}
