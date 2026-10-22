package discoverytask

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// countingListener starts a TCP listener on 127.0.0.1 that accepts and closes
// every connection, counting them (the discovery package's test helper).
func countingListener(t *testing.T) (int, *atomic.Int64) {
	t.Helper()

	lc := net.ListenConfig{}

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	var accepted atomic.Int64

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			accepted.Add(1)

			_ = conn.Close()
		}
	}()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return tcpAddr.Port, &accepted
}

// fakeReporter records the finish body.
type fakeReporter struct {
	mu   sync.Mutex
	body json.RawMessage
}

func (*fakeReporter) Heartbeat(context.Context) error                        { return nil }
func (*fakeReporter) Step(context.Context, workertasks.StepRequest) error    { return nil }
func (*fakeReporter) Fail(context.Context, string) error                     { return nil }
func (f *fakeReporter) Finish(_ context.Context, body json.RawMessage) error { f.set(body); return nil }

func (f *fakeReporter) set(body json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.body = body
}

func (f *fakeReporter) result(t *testing.T) Result {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotNil(t, f.body, "the executor must finish the chunk")

	var res Result
	require.NoError(t, json.Unmarshal(f.body, &res))

	return res
}

func loopbackExecutor() *Executor {
	return NewExecutor([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
}

func runChunk(ctx context.Context, t *testing.T, exec *Executor, input Input) (Result, string) {
	t.Helper()

	raw, err := json.Marshal(input)
	require.NoError(t, err)

	rep := &fakeReporter{}
	require.NoError(t, exec.Run(ctx, &workertasks.Claim{UID: "task-1", Kind: Kind, Input: raw}, rep))

	return rep.result(t), string(rep.body)
}

func TestExecutorScansAllowedChunk(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	port, accepted := countingListener(t)

	res, body := runChunk(t.Context(), t, loopbackExecutor(), Input{CIDRs: []string{"127.0.0.1/32"}, Ports: []int{port}})
	r.Empty(res.ErrorCode)
	r.False(res.Partial)
	r.Len(res.Hosts, 1)
	r.Equal("127.0.0.1", res.Hosts[0].IP)
	r.Equal([]int{port}, res.Hosts[0].OpenPorts)
	r.NotContains(body, "suggestedChecks", "the agent reports raw facts only")
	r.Eventually(func() bool { return accepted.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
}

func TestExecutorRefusesOutsideAllowList(t *testing.T) {
	t.Parallel()

	port, accepted := countingListener(t)

	tests := []struct {
		name  string
		cidrs []string
		ports []int
		code  string
		exec  *Executor
	}{
		{name: "outside the list", cidrs: []string{"127.0.0.2/32"}, code: CodeNotAllowed},
		{name: "wider than the list", cidrs: []string{"127.0.0.0/30"}, code: CodeNotAllowed},
		{name: "one bad cidr among good ones", cidrs: []string{"127.0.0.1/32", "10.0.0.0/24"}, code: CodeNotAllowed},
		{name: "host bits", cidrs: []string{"127.0.0.1/31"}, code: CodeInvalidChunk},
		{name: "4-in-6", cidrs: []string{"::ffff:127.0.0.1/128"}, code: CodeInvalidChunk},
		{name: "no cidr", code: CodeInvalidChunk},
		{name: "bad port", cidrs: []string{"127.0.0.1/32"}, ports: []int{0}, code: CodeInvalidChunk},
		{name: "no allow-list", cidrs: []string{"127.0.0.1/32"}, code: CodeDisabled, exec: NewExecutor(nil)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			exec := tc.exec
			if exec == nil {
				exec = loopbackExecutor()
			}

			ports := tc.ports
			if ports == nil {
				ports = []int{port}
			}

			res, _ := runChunk(t.Context(), t, exec, Input{CIDRs: tc.cidrs, Ports: ports})
			r.Equal(tc.code, res.ErrorCode)
			r.Empty(res.Hosts)
		})
	}

	// Accept runs on the listener goroutine; give a stray dial time to land.
	time.Sleep(100 * time.Millisecond)
	require.Zero(t, accepted.Load(), "a refused chunk must not be dialed")
}

// A 10.0.0.5/20 chunk (host bits) and a ::ffff:10.0.0.0/116 chunk (4-in-6) are
// refused against a 10.0.0.0/24 list, and a /20 chunk is wider than the list.
func TestExecutorRefusesAgainstPrivateList(t *testing.T) {
	t.Parallel()

	exec := NewExecutor([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")})

	for _, cidr := range []string{"10.0.0.5/20", "::ffff:10.0.0.0/116", "10.0.0.0/20"} {
		res, _ := runChunk(t.Context(), t, exec, Input{CIDRs: []string{cidr}, Ports: []int{22}})
		require.NotEmpty(t, res.ErrorCode, cidr)
		require.Empty(t, res.Hosts, cidr)
	}
}

func TestExecutorDeduplicatesAddresses(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	port, accepted := countingListener(t)

	cidrs := make([]string, 4096)
	for i := range cidrs {
		cidrs[i] = "127.0.0.1/32"
	}

	res, _ := runChunk(t.Context(), t, loopbackExecutor(), Input{CIDRs: cidrs, Ports: []int{port, port}})
	r.Empty(res.ErrorCode)
	r.Len(res.Hosts, 1)
	r.Eventually(func() bool { return accepted.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	r.Equal(int64(1), accepted.Load(), "one address, one port, one dial")
}

func TestExecutorRefusesUnderEnforcingGuard(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	port, accepted := countingListener(t)

	ctx := egress.WithGuard(t.Context(), egress.New(false))
	res, _ := runChunk(ctx, t, loopbackExecutor(), Input{CIDRs: []string{"127.0.0.1/32"}, Ports: []int{port}})
	r.Equal(CodeEgressRefused, res.ErrorCode)
	r.Zero(accepted.Load())

	// Control: a permissive guard scans.
	ctx = egress.WithGuard(t.Context(), egress.New(true))
	res, _ = runChunk(ctx, t, loopbackExecutor(), Input{CIDRs: []string{"127.0.0.1/32"}, Ports: []int{port}})
	r.Empty(res.ErrorCode)
	r.Len(res.Hosts, 1)
}

func TestExecutorCanceledIsPartial(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	port, _ := countingListener(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res, _ := runChunk(ctx, t, loopbackExecutor(), Input{CIDRs: []string{"127.0.0.1/32"}, Ports: []int{port}})
	r.Empty(res.ErrorCode)
	r.True(res.Partial)
}

func TestValidatePortsCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ports := make([]int, 0, MaxPorts+1)
	for i := 1; i <= MaxPorts+1; i++ {
		ports = append(ports, i)
	}

	_, err := validatePorts(ports)
	r.ErrorIs(err, errInvalidChunk)

	out, err := validatePorts(ports[:MaxPorts])
	r.NoError(err)
	r.Len(out, MaxPorts)

	_, err = validatePorts([]int{70000})
	r.True(err != nil && strings.Contains(err.Error(), "70000"))
}

// The executor's capability is the one the regions service publishes and the
// agent socket strips from system agents.
func TestCapabilityIsPublished(t *testing.T) {
	t.Parallel()

	require.Equal(t, Capability, regions.CapabilityDiscovery)
	require.Equal(t, []string{"ipv4"}, regions.StripTaskCapabilities([]string{"ipv4", Capability}))
}
