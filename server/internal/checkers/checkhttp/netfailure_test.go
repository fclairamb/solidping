package checkhttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestHTTP500CarriesNoNetworkFailureMarker is THE negative of this feature.
//
// "Application-level failures do not trigger a trace" is the rule the spec is
// most explicit about, and a 500 is its canonical case: the check is DOWN, the
// incident opens, and the path is provably fine because the server answered.
// A traceroute here would be pure noise on the incident page.
func TestHTTP500CarriesNoNetworkFailureMarker(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{URL: server.URL, Method: http.MethodGet}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Positive control on the OUTCOME: this really is a failing check, so the
	// absence of a marker below cannot be explained by "nothing failed".
	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the 500 to fail the check, got %v", result.Status)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("an HTTP 500 minted a network-failure marker: %+v", result.Diagnostics.NetworkFailure)
	}
}

// TestHTTPKeywordMismatchCarriesNoNetworkFailureMarker — the second
// application-level class the spec names. A 200 whose body is wrong is still
// the server answering.
func TestHTTPKeywordMismatchCarriesNoNetworkFailureMarker(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("all systems nominal"))
	}))
	defer server.Close()

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{
		URL:        server.URL,
		Method:     http.MethodGet,
		BodyExpect: "this string is not in the body",
	}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the keyword mismatch to fail the check, got %v", result.Status)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a keyword mismatch minted a marker: %+v", result.Diagnostics.NetworkFailure)
	}
}

// TestHTTPRefusedConnectionCarriesANetworkFailureMarker is the positive control
// for both negatives above: the same checker, on the same host, DOES mint a
// marker when nothing is listening — so the empty results above are the rule
// working, not the wiring being absent.
func TestHTTPRefusedConnectionCarriesANetworkFailureMarker(t *testing.T) {
	t.Parallel()

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	port := tcpPort(t, listener)

	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{
		URL:    fmt.Sprintf("http://127.0.0.1:%d/", port),
		Method: http.MethodGet,
	}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Diagnostics == nil || result.Diagnostics.NetworkFailure == nil {
		t.Fatalf("a refused HTTP connect carried no marker: %+v", result.Diagnostics)
	}

	failure := result.Diagnostics.NetworkFailure
	if failure.Class != checkerdef.NetFailureConnectionRefused {
		t.Fatalf("class = %q, want %q", failure.Class, checkerdef.NetFailureConnectionRefused)
	}

	// The address comes from the httptrace ConnectDone hook, which is the only
	// place it is observable when the connection never comes up.
	if failure.Address != "127.0.0.1" || failure.Port != port {
		t.Fatalf("marker endpoint = %s:%d, want 127.0.0.1:%d", failure.Address, failure.Port, port)
	}

	if failure.Host != "127.0.0.1" {
		t.Fatalf("marker host = %q, want the URL hostname", failure.Host)
	}
}

// TestHTTPUnresolvableHostCarriesNoNetworkFailureMarker: a name that does not
// resolve has no address to trace to. DNS diagnostics are explicitly a
// different capture with its own spec.
func TestHTTPUnresolvableHostCarriesNoNetworkFailureMarker(t *testing.T) {
	t.Parallel()

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{
		URL:    "http://this-host-does-not-exist.invalid/",
		Method: http.MethodGet,
	}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the unresolvable host to fail the check, got %v", result.Status)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a DNS failure minted a marker: %+v", result.Diagnostics.NetworkFailure)
	}
}

// tcpPort reads a listener's bound port, checked rather than asserted.
func tcpPort(t *testing.T, listener net.Listener) int {
	t.Helper()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is not TCP: %T", listener.Addr())
	}

	return addr.Port
}

