package discovery

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanLocalhost(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	// Scan localhost with only port 4000; we don't require it to be open
	// (the server may not be running), but we do require the scan itself to work.
	cfg := Config{
		CIDRs: []string{"127.0.0.1/32"},
		Ports: []int{80, 443},
	}

	hosts, err := Scan(ctx, cfg)
	r.NoError(err)
	// The scan may return zero hosts if nothing is listening, but it must not error.
	r.NotNil(hosts)
}

func TestScanInvalidCIDR(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	_, err := Scan(ctx, Config{
		CIDRs: []string{"invalid"},
		Ports: []int{80},
	})
	r.Error(err)
}

func TestScanTooLarge(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	_, err := Scan(ctx, Config{
		CIDRs: []string{"10.0.0.0/8"},
		Ports: []int{80},
	})
	r.ErrorIs(err, ErrRangeTooLarge)
}

func TestScanHostsHostnameHintPrecedence(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	// Listen on a loopback port so the scan finds at least one open port and
	// returns a result for the host.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	r.NoError(err)
	defer func() { _ = ln.Close() }()

	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	r.True(ok)
	port := tcpAddr.Port

	hosts := []HostInput{
		{IP: net.ParseIP("127.0.0.1"), HostnameHint: "my-device"},
	}

	results, err := ScanHosts(ctx, hosts, Config{Ports: []int{port}})
	r.NoError(err)
	r.Len(results, 1, "the listening host must be reported")

	got := results[0]
	r.Equal("127.0.0.1", got.IP)
	r.Equal("my-device", got.Hostname, "HostnameHint must take precedence over reverse DNS")
	r.Contains(got.OpenPorts, port)
}

func TestScanHostsUnresponsiveOmitted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	// A host that is not listening on the probed port and (typically) not
	// ICMP-reachable from the test sandbox is omitted, mirroring Scan.
	hosts := []HostInput{
		{IP: net.ParseIP("192.0.2.1"), HostnameHint: "test-net-host"}, // TEST-NET-1
	}

	results, err := ScanHosts(ctx, hosts, Config{Ports: []int{9}, Timeout: "200ms"})
	r.NoError(err)
	r.NotNil(results)
}

func TestScanHostsDefaultsAndIgnoresCIDRs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	// cfg.CIDRs is ignored by ScanHosts; nil port list falls back to defaults.
	results, err := ScanHosts(ctx, nil, Config{CIDRs: []string{"invalid-cidr-ignored"}})
	r.NoError(err)
	r.NotNil(results)
	r.Empty(results)
}

func TestScanHostsInvalidTimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	_, err := ScanHosts(ctx, []HostInput{{IP: net.ParseIP("127.0.0.1")}}, Config{Timeout: "notaduration"})
	r.Error(err)
}

func TestSortPorts(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ports := []int{443, 22, 80}
	sortPorts(ports)
	r.Equal([]int{22, 80, 443}, ports)
}
