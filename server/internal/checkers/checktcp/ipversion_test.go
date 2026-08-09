package checktcp_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checktcp"
)

// dualStackListener starts a wildcard TCP listener (so it answers on both
// loopback families) and returns its port, plus the resolved addresses of
// "localhost". It reports whether the host actually is dual-stack: a container
// with IPv6 disabled is a legitimate environment, and skipping there is honest —
// the family-selection logic itself is covered deterministically in
// checkerdef's SelectIPAddr tests.
func dualStackListener(t *testing.T) (int, bool) {
	t.Helper()

	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), "localhost")
	if err != nil {
		return 0, false
	}

	var hasV4, hasV6 bool

	for i := range addrs {
		if checkerdef.MatchesIPVersion(addrs[i].IP, checkerdef.IPVersionIPv4) {
			hasV4 = true
		} else {
			hasV6 = true
		}
	}

	if !hasV4 || !hasV6 {
		return 0, false
	}

	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		return 0, false
	}

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, false
	}

	return tcpAddr.Port, true
}

func runTCPCheck(t *testing.T, port int, version checkerdef.IPVersion) *checkerdef.Result {
	t.Helper()

	r := require.New(t)

	cfg := &checktcp.TCPConfig{}
	r.NoError(cfg.FromMap(map[string]any{"host": "localhost", "port": port, "timeout": "5s"}))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if version.Explicit() {
		ctx = checkerdef.WithIPVersion(ctx, version)
	}

	checker := &checktcp.TCPChecker{}

	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.NotNil(result)

	return result
}

// TestTCPDualStackTarget is the acceptance test for the feature as a user sees
// it: the SAME dual-stack target, checked over each family, reports the family
// it actually used — and an unpinned check keeps picking IPv4, exactly as it did
// before the option existed.
func TestTCPDualStackTarget(t *testing.T) {
	t.Parallel()

	port, ok := dualStackListener(t)
	if !ok {
		t.Skip("localhost is not dual-stack on this host")
	}

	r := require.New(t)

	ipv6 := runTCPCheck(t, port, checkerdef.IPVersionIPv6)
	r.Equal(checkerdef.StatusUp, ipv6.Status)
	r.Equal("ipv6", ipv6.Output[checkerdef.OutputKeyIPVersion])

	ipv4 := runTCPCheck(t, port, checkerdef.IPVersionIPv4)
	r.Equal(checkerdef.StatusUp, ipv4.Status)
	r.Equal("ipv4", ipv4.Output[checkerdef.OutputKeyIPVersion])

	// The backward-compatibility half: unpinned still means IPv4-first on a
	// dual-stack target. Flipping this would change every existing check.
	auto := runTCPCheck(t, port, checkerdef.IPVersionAuto)
	r.Equal(checkerdef.StatusUp, auto.Status)
	r.Equal("ipv4", auto.Output[checkerdef.OutputKeyIPVersion])
}