// TestResponseStallCarriesNoNetworkFailureMarker is the correction the audit
// asked for: a target that completes the TCP handshake and then hangs is an
// APPLICATION stall, not a reachability failure.
//
// It is the sharpest case in this file because the raw fact available to the
// checker — `ctx.Err() == DeadlineExceeded` — is byte-for-byte identical to the
// one a black-holed SYN produces. Only how far the request got tells them
// apart, and getting it wrong attaches a hop list labeled `connect-timeout` to
// an outage whose path is provably fine.
func TestResponseStallCarriesNoNetworkFailureMarker(t *testing.T) {
	t.Parallel()

	stalled := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-stalled
	}))

	t.Cleanup(func() {
		close(stalled)
		server.Close()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	checker := &HTTPChecker{}
	result, err := checker.Execute(ctx, &HTTPConfig{URL: server.URL, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Positive control on the OUTCOME: the check really did time out, so the
	// missing marker is the rule working and not a probe that quietly passed.
	if result.Status != checkerdef.StatusTimeout {
		t.Fatalf("expected a timeout, got %v (%v)", result.Status, result.Output)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a response stall minted a %q marker: %+v",
			result.Diagnostics.NetworkFailure.Class, result.Diagnostics.NetworkFailure)
	}
}

// TestConnectTimeoutStillCarriesAMarker is the other direction, and the control
// that keeps the stall test above from passing for the wrong reason: a deadline
// that fires while a connection is still being established IS a reachability
// failure and must still be traced, with the address it was dialing.
//
// DRIVEN THROUGH THE httptrace HOOKS RATHER THAN THROUGH A REAL BLACK HOLE.
// The obvious version of this test points the checker at a reserved,
// unrouted address and waits — and it fails on any machine whose network
// disagrees: a host with no default route, or a firewall answering ICMP
// admin-prohibited, produces `host-unreachable` instead and the assertion
// breaks. A test whose result depends on the network the machine happens to sit
// on is a bug, not a flake, and the seam to avoid it is right here: the tracker
// is the only thing that knows how far a request got, so replaying the exact
// hook sequence a black-holed dial produces pins the behavior exactly and
// hermetically.
func TestConnectTimeoutStillCarriesAMarker(t *testing.T) {
	t.Parallel()

	tracker := &connFamilyTracker{}
	trace := tracker.clientTrace()

	// A dial begins and never completes: the deadline fires first, so
	// ConnectDone either never lands or lands after we read the tracker.
	trace.ConnectStart("tcp", "192.0.2.10:443")

	if got := tracker.lastPhase(); got != phaseNone {
		t.Fatalf("phase = %v, want phaseNone for a dial still in flight", got)
	}

	result := &checkerdef.Result{}
	result.SetNetworkFailure(checkerdef.NewNetworkFailure(
		checkerdef.NetFailureConnectTimeout, "acme.com", "", 0))

	refineHTTPNetworkFailure(result, tracker.lastPhase())
	locateHTTPNetworkFailure(result, tracker.dialedAddr())

	if result.Diagnostics == nil || result.Diagnostics.NetworkFailure == nil {
		t.Fatalf("a connect that never completed lost its marker: %+v", result.Diagnostics)
	}

	failure := result.Diagnostics.NetworkFailure
	if failure.Class != checkerdef.NetFailureConnectTimeout {
		t.Fatalf("class = %q, want %q", failure.Class, checkerdef.NetFailureConnectTimeout)
	}

	// Recording the address at dial START is what makes this survive: without
	// it the marker reaches locate with no endpoint and is dropped for having
	// nothing to trace to.
	if failure.Address != "192.0.2.10" || failure.Port != 443 {
		t.Fatalf("marker endpoint = %s:%d, want 192.0.2.10:443", failure.Address, failure.Port)
	}
}

// TestRedirectChainKeepsTheSecondHopsConnectTimeout is the bug the phase reset
// exists for, and the everyday shape of it is mundane: `http://acme.com`
// answers on port 80, redirects to `https://acme.com`, and 443 is filtered.
//
// Hop 1 connects, so phase reaches phaseConnected. Hop 2's dial black-holes, so
// its ConnectDone races the deadline and may never be observed. Without
// ConnectStart resetting the phase, the stale phaseConnected from hop 1 makes
// refineHTTPNetworkFailure read the failure as an application stall and drop
// the marker — silently disabling the trace for a genuinely broken path.
func TestRedirectChainKeepsTheSecondHopsConnectTimeout(t *testing.T) {
	t.Parallel()

	tracker := &connFamilyTracker{}
	trace := tracker.clientTrace()

	// Hop 1: port 80 answers and serves the redirect.
	trace.ConnectStart("tcp", "192.0.2.10:80")
	trace.ConnectDone("tcp", "192.0.2.10:80", nil)

	if got := tracker.lastPhase(); got != phaseConnected {
		t.Fatalf("phase after a successful connect = %v, want phaseConnected", got)
	}

	// Hop 2: port 443 is filtered. The dial starts and nothing comes back.
	trace.ConnectStart("tcp", "192.0.2.10:443")

	if got := tracker.lastPhase(); got != phaseNone {
		t.Fatalf("phase = %v: a new dial must not inherit the previous hop's success", got)
	}

	result := &checkerdef.Result{}
	result.SetNetworkFailure(checkerdef.NewNetworkFailure(
		checkerdef.NetFailureConnectTimeout, "acme.com", "", 0))

	refineHTTPNetworkFailure(result, tracker.lastPhase())
	locateHTTPNetworkFailure(result, tracker.dialedAddr())

	if result.Diagnostics == nil || result.Diagnostics.NetworkFailure == nil {
		t.Fatalf("the second hop's connect timeout was dropped as an application stall")
	}

	failure := result.Diagnostics.NetworkFailure
	if failure.Class != checkerdef.NetFailureConnectTimeout {
		t.Fatalf("class = %q, want %q", failure.Class, checkerdef.NetFailureConnectTimeout)
	}

	// The trace must point at the hop that BROKE, not the one that worked.
	if failure.Port != 443 {
		t.Fatalf("marker port = %d, want the failing hop's 443", failure.Port)
	}
}

// TestRedirectChainStillDropsAStallOnTheSecondHop is the control for the reset:
// it must not become a blanket "always keep the marker". When hop 2 CONNECTS
// and then hangs, that is still an application stall and still gets no trace.
func TestRedirectChainStillDropsAStallOnTheSecondHop(t *testing.T) {
	t.Parallel()

	tracker := &connFamilyTracker{}
	trace := tracker.clientTrace()

	trace.ConnectStart("tcp", "192.0.2.10:80")
	trace.ConnectDone("tcp", "192.0.2.10:80", nil)
	trace.ConnectStart("tcp", "192.0.2.11:80")
	trace.ConnectDone("tcp", "192.0.2.11:80", nil)

	result := &checkerdef.Result{}
	result.SetNetworkFailure(checkerdef.NewNetworkFailure(
		checkerdef.NetFailureConnectTimeout, "acme.com", "", 0))

	refineHTTPNetworkFailure(result, tracker.lastPhase())

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a stall after the second hop connected was traced: %+v",
			result.Diagnostics.NetworkFailure)
	}
}

