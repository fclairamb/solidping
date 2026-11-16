package checkbrowser

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

func TestOriginOfNormalizes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for raw, want := range map[string]string{
		"https://ACME.com":         "https://acme.com:443",
		"https://acme.com:443/x?y": "https://acme.com:443",
		"http://acme.com":          "http://acme.com:80",
		"http://127.0.0.1:8080/a":  "http://127.0.0.1:8080",
		"http://[::1]:8080":        "http://[::1]:8080",
	} {
		got, ok := originOf(raw)
		r.True(ok, raw)
		r.Equal(want, got, raw)
	}

	for _, raw := range []string{"", "ftp://acme.com", "acme.com", "https://", "javascript:alert(1)"} {
		_, ok := originOf(raw)
		r.False(ok, raw)
	}
}

func authEvent(source fetch.AuthChallengeSource, origin string) *fetch.EventAuthRequired {
	return &fetch.EventAuthRequired{
		RequestID:     "1",
		Request:       &network.Request{URL: origin + "/"},
		AuthChallenge: &fetch.AuthChallenge{Source: source, Origin: origin, Scheme: "basic"},
	}
}

func armedSession(origin string) *Session {
	session := &Session{}
	session.auth.set = true
	session.auth.username = "alice"
	session.auth.password = "pw"
	session.auth.origin = origin

	return session
}

// TestAnswerChallengeScopesCredentials pins the decision table: credentials
// only for a server challenge from their own origin, at most twice; a cancel
// for another origin, a proxy, or an origin not bound yet.
func TestAnswerChallengeScopesCredentials(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	provide := fetch.AuthChallengeResponseResponseProvideCredentials
	cancel := fetch.AuthChallengeResponseResponseCancelAuth

	session := armedSession("https://staging.acme.com:443")

	other := session.answerChallenge(authEvent(fetch.AuthChallengeSourceServer, "https://cdn.acme.com"))
	r.Equal(cancel, other.Response)
	r.Empty(other.Username)
	r.Empty(other.Password)

	proxy := session.answerChallenge(authEvent(fetch.AuthChallengeSourceProxy, "https://staging.acme.com"))
	r.Equal(cancel, proxy.Response, "a proxy challenge is always cancelled")
	r.Empty(proxy.Password)

	first := session.answerChallenge(authEvent(fetch.AuthChallengeSourceServer, "https://staging.acme.com"))
	r.Equal(provide, first.Response)
	r.Equal("alice", first.Username)
	r.Equal("pw", first.Password)

	second := session.answerChallenge(authEvent(fetch.AuthChallengeSourceServer, "https://staging.acme.com"))
	r.Equal(provide, second.Response)

	third := session.answerChallenge(authEvent(fetch.AuthChallengeSourceServer, "https://staging.acme.com"))
	r.Equal(cancel, third.Response, "a third challenge means the credentials are wrong")
	r.True(session.basicAuthRejectedSince(0))

	unbound := armedSession("")
	r.Equal(cancel, unbound.answerChallenge(authEvent(fetch.AuthChallengeSourceServer, "https://acme.com")).Response)

	none := &Session{}
	r.Equal(cancel, none.answerChallenge(authEvent(fetch.AuthChallengeSourceServer, "https://acme.com")).Response)
}

func TestBindBasicAuthOriginTakesTheNextNavigation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	session := armedSession("")
	origin, _ := session.bindBasicAuthOrigin("https://staging.acme.com/login")
	r.Equal("https://staging.acme.com:443", origin)

	// Bound once: a later navigation elsewhere does not move the credentials.
	origin, _ = session.bindBasicAuthOrigin("https://other.acme.com/")
	r.Equal("https://staging.acme.com:443", origin)

	explicit := armedSession("https://acme.com:443")
	origin, _ = explicit.bindBasicAuthOrigin("https://other.acme.com/")
	r.Equal("https://acme.com:443", origin)

	unarmed := &Session{}
	origin, _ = unarmed.bindBasicAuthOrigin("https://acme.com/")
	r.Empty(origin)
}

func TestBasicAuthRejectedErrorNamesOnlyTheOrigin(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.EqualError(basicAuthRejectedError("https://acme.com:443"), "basic auth rejected by https://acme.com")
	r.EqualError(basicAuthRejectedError("http://acme.com:80"), "basic auth rejected by http://acme.com")
	r.EqualError(basicAuthRejectedError("http://acme.com:8080"), "basic auth rejected by http://acme.com:8080")
}

func TestBasicAuthRefusesBadOriginAndClosedSession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	session := &Session{}
	r.ErrorIs(session.BasicAuth(t.Context(), "u", "p", "ftp://acme.com"), errBasicAuthOrigin)

	closed := &Session{closed: true}
	err := closed.BasicAuth(t.Context(), "u", "p", "")
	r.ErrorIs(err, errSessionClosed)
	r.True(Infra(err), "a closed page is infrastructure, like every other page method")
}

// authServer is a fixture on all interfaces that requires Basic credentials
// user/pass on every path and records every Authorization header it received.
type authServer struct {
	*httptest.Server

	mu      sync.Mutex
	headers []string
	hits    int
}

func (a *authServer) requests() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.hits
}

func (a *authServer) received() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.headers...)
}

