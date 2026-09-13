package checkjs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// cookieServer is the fixture the spec describes: /a sets two cookies and
// redirects to /b, which sets a third; /c echoes back whatever Cookie header it
// received. A flow that keeps a jar ends up sending all three to /c; one that
// does not sends none — including the cookie set DURING the redirect, which Go
// only carries with a Jar installed.
func cookieServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "first", Value: "1", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "second", Value: "2", Path: "/"})
		w.Header().Set("Location", "/b")
		w.WriteHeader(http.StatusFound)
	})

	mux.HandleFunc("/b", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "third", Value: "3", Path: "/"})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("landed"))
	})

	mux.HandleFunc("/c", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(req.Header.Get("Cookie")))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func TestHTTPSessionCarriesCookiesIncludingAcrossARedirect(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := cookieServer(t)

	result := runScript(t, `
var s = http.session();
s.get(`+jsString(server.URL+"/a")+`);
var echo = s.post(`+jsString(server.URL+"/c")+`, { body: "" });
return { status: "up", output: { sent: echo.body } };
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)

	sent, _ := result.Output["sent"].(string)
	r.Contains(sent, "first=1")
	r.Contains(sent, "second=2")
	r.Contains(sent, "third=3", "the cookie set DURING the redirect must be captured too")
}

func TestBareHTTPCarriesNoCookies(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := cookieServer(t)

	// The negative half of the pair above: the stateless helpers must keep
	// today's semantics, or every existing script silently changes behavior.
	result := runScript(t, `
http.get(`+jsString(server.URL+"/a")+`);
var echo = http.post(`+jsString(server.URL+"/c")+`, { body: "" });
return { status: "up", output: { sent: echo.body } };
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)

	sent, _ := result.Output["sent"].(string)
	r.Empty(sent, "bare http.* must not carry a cookie jar, got %q", sent)
}

func TestSessionCookiesAreReadable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := cookieServer(t)

	result := runScript(t, `
var s = http.session();
s.get(`+jsString(server.URL+"/a")+`);
var jar = s.cookies(`+jsString(server.URL+"/b")+`);
var names = [];
var domains = [];
for (var i = 0; i < jar.length; i++) { names.push(jar[i].name); domains.push(jar[i].domain + jar[i].path); }
names.sort();
return { status: "up", output: { names: names.join(","), scopes: domains.join(",") } };
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Equal("first,second,third", result.Output["names"])

	scopes, _ := result.Output["scopes"].(string)
	host, _, _ := strings.Cut(strings.TrimPrefix(server.URL, "http://"), ":")
	r.Contains(scopes, host+"/", "cookies() must report the domain and path, not just name/value")
}

func TestFollowRedirectsFalseReturnsTheRedirectItself(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := cookieServer(t)

	result := runScript(t, `
var stopped = http.get(`+jsString(server.URL+"/a")+`, { followRedirects: false });
var followed = http.get(`+jsString(server.URL+"/a")+`);
return { status: "up", output: {
  stoppedCode: stopped.statusCode,
  stoppedLocation: stopped.headers.Location,
  stoppedHops: stopped.redirects.length,
  followedCode: followed.statusCode,
  followedHops: followed.redirects.length,
  followedHopCode: followed.redirects.length > 0 ? followed.redirects[0].statusCode : 0,
  followedHopLocation: followed.redirects.length > 0 ? followed.redirects[0].location : "",
  followedUrl: followed.url,
} };
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)

	// Stopped at the 302: the status and Location are what an OAuth flow reads.
	r.EqualValues(302, result.Output["stoppedCode"])
	r.Equal("/b", result.Output["stoppedLocation"])
	r.EqualValues(0, result.Output["stoppedHops"], "nothing was followed, so the chain is empty")

	// Default: followed, landed on /b, and the chain is reported.
	r.EqualValues(200, result.Output["followedCode"])
	r.EqualValues(1, result.Output["followedHops"])
	r.EqualValues(302, result.Output["followedHopCode"])
	r.Equal("/b", result.Output["followedHopLocation"])
	r.Equal(server.URL+"/b", result.Output["followedUrl"], "url must be the FINAL url")
}