// TestRedirectToAClosedPortCarriesTheSecondHopsAddress drives a REAL redirect
// through the real transport, hermetically: hop 2 is a loopback port with
// nothing listening, so the connect is refused rather than black-holed.
//
// It cannot exercise the timeout branch (a refusal is not a timeout), but it is
// the end-to-end proof of the other half of "the last attempt wins" — that a
// redirect really does re-dial and that the marker follows the hop that failed
// rather than the one that worked.
func TestRedirectToAClosedPortCarriesTheSecondHopsAddress(t *testing.T) {
	t.Parallel()

	var listenConfig net.ListenConfig

	closed, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	deadPort := tcpPort(t, closed)

	if closeErr := closed.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		http.Redirect(writer, req,
			fmt.Sprintf("http://127.0.0.1:%d/next", deadPort), http.StatusFound)
	}))
	defer server.Close()

	checker := &HTTPChecker{}

	result, err := checker.Execute(t.Context(), &HTTPConfig{URL: server.URL, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the redirect target to fail, got %v", result.Status)
	}

	if result.Diagnostics == nil || result.Diagnostics.NetworkFailure == nil {
		t.Fatalf("the second hop's refusal carried no marker: %+v", result.Diagnostics)
	}

	failure := result.Diagnostics.NetworkFailure
	if failure.Class != checkerdef.NetFailureConnectionRefused {
		t.Fatalf("class = %q, want %q", failure.Class, checkerdef.NetFailureConnectionRefused)
	}

	if failure.Port != deadPort {
		t.Fatalf("marker port = %d, want the failing hop's %d (the first hop answered fine)",
			failure.Port, deadPort)
	}
}

