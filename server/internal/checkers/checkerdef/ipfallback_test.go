package checkerdef

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

type fakeConn struct {
	net.Conn
	remote net.Addr
}

func (c fakeConn) RemoteAddr() net.Addr { return c.remote }

func replayObserver() (*FamilyFallbackObserver, *time.Time, *httptrace.ClientTrace) {
	now := time.Unix(1000, 0)
	obs := &FamilyFallbackObserver{now: func() time.Time { return now }}

	return obs, &now, obs.ClientTrace()
}

func gotConn(trace *httptrace.ClientTrace, ip string) {
	trace.GotConn(httptrace.GotConnInfo{Conn: fakeConn{remote: &net.TCPAddr{IP: net.ParseIP(ip), Port: 443}}})
}

func TestFallbackObserverFailedFirstFamily(t *testing.T) {
	t.Parallel()

	obs, now, trace := replayObserver()

	trace.ConnectStart("tcp", "[2001:db8::1]:443")
	*now = now.Add(40 * time.Millisecond)
	trace.ConnectDone("tcp", "[2001:db8::1]:443", syscall.ENETUNREACH)
	trace.ConnectStart("tcp", "192.0.2.1:443")
	trace.ConnectDone("tcp", "192.0.2.1:443", nil)
	gotConn(trace, "192.0.2.1")

	fb := obs.Fallback()
	require.NotNil(t, fb)
	require.Equal(t, IPVersionIPv6, fb.Failed)
	require.Equal(t, 40*time.Millisecond, fb.After)
	require.Contains(t, fb.Reason, "unreachable")

	result := &Result{Status: StatusUp}
	fb.Apply(result)
	require.Equal(t, StatusWarning, result.Status)
	require.Equal(t, "IPv6 unreachable from this region, answered over IPv4", result.Output[OutputKeyMessage])

	detail, ok := result.Output[OutputKeyIPFallback].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "ipv6", detail["failed"])
	require.EqualValues(t, 40, detail["after_ms"])
}

func TestFallbackObserverPendingThreshold(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		wait time.Duration
		warn bool
	}{
		"won after 300 ms is just a slower v6": {300 * time.Millisecond, false},
		"still silent after 1 s":               {1200 * time.Millisecond, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			obs, now, trace := replayObserver()

			trace.ConnectStart("tcp", "[2001:db8::1]:443")
			*now = now.Add(tc.wait)
			trace.ConnectStart("tcp", "192.0.2.1:443")
			trace.ConnectDone("tcp", "192.0.2.1:443", nil)
			gotConn(trace, "192.0.2.1")
			// The loser is canceled by the winner: never a failure.
			trace.ConnectDone("tcp", "[2001:db8::1]:443", context.Canceled)

			if !tc.warn {
				require.Nil(t, obs.Fallback())

				return
			}

			require.NotNil(t, obs.Fallback())
			require.Equal(t, "timeout", obs.Fallback().Reason)
		})
	}
}

func TestFallbackObserverCleanAndFailureStatus(t *testing.T) {
	t.Parallel()

	obs, _, trace := replayObserver()
	trace.ConnectStart("tcp", "[2001:db8::1]:443")
	trace.ConnectDone("tcp", "[2001:db8::1]:443", nil)
	gotConn(trace, "2001:db8::1")
	require.Nil(t, obs.Fallback())

	// A fallback never upgrades a failure, and the output is still stamped.
	fb := &FamilyFallback{Failed: IPVersionIPv6, Reason: "timeout", After: time.Second}
	down := &Result{Status: StatusDown}
	fb.Apply(down)
	require.Equal(t, StatusDown, down.Status)
	require.Contains(t, down.Output, OutputKeyIPFallback)
	require.NotContains(t, down.Output, OutputKeyMessage)

	var nilFB *FamilyFallback
	nilFB.Apply(down) // no-op
}

// dualStackRequest runs a real request through the enforcing guard's Happy
// Eyeballs dialer against a "dual-stack" host (a v6 and a v4 address, the v4
// being the loopback listener) and returns what the observer saw.
func dualStackRequest(t *testing.T, control func(network string) error) *FamilyFallback {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(server.Close)

	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)

	var calls atomic.Int32

	guard := egress.New(false,
		egress.WithLookup(func(context.Context, string) ([]net.IPAddr, error) {
			calls.Add(1)

			return []net.IPAddr{{IP: net.ParseIP("2606:4700::1111")}, {IP: net.ParseIP("127.0.0.1")}}, nil
		}),
		egress.WithPublicOverride(net.ParseIP("127.0.0.1")),
	)
	base := &net.Dialer{
		Timeout: 30 * time.Second,
		Control: func(network, _ string, _ syscall.RawConn) error { return control(network) },
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return guard.DialContextWith(ctx, base, network, addr)
		},
	}}

	obs := &FamilyFallbackObserver{}
	ctx, cancel := context.WithTimeout(httptrace.WithClientTrace(t.Context(), obs.ClientTrace()), 10*time.Second)
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://dual.acme.com:"+port+"/", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	return obs.Fallback()
}

func TestFallbackGuardPath(t *testing.T) {
	t.Parallel()

	errV6 := errors.New("v6 path dropped") //nolint:err113 // test sentinel

	t.Run("v6 fails fast", func(t *testing.T) {
		t.Parallel()

		fb := dualStackRequest(t, func(network string) error {
			if network == "tcp6" {
				return errV6
			}

			return nil
		})
		require.NotNil(t, fb)
		require.Equal(t, IPVersionIPv6, fb.Failed)
		require.Contains(t, fb.Reason, "v6 path dropped")
	})

	t.Run("v6 silent, v4 slow to connect", func(t *testing.T) {
		t.Parallel()

		fb := dualStackRequest(t, func(network string) error {
			if network == "tcp6" {
				time.Sleep(5 * time.Second)

				return errV6
			}

			time.Sleep(1200 * time.Millisecond)

			return nil
		})
		require.NotNil(t, fb)
		require.Equal(t, "timeout", fb.Reason)
	})

	t.Run("clean dual-stack is not a fallback", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, dualStackRequest(t, func(string) error { return nil }))
	})
}
