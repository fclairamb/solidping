package checkerdef_test

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestProbeEgressReportsBothFamilies pins the dual-stack host: both families
// answer true. Driven through the injected probe seam rather than the CI
// runner's real networking, which is exactly why that seam exists.
func TestProbeEgressReportsBothFamilies(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	families := checkerdef.ProbeEgressWith(func(_ checkerdef.IPVersion, _ net.IP) error {
		return nil
	})

	r.True(families[checkerdef.IPVersionIPv4])
	r.True(families[checkerdef.IPVersionIPv6])
}

// TestProbeEgressV4OnlyHost is the case the whole feature exists for: the host
// routes IPv4 and has no IPv6 route at all.
func TestProbeEgressV4OnlyHost(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	families := checkerdef.ProbeEgressWith(func(version checkerdef.IPVersion, _ net.IP) error {
		if version == checkerdef.IPVersionIPv6 {
			return errors.New("network is unreachable") //nolint:err113 // fake probe diagnostic
		}

		return nil
	})

	r.True(families[checkerdef.IPVersionIPv4])
	r.False(families[checkerdef.IPVersionIPv6])
}

// TestProbeEgressTargetsAreOffLink guards the property the probe depends on:
// the addresses must not be loopback, unspecified or link-local, or the route
// lookup would short-circuit and report egress that does not exist.
func TestProbeEgressTargetsAreOffLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	seen := map[checkerdef.IPVersion]net.IP{}

	checkerdef.ProbeEgressWith(func(version checkerdef.IPVersion, ip net.IP) error {
		seen[version] = ip

		return nil
	})

	r.Len(seen, 2)

	for version, ip := range seen {
		r.NotNil(ip, "%s probe target must parse", version)
		r.False(ip.IsLoopback(), "%s probe target must not be loopback", version)
		r.False(ip.IsUnspecified(), "%s probe target must not be unspecified", version)
		r.False(ip.IsLinkLocalUnicast(), "%s probe target must not be link-local", version)
		r.True(checkerdef.MatchesIPVersion(ip, version), "%s probe target must be of that family", version)
	}
}

// TestEgressCacheReprobesAfterTTL pins "probed at report time, not at process
// start": a host that gains IPv6 must start advertising it without a restart.
func TestEgressCacheReprobesAfterTTL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hasV6 := false
	calls := 0

	cache := checkerdef.NewEgressCacheWith(time.Millisecond, func() map[checkerdef.IPVersion]bool {
		calls++

		return map[checkerdef.IPVersion]bool{checkerdef.IPVersionIPv6: hasV6}
	})

	r.False(cache.Get()[checkerdef.IPVersionIPv6])
	r.Equal(1, calls)

	// Within the TTL the cached answer is reused.
	r.False(cache.Get()[checkerdef.IPVersionIPv6])
	r.Equal(1, calls)

	hasV6 = true

	require.Eventually(t, func() bool {
		return cache.Get()[checkerdef.IPVersionIPv6]
	}, time.Second, 2*time.Millisecond, "a host that gains IPv6 must converge without a restart")
}

// TestEgressCacheZeroTTLAlwaysProbes documents the degenerate configuration.
func TestEgressCacheZeroTTLAlwaysProbes(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	calls := 0
	cache := checkerdef.NewEgressCacheWith(0, func() map[checkerdef.IPVersion]bool {
		calls++

		return map[checkerdef.IPVersion]bool{}
	})

	cache.Get()
	cache.Get()

	r.Equal(2, calls)
}