// TestRefineHTTPNetworkFailure drives the phase rules directly, so every branch
// is pinned without needing a network shape that produces it.
func TestRefineHTTPNetworkFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		start string
		phase requestPhase
		want  string
	}{
		{
			"nothing connected: a real connect timeout",
			checkerdef.NetFailureConnectTimeout, phaseNone,
			checkerdef.NetFailureConnectTimeout,
		},
		{
			"the last connect failed: a real connect timeout",
			checkerdef.NetFailureConnectTimeout, phaseConnectFailed,
			checkerdef.NetFailureConnectTimeout,
		},
		{
			"the handshake stalled: reclassified, still traceable",
			checkerdef.NetFailureConnectTimeout, phaseTLSHandshaking,
			checkerdef.NetFailureTLSHandshakeTimeout,
		},
		{
			"the connection came up: the marker is dropped entirely",
			checkerdef.NetFailureConnectTimeout, phaseConnected,
			"",
		},
		{
			"a refusal is never second-guessed",
			checkerdef.NetFailureConnectionRefused, phaseConnected,
			checkerdef.NetFailureConnectionRefused,
		},
		{
			"an unreachable is never second-guessed",
			checkerdef.NetFailureHostUnreachable, phaseConnected,
			checkerdef.NetFailureHostUnreachable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := &checkerdef.Result{}
			result.SetNetworkFailure(checkerdef.NewNetworkFailure(test.start, "acme.com", "10.0.0.1", 443))

			refineHTTPNetworkFailure(result, test.phase)

			got := ""
			if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
				got = result.Diagnostics.NetworkFailure.Class
			}

			if got != test.want {
				t.Fatalf("class = %q, want %q", got, test.want)
			}
		})
	}
}

// TestStallOnAPooledConnectionIsNotTraced closes the one gap the phase reset
// opens up, and it is worth pinning because it is a real production path: HTTP
// checks with no tunnel, no `verifySsl:false` and no pinned IP family share
// http.DefaultTransport, so a request can be served by a connection some
// EARLIER check established.
//
// Such a request produces no ConnectStart and no ConnectDone, so `phase` reads
// phaseNone — "a dial has not completed" — and refine keeps the timeout class,
// which on its own would trace an application stall. What stops it is the
// second gate: no dial means no address, and a marker with nothing to trace to
// is dropped. Two independent conditions have to hold for a trace to happen,
// and this is the one that carries it here.
func TestStallOnAPooledConnectionIsNotTraced(t *testing.T) {
	t.Parallel()

	tracker := &connFamilyTracker{}

	// No hooks fire at all: the connection came out of the pool.
	if got := tracker.lastPhase(); got != phaseNone {
		t.Fatalf("phase = %v, want phaseNone", got)
	}

	result := &checkerdef.Result{}
	result.SetNetworkFailure(checkerdef.NewNetworkFailure(
		checkerdef.NetFailureConnectTimeout, "acme.com", "", 0))

	refineHTTPNetworkFailure(result, tracker.lastPhase())

	// Refine alone keeps it — phaseNone reads as "never connected".
	if result.Diagnostics.NetworkFailure == nil {
		t.Fatal("refine dropped the marker; this test is asserting the wrong gate")
	}

	locateHTTPNetworkFailure(result, tracker.dialedAddr())

	if result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a stall on a pooled connection was traced: %+v", result.Diagnostics.NetworkFailure)
	}
}
