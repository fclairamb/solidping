package checkjs

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// httpTunnelHost/httpTunnelTarget mirror checkhttp/tunnel_test.go: `.invalid`
// is RFC 2606-reserved and never resolves anywhere, so a script succeeding
// against it proves the hostname was resolved on the far side of the tunnel,
// not locally.
const (
	httpTunnelHost   = "private.invalid"
	httpTunnelTarget = httpTunnelHost + ":80"
)

// newHTTPTunnelDialer starts a fake bastion forwarding httpTunnelTarget to
// backendAddr and returns a dialer through it.
func newHTTPTunnelDialer(t *testing.T, backendAddr string) (*sshtunnel.Dialer, *sshtunneltest.Server) {
	t.Helper()

	srv := sshtunneltest.Start(t)
	srv.Forward(httpTunnelTarget, backendAddr)

	return srv.Dialer(t), srv
}

// TestJSHTTPGetThroughTunnel proves http.get() honors the dialer: the request
// reaches the backend behind the fake bastion, the response carries
// `tunneled: true`, and the bastion — not this process — resolved the host.
func TestJSHTTPGetThroughTunnel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Seen-Host", req.Host)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("private-service-ok"))
	}))
	defer backend.Close()

	dialer, srv := newHTTPTunnelDialer(t, backend.Listener.Addr().String())
	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result := runScriptWithContext(ctx, t, `
var res = http.get("http://`+httpTunnelHost+`/");
return { status: res.statusCode === 200 ? "up" : "down", output: res };
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.EqualValues(200, result.Output[jsKeyStatusCode])
	r.Equal("private-service-ok", result.Output["body"])
	r.Equal(true, result.Output[jsKeyTunneled])
	r.Equal([]string{httpTunnelTarget}, srv.Requested(), "the bastion must be handed the raw host:port")
}

// TestJSHTTPGetWithoutTunnelFails is the negative control: the same script
// with no dialer on the context cannot resolve the RFC 2606 host and reports
// the failure through the normal `{ error }` shape — no `tunneled` field.
func TestJSHTTPGetWithoutTunnelFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScriptWithContext(t.Context(), t, `
var res = http.get("http://`+httpTunnelHost+`/");
return { status: res.error ? "down" : "up", output: res };
`, 10*time.Second)

	r.Equal("down", result.Status.String(), "output: %#v", result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyError], httpTunnelHost)
	r.Nil(result.Output[jsKeyTunneled])
}

// TestJSHTTPSessionThroughTunnel proves http.session() goes through the same
// httpRequest path as the bare http.* calls, and therefore honors the dialer
// exactly the same way.
func TestJSHTTPSessionThroughTunnel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "sid=abc123; Path=/")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("session-ok"))
	}))
	defer backend.Close()

	dialer, srv := newHTTPTunnelDialer(t, backend.Listener.Addr().String())
	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result := runScriptWithContext(ctx, t, `
var session = http.session();
var res = session.get("http://`+httpTunnelHost+`/");
return { status: res.statusCode === 200 ? "up" : "down", output: res };
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal("session-ok", result.Output["body"])
	r.Equal(true, result.Output[jsKeyTunneled])
	r.Equal([]string{httpTunnelTarget}, srv.Requested())
}

