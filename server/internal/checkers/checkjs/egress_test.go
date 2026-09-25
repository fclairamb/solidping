package checkjs

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

const jsInternalSecret = "internal-metadata-secret"

// runEgressScript runs a script with the given egress guard on its context.
func runEgressScript(t *testing.T, guard *egress.Guard, script string) *checkerdef.Result {
	t.Helper()

	ctx := egress.WithGuard(context.Background(), guard)

	result, err := (&JSChecker{}).Execute(ctx, &JSConfig{Script: script})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

// A script's http.get towards a loopback service is refused under an
// enforcing policy: the request's error carries the policy message and no
// byte of the internal response ever reaches the script (and so the result).
func TestScriptHTTPGetIsRefusedUnderAnEnforcingPolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(jsInternalSecret))
	}))
	t.Cleanup(server.Close)

	script := fmt.Sprintf(`
var res = http.get(%q);
return { status: res.error ? "down" : "up", output: { error: res.error, body: res.body } };
`, server.URL+"/latest/meta-data/iam/")

	denied := runEgressScript(t, egress.New(false), script)
	r.Contains(fmt.Sprint(denied.Output["error"]), "denied by egress policy", denied.Output)
	r.Nil(denied.Output["body"], "no body in output")
	r.NotContains(fmt.Sprint(denied.Output), jsInternalSecret)

	// Positive control: same script, permissive policy.
	allowed := runEgressScript(t, egress.New(true), script)
	r.Equal(checkerdef.StatusUp, allowed.Status, allowed.Output)
	r.Equal(jsInternalSecret, allowed.Output["body"])
}

// ipv6LoopbackServer starts an httptest server on the IPv6 loopback address
// instead of the default IPv4 one — see checkhttp's copy of this helper for
// why: it stands in for a "second address" a redirect test needs, since a
// second real IPv4 loopback address (127.0.0.2) is not portably bindable
// (macOS refuses it) while ::1 is guaranteed everywhere.
func ipv6LoopbackServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	server := httptest.NewUnstartedServer(handler)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "[::1]:0")
	require.NoError(t, err)

	_ = server.Listener.Close()
	server.Listener = ln
	server.Start()
	t.Cleanup(server.Close)

	return server
}

// The redirect version of the SSRF read primitive, through the JS http
// client: hop 1 is a "public" server (stood up on the IPv6 loopback address,
// treated as public by this guard only via egress.WithPublicOverride), hop 2
// a genuinely internal loopback service. Every hop dials through the same
// guarded transport, so hop 2 is refused exactly as a direct http.get at it
// would be — it must never even be dialed.
func TestScriptRedirectHopTwoIsRecheckedAtDialTime(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hop2Hit atomic.Bool

	hop2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hop2Hit.Store(true)
		_, _ = w.Write([]byte(jsInternalSecret))
	}))
	t.Cleanup(hop2.Close)

	hop1 := ipv6LoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, hop2.URL, http.StatusFound)
	}))

	script := fmt.Sprintf(`
var res = http.get(%q);
return { status: res.error ? "down" : "up", output: { error: res.error, body: res.body } };
`, hop1.URL)

	guard := egress.New(false, egress.WithPublicOverride(net.ParseIP("::1")))
	result := runEgressScript(t, guard, script)

	r.Contains(fmt.Sprint(result.Output["error"]), "denied by egress policy", result.Output)
	r.False(hop2Hit.Load(), "hop 2 must never be dialed: the guard refuses it before any connection is made")
	r.NotContains(fmt.Sprint(result.Output), jsInternalSecret)
}

// redirectHostPolicy: same-host refuses a cross-host hop on its own,
// independent of the egress guard — proven with allow_private=true (the IP
// policy would let hop 2 through) so only the host policy can be why hop 2 is
// never reached.
func TestScriptRedirectHostPolicySameHostRefusesEvenWithPrivateAllowed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hop2Hit atomic.Bool

	hop2 := ipv6LoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hop2Hit.Store(true)
		_, _ = w.Write([]byte(jsInternalSecret))
	}))

	hop1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, hop2.URL, http.StatusFound)
	}))
	t.Cleanup(hop1.Close)

	script := fmt.Sprintf(`
var res = http.get(%q, {redirectHostPolicy: "same-host"});
return { status: res.error ? "down" : "up", output: { error: res.error, body: res.body } };
`, hop1.URL)

	result := runEgressScript(t, egress.New(true), script)

	r.Contains(fmt.Sprint(result.Output["error"]), "redirect to different host refused", result.Output)
	r.False(hop2Hit.Load(), "the refused hop must never be dialed")
	r.NotContains(fmt.Sprint(result.Output), jsInternalSecret)
}

// Raw sockets go through the guard too (the pinned, resolved address is
// judged before connect).
func TestScriptSocketIsRefusedUnderAnEnforcingPolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	script := fmt.Sprintf(`
var c = tcp.connect(%q);
if (!c.ok) return { status: "down", output: { error: c.error } };
c.close();
return { status: "up" };
`, ln.Addr().String())

	denied := runEgressScript(t, egress.New(false), script)
	r.Equal(checkerdef.StatusDown, denied.Status)
	r.Contains(fmt.Sprint(denied.Output["error"]), "denied by egress policy")

	allowed := runEgressScript(t, egress.New(true), script)
	r.Equal(checkerdef.StatusUp, allowed.Status, allowed.Output)
}

// websocket.connect goes through the guard too.
func TestScriptWebSocketIsRefusedUnderAnEnforcingPolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := startWSFixture(t, echoWSFrames)

	script := fmt.Sprintf(`
var ws = websocket.connect(%q);
if (!ws.ok) return { status: "down", output: { error: ws.error } };
ws.close();
return { status: "up" };
`, wsURL(server))

	denied := runEgressScript(t, egress.New(false), script)
	r.Equal(checkerdef.StatusDown, denied.Status)
	r.Contains(fmt.Sprint(denied.Output["error"]), "denied by egress policy")

	allowed := runEgressScript(t, egress.New(true), script)
	r.Equal(checkerdef.StatusUp, allowed.Status, allowed.Output)
}