// TestMaxRedirectsStopsTheChain drives an endless redirect loop: without a cap
// this hangs until the check times out.
func TestMaxRedirectsStopsTheChain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	hops := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hops++
		http.Redirect(w, req, "/loop", http.StatusFound)
	}))
	t.Cleanup(server.Close)

	result := runScript(t, `
var resp = http.get(`+jsString(server.URL+"/loop")+`, { maxRedirects: 2 });
return { status: "up", output: { err: resp.error || "", code: resp.statusCode || 0 } };
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Contains(result.Output["err"], "redirect")
	r.LessOrEqual(hops, 3, "maxRedirects: 2 must not walk more than 3 requests, walked %d", hops)
}

// TestRequestTimeoutIsClampedToTheCheckTimeout proves the option cannot widen
// the budget the scheduler allocated: a 10-minute request timeout on a check
// with a 1-second timeout still gives up at ~1s.
func TestRequestTimeoutIsClampedToTheCheckTimeout(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	runtime := newJSRuntime(context.Background(), &JSConfig{Timeout: 0})

	defaults, err := runtime.parseHTTPOptions(nil)
	r.NoError(err)
	r.Equal(defaultTimeout, defaults.timeout)
	r.True(defaults.followRedirects, "redirects are followed unless asked otherwise")
	r.Equal(maxRedirectsCap, defaults.maxRedirects)

	widened, err := runtime.parseHTTPOptions(map[string]any{"timeout": "10m"})
	r.NoError(err)
	r.Equal(defaultTimeout, widened.timeout, "a request may not outlive the check's own timeout")

	narrowed, err := runtime.parseHTTPOptions(map[string]any{"timeout": float64(250)})
	r.NoError(err)
	r.EqualValues(250, narrowed.timeout.Milliseconds(), "a number is milliseconds")

	capped, err := runtime.parseHTTPOptions(map[string]any{"maxRedirects": float64(99)})
	r.NoError(err)
	r.Equal(maxRedirectsCap, capped.maxRedirects, "maxRedirects is capped at %d", maxRedirectsCap)

	_, err = runtime.parseHTTPOptions(map[string]any{"timeout": true})
	r.Error(err, "a nonsense timeout must be reported, not silently ignored")
}

// TestJarIsBounded pins both bounds. A hostile target that sets thousands of
// cookies, or one enormous one, must not grow the job without limit.
func TestJarIsBounded(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jar, err := newBoundedJar()
	r.NoError(err)

	target, err := url.Parse("https://acme.com/")
	r.NoError(err)

	// One cookie past the size bound is dropped; one just under it is kept.
	jar.SetCookies(target, []*http.Cookie{
		{Name: "huge", Value: strings.Repeat("x", maxCookieBytes), Path: "/"},
		{Name: "fine", Value: "ok", Path: "/"},
	})

	stored := jar.Cookies(target)
	names := make([]string, 0, len(stored))

	for _, cookie := range stored {
		names = append(names, cookie.Name)
	}

	r.Equal([]string{"fine"}, names, "an oversized cookie must be refused")

	// Fill past the count bound.
	flood := make([]*http.Cookie, 0, maxJarCookies+50)
	for i := range maxJarCookies + 50 {
		flood = append(flood, &http.Cookie{Name: "c" + itoa(i), Value: "v", Path: "/"})
	}

	jar.SetCookies(target, flood)
	r.LessOrEqual(len(jar.Cookies(target)), maxJarCookies,
		"the jar must stop accepting new cookies at %d", maxJarCookies)

	// An ALREADY-stored cookie can still be updated once the jar is full, or a
	// session would freeze mid-login the moment a target got chatty.
	jar.SetCookies(target, []*http.Cookie{{Name: "fine", Value: "updated", Path: "/"}})

	updated := ""

	for _, cookie := range jar.Cookies(target) {
		if cookie.Name == "fine" {
			updated = cookie.Value
		}
	}

	r.Equal("updated", updated)
}

// jsString renders a Go string as a JS string literal.
func jsString(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}

	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}

	return digits
}