// TestJSHTTPRedirectThroughTunnel proves a redirect to another host behind
// the SAME bastion is dialed through it too, matching what the `http` checker
// does — the script never has to know the second hop exists.
func TestJSHTTPRedirectThroughTunnel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const (
		redirectHost   = "private-two.invalid"
		redirectTarget = redirectHost + ":80"
	)

	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("final-ok"))
	}))
	defer final.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, "http://"+redirectHost+"/final", http.StatusFound)
	}))
	defer first.Close()

	srv := sshtunneltest.Start(t)
	srv.Forward(httpTunnelTarget, first.Listener.Addr().String())
	srv.Forward(redirectTarget, final.Listener.Addr().String())
	dialer := srv.Dialer(t)

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result := runScriptWithContext(ctx, t, `
var res = http.get("http://`+httpTunnelHost+`/");
return { status: res.statusCode === 200 ? "up" : "down", output: res };
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal("final-ok", result.Output["body"])
	r.Equal(true, result.Output[jsKeyTunneled])
	r.ElementsMatch([]string{httpTunnelTarget, redirectTarget}, srv.Requested(),
		"both hops must be dialed through the same tunnel")
}

// TestSubCheckHTTPInheritsTunnelDialer proves the sub-check dispatch path
// (jsRuntime.check) needs no code change for a type that already declares
// SupportsTunnel: solidping.http(...) runs on r.execCtx, which already
// carries the dialer.
//
//nolint:paralleltest // mutates the package-level ResolveChecker/TypeEnabled globals
func TestSubCheckHTTPInheritsTunnelDialer(t *testing.T) {
	r := require.New(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	dialer, srv := newHTTPTunnelDialer(t, backend.Listener.Addr().String())

	previous := ResolveChecker

	t.Cleanup(func() { ResolveChecker = previous })

	ResolveChecker = func(checkType checkerdef.CheckType) (checkerdef.Checker, checkerdef.Config, bool) {
		if checkType == checkerdef.CheckTypeHTTP {
			return &checkhttp.HTTPChecker{}, &checkhttp.HTTPConfig{}, true
		}

		return nil, nil, false
	}
	clearGate(t)

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result := runScriptWithContext(ctx, t, `
var res = solidping.http({ url: "http://`+httpTunnelHost+`/" });
return { status: "up", output: res };
`, 10*time.Second)

	r.Equal(checkerdef.StatusUp.String(), subCheckStatus(t, result))
	r.Equal([]string{httpTunnelTarget}, srv.Requested())
}

// TestSubCheckUDPRefusedUnderTunnel is the core negative test for §3: `udp`
// does not declare SupportsTunnel, so a tunneled script must be refused with
// the API's own sentence rather than silently probing from the worker's own
// network — and the stub must never be entered.
//
//nolint:paralleltest // mutates the package-level ResolveChecker/TypeEnabled globals
func TestSubCheckUDPRefusedUnderTunnel(t *testing.T) {
	r := require.New(t)

	udpStub := &stubChecker{checkType: checkerdef.CheckTypeUDP}
	installStubResolver(t, map[checkerdef.CheckType]*stubChecker{
		checkerdef.CheckTypeUDP: udpStub,
	})
	clearGate(t)

	dialer := checkerdef.DialerFunc(func(_ context.Context, _, _ string) (net.Conn, error) {
		panic("must never be called")
	})
	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result := runScriptWithContext(ctx, t, `
var res = solidping.udp({ host: "10.0.0.1:53" });
return { status: "up", output: res };
`, 10*time.Second)

	msg := subCheckError(t, result)
	r.Contains(msg, `check type "udp" cannot run through an SSH tunnel`)
	r.Zero(udpStub.calls.Load(), "a non-tunnel-capable sub-check must never run")
}

// TestSubCheckUDPAllowedWithoutTunnel is the positive control: with no dialer
// on the context, `udp` resolves and executes exactly as before this spec.
//
//nolint:paralleltest // mutates the package-level ResolveChecker/TypeEnabled globals
func TestSubCheckUDPAllowedWithoutTunnel(t *testing.T) {
	r := require.New(t)

	udpStub := &stubChecker{checkType: checkerdef.CheckTypeUDP}
	installStubResolver(t, map[checkerdef.CheckType]*stubChecker{
		checkerdef.CheckTypeUDP: udpStub,
	})
	clearGate(t)

	result := runScript(t, `
var res = solidping.udp({ host: "10.0.0.1:53" });
return { status: "up", output: res };
`)

	r.Equal(checkerdef.StatusUp.String(), subCheckStatus(t, result))
	r.EqualValues(1, udpStub.calls.Load())
}

// TestBrowserOpenRefusedUnderTunnel: Chrome has its own network stack and
// cannot be routed through a ContextDialer, so browser.open() must throw
// before touching OpenBrowser at all.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBrowserOpenRefusedUnderTunnel(t *testing.T) {
	r := require.New(t)

	previous := OpenBrowser

	t.Cleanup(func() { OpenBrowser = previous })

	var opened int

	OpenBrowser = func(context.Context) (BrowserSession, error) {
		opened++

		return &fakeSession{}, nil
	}

	dialer := checkerdef.DialerFunc(func(_ context.Context, _, _ string) (net.Conn, error) {
		panic("must never be called")
	})
	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	checker := &JSChecker{}
	result, err := checker.Execute(ctx, &JSConfig{
		Script:  `var page = browser.open(); return { status: "up" };`,
		Timeout: 10 * time.Second,
	})
	r.NoError(err)
	r.Equal("error", result.Status.String())
	r.Contains(result.Output["error"], "browser cannot run through an SSH tunnel")
	r.Zero(opened, "OpenBrowser must never be invoked when a dialer is on the context")
}

// TestBrowserOpenAllowedWithoutTunnel is the positive control: with no dialer
// on the context, browser.open() reaches OpenBrowser exactly as before.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBrowserOpenAllowedWithoutTunnel(t *testing.T) {
	r := require.New(t)

	previous := OpenBrowser

	t.Cleanup(func() { OpenBrowser = previous })

	var opened int

	OpenBrowser = func(context.Context) (BrowserSession, error) {
		opened++

		return &fakeSession{}, nil
	}

	checker := &JSChecker{}
	result, err := checker.Execute(t.Context(), &JSConfig{
		Script:  `var page = browser.open(); return { status: "up" };`,
		Timeout: 10 * time.Second,
	})
	r.NoError(err)
	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal(1, opened, "OpenBrowser must be invoked when no dialer is on the context")
}