func newAuthServer(t *testing.T, user, pass string, body func() string) *authServer {
	t.Helper()

	fixture := &authServer{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fixture.mu.Lock()
		fixture.hits++
		fixture.mu.Unlock()

		if header := req.Header.Get("Authorization"); header != "" {
			fixture.mu.Lock()
			fixture.headers = append(fixture.headers, header)
			fixture.mu.Unlock()
		}

		gotUser, gotPass, ok := req.BasicAuth()
		if !ok || gotUser != user || gotPass != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="staging"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`<html><title>Denied</title><body>denied</body></html>`))

			return
		}

		_, _ = w.Write([]byte(body()))
	})

	listener, err := net.Listen("tcp", "0.0.0.0:0") //nolint:noctx // test fixture the browser must reach
	require.NoError(t, err)

	server := httptest.NewUnstartedServer(handler)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()

	t.Cleanup(server.Close)

	fixture.Server = server

	return fixture
}

// TestBasicAuthLive drives a real Chrome against a Basic-auth site whose page
// pulls an image from a SECOND origin that challenges too.
//
//nolint:paralleltest // mutates the process-wide settings
func TestBasicAuthLive(t *testing.T) {
	settings, ok := liveBrowserSettings()
	if !ok {
		t.Skip(liveBrowserSkipReason)
	}

	withSettings(t, settings)
	widenChromeStart(t)

	const (
		user     = "alice-user"
		password = "pw-s3cr3t-value"
	)

	third := newAuthServer(t, user, password, func() string { return "img" })
	thirdURL := browserReachableURL(t, third.URL)

	site := newAuthServer(t, user, password, func() string {
		return `<html><title>Staging</title><body><h1 id="ok">welcome</h1>` +
			`<img id="pixel" src="` + thirdURL + `/pixel.png"></body></html>`
	})
	siteURL := browserReachableURL(t, site.URL)

	t.Run("right credentials", func(t *testing.T) {
		r := require.New(t)

		ctx, cancel := contextWithTimeout(t, 60*time.Second)
		defer cancel()

		session, err := Open(ctx)
		r.NoError(err)

		defer session.Close()

		r.NoError(session.BasicAuth(ctx, user, password, ""))

		// The egress watcher reads Network.responseReceived: with Fetch
		// interception on, it must still see every response with its remote
		// address, exactly once per request (no double pause, no lost event).
		var (
			seenMu sync.Mutex
			seen   = map[string]int{}
		)

		chromedp.ListenTarget(session.browserCtx, func(ev any) {
			if event, isResponse := ev.(*network.EventResponseReceived); isResponse && event.Response != nil &&
				event.Response.RemoteIPAddress != "" {
				seenMu.Lock()
				seen[event.Response.URL]++
				seenMu.Unlock()
			}
		})

		nav, err := session.Navigate(ctx, siteURL+"/")
		r.NoError(err)
		r.Equal("Staging", nav.Title)
		r.NotContains(nav.URL, password, "the credentials never land in the URL")

		// Let the cross-origin image request settle, then assert the second
		// origin never saw the credentials.
		_, err = session.Evaluate(ctx, `new Promise(function (resolve) {
			var img = document.getElementById("pixel");
			if (img.complete) { resolve(true); return; }
			img.onload = img.onerror = function () { resolve(true); };
		})`)
		r.NoError(err)

		r.NotEmpty(site.received(), "the site got the credentials")
		r.Positive(third.requests(), "the second origin was really asked, and challenged")
		r.Empty(third.received(), "the second origin must never receive an Authorization header")

		seenMu.Lock()
		r.Equal(1, seen[siteURL+"/"], "the egress watcher's event still fires, once, for the page")
		seenMu.Unlock()

		text, err := session.Text(ctx, "#ok")
		r.NoError(err)
		r.Equal("welcome", text)
	})

	t.Run("wrong credentials", func(t *testing.T) {
		r := require.New(t)

		before := len(site.received())

		ctx, cancel := contextWithTimeout(t, 60*time.Second)
		defer cancel()

		session, err := Open(ctx)
		r.NoError(err)

		defer session.Close()

		r.NoError(session.BasicAuth(ctx, "wrong-user", "wrong-pass", ""))

		_, err = session.Navigate(ctx, siteURL+"/")
		r.Error(err)
		r.Contains(err.Error(), "basic auth rejected by "+siteURL)
		r.NotContains(err.Error(), "wrong-user")
		r.NotContains(err.Error(), "wrong-pass")
		r.False(Infra(err), "wrong credentials are the target's verdict, not our infrastructure")

		r.LessOrEqual(len(site.received())-before, maxBasicAuthAttempts,
			"wrong credentials are tried at most twice")
	})

	t.Run("explicit origin elsewhere", func(t *testing.T) {
		r := require.New(t)

		before := len(site.received())

		ctx, cancel := contextWithTimeout(t, 60*time.Second)
		defer cancel()

		session, err := Open(ctx)
		r.NoError(err)

		defer session.Close()

		// Credentials scoped to the second origin: the site's own challenge
		// is cancelled, so it never receives them.
		r.NoError(session.BasicAuth(ctx, user, password, thirdURL))

		nav, err := session.Navigate(ctx, siteURL+"/")
		r.NoError(err, "a cancelled challenge from another origin is not a rejection")
		r.Equal("Denied", nav.Title)
		r.Len(site.received(), before, "the site must not receive credentials scoped elsewhere")
	})
}
