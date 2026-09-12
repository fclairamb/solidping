package checkbrowser

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// liveCDPURL reports the CDP endpoint a live test should drive, or "" when
// there is none. A missing endpoint is a SKIP with a visible reason, never a
// silent pass: CI's backend job has no Chrome, and a developer running
// `make test` on a laptop should be told why the live coverage did not run.
func liveCDPURL(t *testing.T) string {
	t.Helper()

	return os.Getenv("SP_CHECKERS_BROWSER_CDP_URL")
}

// browserReachableURL rewrites a local httptest URL into one BOTH the test
// process and the BROWSER can reach.
//
// A Chrome in a container cannot dial this process's 127.0.0.1, and the doc
// example deliberately uses ONE base URL for the page and for the http.get
// that follows it — so a name only the container resolves would break the
// second half. This machine's outbound-route address satisfies both, and
// SP_TEST_BROWSER_HOST overrides it for a setup where it does not (a Chrome
// sharing this network namespace wants 127.0.0.1).
func browserReachableURL(t *testing.T, rawURL string) string {
	t.Helper()

	alias := os.Getenv("SP_TEST_BROWSER_HOST")
	if alias == "" {
		alias = outboundHost(t)
	}

	_, port, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	require.NoError(t, err, "unexpected httptest URL %q", rawURL)

	return "http://" + net.JoinHostPort(alias, port)
}

// outboundHost reports the local address this machine would use to reach the
// outside world. The UDP "connection" sends nothing — it only makes the kernel
// pick a route — so this works offline and costs a syscall.
func outboundHost(t *testing.T) string {
	t.Helper()

	conn, err := net.Dial("udp", "203.0.113.1:9") //nolint:noctx // no packet is sent; this only picks a route
	require.NoError(t, err, "cannot determine a browser-reachable host address")

	defer func() { _ = conn.Close() }()

	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	require.NoError(t, err)

	return host
}

// liveFixtureServer serves a tiny login form on ALL interfaces, so a browser
// outside this process can reach it.
func liveFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			_ = req.ParseForm()

			if req.Form.Get("email") == "alice@acme.com" && req.Form.Get("password") == "hunter2" {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "sess-1", Path: "/"})
				http.Redirect(w, req, "/dashboard", http.StatusFound)

				return
			}

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html><body><p id="oops">invalid credentials</p></body></html>`))

			return
		}

		_, _ = w.Write([]byte(`<html><body><form method="post" action="/login">` +
			`<input id="email" name="email"><input id="password" name="password" type="password">` +
			`<button type="submit" id="go">Sign in</button></form></body></html>`))
	})

	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, req *http.Request) {
		cookie, err := req.Cookie("session")
		if err != nil || cookie.Value != "sess-1" {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		_, _ = w.Write([]byte(`<html><title>Dash</title><body>` +
			`<h1 data-testid="dashboard">welcome alice</h1></body></html>`))
	})

	listener, err := net.Listen("tcp", "0.0.0.0:0") //nolint:noctx // test fixture the browser must reach
	require.NoError(t, err)

	server := httptest.NewUnstartedServer(mux)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()

	t.Cleanup(server.Close)

	return server
}

// TestSessionDrivesARealPage is the live proof that Session's chromedp wiring
// is right — the one thing a fake can never tell us.
//
// It runs only when SP_CHECKERS_BROWSER_CDP_URL points at a reachable Chrome
// (`docker compose --profile browser up -d`, or any headless-shell) and skips
// with a visible reason otherwise.
//
//nolint:paralleltest // mutates the process-wide settings
func TestSessionDrivesARealPage(t *testing.T) {
	cdpURL := liveCDPURL(t)
	if cdpURL == "" {
		t.Skip("SP_CHECKERS_BROWSER_CDP_URL is not set: no real browser to drive")
	}

	r := require.New(t)

	withSettings(t, Settings{CDPURL: cdpURL})

	fixture := liveFixtureServer(t)
	base := browserReachableURL(t, fixture.URL)

	ctx, cancel := contextWithTimeout(t, 60*time.Second)
	defer cancel()

	session, err := Open(ctx)
	r.NoError(err)

	defer session.Close()

	nav, err := session.Navigate(ctx, base+"/login")
	r.NoError(err)
	r.Contains(nav.URL, "/login")

	r.NoError(session.WaitVisible(ctx, "#email"))
	r.NoError(session.Fill(ctx, "#email", "alice@acme.com"))
	r.NoError(session.Fill(ctx, "#password", "hunter2"))
	r.NoError(session.Click(ctx, "#go"))
	r.NoError(session.WaitVisible(ctx, "[data-testid=dashboard]"))

	// The navigation really moved: the browser followed the 302 to /dashboard
	// and the target only serves that page to a request carrying the cookie.
	current, err := session.URL(ctx)
	r.NoError(err)
	r.Contains(current, "/dashboard")

	text, err := session.Text(ctx, "[data-testid=dashboard]")
	r.NoError(err)
	r.Equal("welcome alice", text)

	title, err := session.Evaluate(ctx, "document.title")
	r.NoError(err)
	r.Equal("Dash", title)

	// A page-side throw is a target-side failure, not infrastructure.
	_, evalErr := session.Evaluate(ctx, "throw new Error('boom')")
	r.Error(evalErr)
	r.False(Infra(evalErr), "a page that threw is not our infrastructure failing")

	// An expression with no value must not be an error — a script clicking
	// something through evaluate() would otherwise read as a failure.
	value, err := session.Evaluate(ctx, "void 0")
	r.NoError(err)
	r.Nil(value)

	cookies, err := session.Cookies(ctx)
	r.NoError(err)

	var found bool

	for _, cookie := range cookies {
		if cookie.Name == "session" && cookie.Value == "sess-1" {
			found = true
		}
	}

	r.True(found, "the browser-established session cookie must be readable, got %#v", cookies)

	// Deliberately asserted as "a real image", not "a PNG": chromedp's
	// FullScreenshot emits JPEG for any quality below 100, and
	// screenshotQuality is 90 — so what the browser check has always captured
	// (and stored in the field named PNG) is a JPEG. That mismatch predates
	// this session type and is left exactly as it was; asserting PNG here
	// would be asserting a behavior the product does not have.
	shot, err := session.Screenshot(ctx)
	r.NoError(err)
	r.NotEmpty(shot)
	r.Greater(len(shot), 1024, "a full-page capture of a real page is not a handful of bytes")

	// A selector that never appears is a TARGET failure, and it must end at
	// the caller's deadline rather than hanging.
	waitCtx, waitCancel := contextWithTimeout(t, 2*time.Second)
	defer waitCancel()

	waitErr := session.WaitVisible(waitCtx, "#never-appears")
	r.Error(waitErr)
	r.False(Infra(waitErr), "a missing selector is not an infrastructure failure")

	// And after Close, every method is refused rather than crashing.
	session.Close()
	session.Close()

	_, closedErr := session.Navigate(ctx, base+"/login")
	r.ErrorIs(closedErr, errSessionClosed)
	r.True(Infra(closedErr), "driving a closed page is infrastructure, not a verdict")
}

// contextWithTimeout is t.Context() with a deadline, kept as a helper so the
// live test above reads as a sequence of page actions.
func contextWithTimeout(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()

	return context.WithTimeout(t.Context(), d)
}
