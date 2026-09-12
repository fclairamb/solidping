package checkjs

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// errSelectorMissing is a TARGET-side failure: the page simply did not have
// what the script asked for. It must come back as `{ ok: false }`, never as a
// throw, so the script keeps the right to call the target down.
var errSelectorMissing = errors.New("selector not found")

// errSocketGone is an INFRASTRUCTURE failure — the shape checkbrowser.Infra
// recognizes. It must throw, so the check reports `error` and nobody gets
// paged for the customer's site.
var errSocketGone = errors.New("websocket: close 1006 (abnormal closure)")

// fakeSession is a BrowserSession with no Chrome behind it. CI's backend job
// has no browser, so every binding assertion below runs against this.
type fakeSession struct {
	navResult checkbrowser.NavResult
	navErr    error
	waitErr   error
	waitBlock bool
	clickErr  error
	fillErr   error
	pressErr  error
	text      string
	textFail  error
	evalValue any
	evalErr   error
	url       string
	urlErr    error
	cookies   []checkbrowser.Cookie
	cookieErr error
	png       []byte
	pngErr    error

	calls   atomic.Int32
	closes  atomic.Int32
	filled  []string
	pressed []string
	goneAt  time.Time
}

func (f *fakeSession) Navigate(_ context.Context, url string) (checkbrowser.NavResult, error) {
	f.calls.Add(1)

	if f.navErr != nil {
		return checkbrowser.NavResult{}, f.navErr
	}

	result := f.navResult
	if result.URL == "" {
		result.URL = url
	}

	return result, nil
}

func (f *fakeSession) WaitVisible(ctx context.Context, _ string) error {
	f.calls.Add(1)

	if f.waitBlock {
		// Blocks until the CALLER's context ends — the §3 proof: a binding
		// stuck in chromedp must return when the check's deadline expires.
		<-ctx.Done()
		f.goneAt = time.Now()

		return ctx.Err()
	}

	return f.waitErr
}

func (f *fakeSession) Click(_ context.Context, _ string) error {
	f.calls.Add(1)

	return f.clickErr
}

func (f *fakeSession) Fill(_ context.Context, sel, text string) error {
	f.calls.Add(1)
	f.filled = append(f.filled, sel+"="+text)

	return f.fillErr
}

func (f *fakeSession) Press(_ context.Context, sel, key string) error {
	f.calls.Add(1)
	f.pressed = append(f.pressed, sel+":"+key)

	return f.pressErr
}

func (f *fakeSession) Text(_ context.Context, _ string) (string, error) {
	f.calls.Add(1)

	if f.textFail != nil {
		return "", f.textFail
	}

	return f.text, nil
}

func (f *fakeSession) Evaluate(_ context.Context, _ string) (any, error) {
	f.calls.Add(1)

	return f.evalValue, f.evalErr
}

func (f *fakeSession) URL(_ context.Context) (string, error) {
	f.calls.Add(1)

	return f.url, f.urlErr
}

func (f *fakeSession) Cookies(_ context.Context) ([]checkbrowser.Cookie, error) {
	f.calls.Add(1)

	return f.cookies, f.cookieErr
}

func (f *fakeSession) Screenshot(_ context.Context) ([]byte, error) {
	f.calls.Add(1)

	return f.png, f.pngErr
}

func (f *fakeSession) Close() { f.closes.Add(1) }

// installFakeBrowser points the OpenBrowser seam at the given session and
// restores the real opener afterwards.
func installFakeBrowser(t *testing.T, session *fakeSession) {
	t.Helper()

	previous := OpenBrowser

	t.Cleanup(func() { OpenBrowser = previous })

	OpenBrowser = func(context.Context) (BrowserSession, error) {
		return session, nil
	}
}

// installFailingBrowser makes browser.open() fail with err.
func installFailingBrowser(t *testing.T, err error) {
	t.Helper()

	previous := OpenBrowser

	t.Cleanup(func() { OpenBrowser = previous })

	OpenBrowser = func(context.Context) (BrowserSession, error) {
		return nil, err
	}
}

// runBrowserScript executes a script the way the worker would, with an
// explicit timeout (the browser tests need short ones to prove cancellation).
func runBrowserScript(t *testing.T, script string, timeout time.Duration) *checkerdef.Result {
	t.Helper()

	checker := &JSChecker{}

	result, err := checker.Execute(t.Context(), &JSConfig{Script: script, Timeout: timeout})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

// TestBrowserBindingsReturnShapes pins every method's return shape against a
// fake session — the contract a script reads, method by method.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBrowserBindingsReturnShapes(t *testing.T) {
	r := require.New(t)

	session := &fakeSession{
		navResult: checkbrowser.NavResult{URL: "https://acme.com/dash", Title: "Dash", Duration: 12 * time.Millisecond},
		text:      "hello world",
		evalValue: float64(42),
		url:       "https://acme.com/dash",
		cookies: []checkbrowser.Cookie{
			{Name: "session", Value: "s1", Domain: "acme.com", Path: "/", Secure: true, HTTPOnly: true},
		},
		png: []byte("\x89PNGfake"),
	}
	installFakeBrowser(t, session)

	result := runBrowserScript(t, `
var page = browser.open();
var nav = page.goto("https://acme.com/login");
var wait = page.waitFor("#ready");
var click = page.click("#submit");
var fill = page.fill("#email", "alice@acme.com");
var press = page.press("#email", "Enter");
var text = page.text("h1");
var evaluated = page.evaluate("40 + 2");
var shot = page.screenshot();
var cookies = page.cookies();
return { status: "down", output: {
  navOk: nav.ok, navURL: nav.url, navTitle: nav.title, navDuration: nav.duration,
  waitOk: wait.ok, clickOk: click.ok, fillOk: fill.ok, pressOk: press.ok,
  textOk: text.ok, text: text.text,
  evalOk: evaluated.ok, evalValue: evaluated.value,
  shotOk: shot.ok,
  url: page.url(),
  cookieCount: cookies.length, cookieName: cookies[0].name, cookieSecure: cookies[0].secure,
} };
`, 5*time.Second)

	out := result.Output
	r.Equal("down", result.Status.String(), "output: %#v", out)
	r.Equal(true, out["navOk"])
	r.Equal("https://acme.com/dash", out["navURL"])
	r.Equal("Dash", out["navTitle"])
	r.InDelta(12, out["navDuration"], 0.001)
	r.Equal(true, out["waitOk"])
	r.Equal(true, out["clickOk"])
	r.Equal(true, out["fillOk"])
	r.Equal(true, out["pressOk"])
	r.Equal(true, out["textOk"])
	r.Equal("hello world", out["text"])
	r.Equal(true, out["evalOk"])
	r.InDelta(42, out["evalValue"], 0.001)
	r.Equal(true, out["shotOk"])
	r.Equal("https://acme.com/dash", out["url"])
	r.InDelta(1, out["cookieCount"], 0.001)
	r.Equal("session", out["cookieName"])
	r.Equal(true, out["cookieSecure"])

	r.Equal([]string{"#email=alice@acme.com"}, session.filled, "fill must send the text it was given")
	r.Equal([]string{"#email:Enter"}, session.pressed)
	r.Positive(session.closes.Load(), "the deferred close must have run")
}

// TestBrowserTargetFailureIsDownNotError is the throw-vs-return split's whole
// point: a selector that never appeared is the TARGET's problem, so the script
// keeps the right to call it down.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBrowserTargetFailureIsDownNotError(t *testing.T) {
	r := require.New(t)

	installFakeBrowser(t, &fakeSession{waitErr: errSelectorMissing})

	result := runBrowserScript(t, `
var page = browser.open();
var wait = page.waitFor("#never");
if (!wait.ok) {
  return { status: "down", output: { step: "wait", error: wait.error } };
}
return { status: "up" };
`, 5*time.Second)

	r.Equal("down", result.Status.String(), "a missing selector must NOT be an error status")
	r.Contains(result.Output["error"], "selector not found")
}

// TestBrowserInfraFailureThrowsAndIsAnError is the mirror image: the socket
// dying is OUR outage, so the binding throws and the runtime reports `error`
// with the message in output.error — never `down`.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBrowserInfraFailureThrowsAndIsAnError(t *testing.T) {
	r := require.New(t)

	session := &fakeSession{waitErr: errSocketGone}
	installFakeBrowser(t, session)

	result := runBrowserScript(t, `
var page = browser.open();
var wait = page.waitFor("#anything");
return { status: "up", output: { reached: true } };
`, 5*time.Second)

	r.Equal("error", result.Status.String())
	r.NotEqual("down", result.Status.String(), "our browser dying must never page the customer")
	r.Contains(result.Output["error"], "close 1006")
	r.Nil(result.Output["reached"], "the script must not have continued past the throw")
	r.Positive(session.closes.Load(), "the deferred close must run on a throw")
}

// TestBrowserOpenFailureIsAnError covers "no Chrome on this worker": open()
// throws with the browser check's own message and the result is `error`.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBrowserOpenFailureIsAnError(t *testing.T) {
	r := require.New(t)

	installFailingBrowser(t, errors.New( //nolint:err113 // the exact operator-facing string is the assertion
		"Chrome/Chromium not found: install headless Chrome on this worker, "+
			"set checkers.browser.chrome_path (SP_CHECKERS_BROWSER_CHROME_PATH), or point "+
			"checkers.browser.cdp_url (SP_CHECKERS_BROWSER_CDP_URL) at a headless-shell container"))

	result := runBrowserScript(t, `
var page = browser.open();
return { status: "up" };
`, 5*time.Second)

	r.Equal("error", result.Status.String())
	r.Contains(result.Output["error"], "Chrome/Chromium not found")
	r.Contains(result.Output["error"], "SP_CHECKERS_BROWSER_CDP_URL")
}

// TestBrowserSlotTimeoutNamesTheWait: a saturated worker names the slot wait
// exactly as the browser check does, so the two read the same in an incident.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBrowserSlotTimeoutNamesTheWait(t *testing.T) {
	r := require.New(t)

	installFailingBrowser(t, checkbrowser.ErrSlotTimeout)

	result := runBrowserScript(t, `browser.open(); return { status: "up" };`, 5*time.Second)

	r.Equal("error", result.Status.String())
	r.Contains(result.Output["error"], "browser slot")
}

// TestSecondOpenThrows pins the one-page-per-execution rule, including after
// an explicit close: a closed page is a page this script already spent.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestSecondOpenThrows(t *testing.T) {
	r := require.New(t)

	installFakeBrowser(t, &fakeSession{})

	//nolint:dupword // the second open() IS the assertion
	result := runBrowserScript(t, `
var page = browser.open();
browser.open();
return { status: "up" };
`, 5*time.Second)

	r.Equal("error", result.Status.String())
	r.Contains(result.Output["error"], "already open")

	installFakeBrowser(t, &fakeSession{})

	afterClose := runBrowserScript(t, `
var page = browser.open();
page.close();
browser.open();
return { status: "up" };
`, 5*time.Second)

	r.Equal("error", afterClose.Status.String())
	r.Contains(afterClose.Output["error"], "already open")
}

// TestCloseIsIdempotent: page.close() twice is a no-op, and the deferred close
// on top of it costs nothing either.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestCloseIsIdempotent(t *testing.T) {
	r := require.New(t)

	session := &fakeSession{}
	installFakeBrowser(t, session)

	//nolint:dupword // closing twice IS the assertion
	result := runBrowserScript(t, `
var page = browser.open();
page.close();
page.close();
return { status: "up" };
`, 5*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	// The fake counts every call; the REAL Session guarantees idempotence
	// itself (see checkbrowser). What matters here is that neither the double
	// close nor the deferred one threw.
	r.GreaterOrEqual(int(session.closes.Load()), 2)
}

// TestBrowserActionLimit pins the 100-action cap: the 101st call returns
// { ok: false } naming the limit rather than running.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBrowserActionLimit(t *testing.T) {
	r := require.New(t)

	session := &fakeSession{}
	installFakeBrowser(t, session)

	result := runBrowserScript(t, `
var page = browser.open();
var refusedAt = 0;
var lastError = "";
for (var i = 1; i <= 105; i++) {
  var res = page.click("#x");
  if (!res.ok && refusedAt === 0) {
    refusedAt = i;
    lastError = res.error;
  }
}
return { status: "up", output: { refusedAt: refusedAt, lastError: lastError } };
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.InDelta(101, result.Output["refusedAt"], 0.001, "the 101st action is the first refused one")
	r.Contains(result.Output["lastError"], "browser action limit of 100 exceeded")
	r.Equal(int32(100), session.calls.Load(), "a refused action must not reach the session")
}

// TestBrowserBudgetIsSeparateFromTheSubCheckBudget proves the two budgets in
// BOTH directions: 100 browser actions leave the 20-call http budget intact,
// and a spent http budget leaves the browser usable.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBrowserBudgetIsSeparateFromTheSubCheckBudget(t *testing.T) {
	r := require.New(t)

	session := &fakeSession{}
	installFakeBrowser(t, session)

	// Direction 1: exhaust the browser budget, then make an http call. The
	// call must be attempted (it fails on the unroutable host, not on a
	// budget refusal).
	first := runBrowserScript(t, `
var page = browser.open();
for (var i = 0; i < 100; i++) { page.click("#x"); }
var res = http.get("http://127.0.0.1:1/never");
return { status: "up", output: { httpError: res.error } };
`, 10*time.Second)

	r.Equal("up", first.Status.String(), "output: %#v", first.Output)
	httpError, _ := first.Output["httpError"].(string)
	r.NotEmpty(httpError)
	r.NotContains(httpError, "sub-check limit",
		"100 browser actions must not consume a single unit of the 20-call budget")

	session2 := &fakeSession{}
	installFakeBrowser(t, session2)

	// Direction 2: exhaust the sub-check budget, then drive the page.
	second := runBrowserScript(t, `
var page = browser.open();
for (var i = 0; i < 21; i++) { http.get("http://127.0.0.1:1/never"); }
var res = page.click("#x");
return { status: "up", output: { clickOk: res.ok, clickError: res.error } };
`, 20*time.Second)

	r.Equal("up", second.Status.String(), "output: %#v", second.Output)
	r.Equal(true, second.Output["clickOk"],
		"a spent http budget must not close the browser budget")
}

// TestBrowserOpenRefusedWhenTypeDisabled: the server-level gate covers the
// scripted path with the EXACT string sub-checks use, so an operator who
// turned `browser` off does not get it back through a script.
//
//nolint:paralleltest // mutates the package-level TypeEnabled and OpenBrowser globals
func TestBrowserOpenRefusedWhenTypeDisabled(t *testing.T) {
	r := require.New(t)

	session := &fakeSession{}
	installFakeBrowser(t, session)
	installGate(t, checkerdef.CheckTypeBrowser)

	result := runBrowserScript(t, `browser.open(); return { status: "up" };`, 5*time.Second)

	r.Equal("error", result.Status.String())
	r.Contains(result.Output["error"], `check type "browser" is disabled on this server`)
	r.Zero(session.calls.Load(), "the session must never have been opened")
	r.Zero(session.closes.Load())
}

// TestGotoRejectsDangerousSchemes: page.goto is held to the browser check's own
// URL rules, so a script cannot reach a scheme the configured path refuses.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestGotoRejectsDangerousSchemes(t *testing.T) {
	r := require.New(t)

	for _, tc := range []struct {
		target string
		reason string
	}{
		// The http(s) prefix rule catches all of these first — the explicit
		// file:/data:/javascript: list behind it is the belt-and-braces second
		// gate, exactly as on a browser check's own `url` field.
		{"file:///etc/passwd", "must start with http:// or https://"},
		{"data:text/html,x", "must start with http:// or https://"},
		{"javascript:alert(1)", "must start with http:// or https://"},
		{"ftp://acme.com/", "must start with http:// or https://"},
	} {
		session := &fakeSession{}
		installFakeBrowser(t, session)

		checker := &JSChecker{}
		result, err := checker.Execute(t.Context(), &JSConfig{
			Script: `
var page = browser.open();
var nav = page.goto(env.TARGET);
return { status: "down", output: { ok: nav.ok, error: nav.error } };
`,
			Timeout: 5 * time.Second,
			Env:     map[string]string{"TARGET": tc.target},
		})
		r.NoError(err)
		r.Equal(false, result.Output["ok"], "%s must be refused", tc.target)
		r.Contains(result.Output["error"], tc.reason)
		r.Zero(session.calls.Load(), "a refused URL must never reach the browser")
	}

	// Positive control: an http(s) URL goes through, so the assertions above
	// are about the scheme and not about a goto that refuses everything.
	session := &fakeSession{}
	installFakeBrowser(t, session)

	ok := runBrowserScript(t, `
var page = browser.open();
var nav = page.goto("https://acme.com/");
return { status: "up", output: { ok: nav.ok } };
`, 5*time.Second)
	r.Equal(true, ok.Output["ok"])
	r.Equal(int32(1), session.calls.Load())
}

// TestScreenshotKeptOnDownDroppedOnUp is §5's rule: the SCRIPT decides when to
// shoot, the runtime decides whether the verdict earns keeping it.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestScreenshotKeptOnDownDroppedOnUp(t *testing.T) {
	cases := []struct {
		name   string
		status string
		kept   bool
	}{
		{"down keeps it", "down", true},
		{"timeout keeps it", "timeout", true},
		{"up drops it", "up", false},
		{"error drops it", "error", false},
	}

	for _, tc := range cases {
		//nolint:paralleltest // shares the package-level seam
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			installFakeBrowser(t, &fakeSession{png: []byte("\x89PNGshot")})

			checker := &JSChecker{}
			result, err := checker.Execute(t.Context(), &JSConfig{
				Script:  `var page = browser.open(); page.screenshot(); return { status: env.WANT };`,
				Timeout: 5 * time.Second,
				Env:     map[string]string{"WANT": tc.status},
			})
			r.NoError(err)

			if !tc.kept {
				if result.Diagnostics != nil {
					r.Nil(result.Diagnostics.Screenshot,
						"a capture on a %s run must be dropped", tc.status)
				}

				return
			}

			r.NotNil(result.Diagnostics)
			r.NotNil(result.Diagnostics.Screenshot)
			r.Equal([]byte("\x89PNGshot"), result.Diagnostics.Screenshot.PNG)
			r.False(result.Diagnostics.Screenshot.CapturedAt.IsZero())
		})
	}
}

// TestLastScreenshotWins: a script can overwrite an early "before" shot with
// the one taken at the moment it decided the target was down.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestLastScreenshotWins(t *testing.T) {
	r := require.New(t)

	swapping := &swappingSession{}
	installFakeBrowser(t, &swapping.fakeSession)

	OpenBrowser = func(context.Context) (BrowserSession, error) { return swapping, nil }

	checker := &JSChecker{}

	//nolint:dupword // shooting twice IS the assertion
	const script = `
var page = browser.open();
page.screenshot();
page.screenshot();
return { status: "down" };
`

	result, err := checker.Execute(t.Context(), &JSConfig{
		Script:  script,
		Timeout: 5 * time.Second,
	})
	r.NoError(err)
	r.NotNil(result.Diagnostics)
	r.NotNil(result.Diagnostics.Screenshot)
	r.Equal([]byte("after"), result.Diagnostics.Screenshot.PNG,
		"the LAST successful capture must be the one kept")
	r.Equal(2, swapping.shots)
}

// swappingSession returns a different capture on each call, so "last wins" is
// provable rather than merely plausible.
type swappingSession struct {
	fakeSession

	shots int
}

func (s *swappingSession) Screenshot(_ context.Context) ([]byte, error) {
	s.shots++
	if s.shots == 1 {
		return []byte("before"), nil
	}

	return []byte("after"), nil
}

// TestWaitForWithNoTimeoutEndsAtTheChecksTimeout is the CANCELLATION PROOF
// from §3, and the load-bearing test of the whole design.
//
// goja's Interrupt only lands when control returns to JavaScript. A binding
// blocked in chromedp.Run on a context that does NOT descend from the
// execution context would hold the script open forever; every page method
// therefore runs on a derived context. With that in place, a waitFor on a
// selector that never appears ends the script AT the check's timeout — with
// status `timeout`, and with the session closed afterwards.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestWaitForWithNoTimeoutEndsAtTheChecksTimeout(t *testing.T) {
	r := require.New(t)

	session := &fakeSession{waitBlock: true}
	installFakeBrowser(t, session)

	const checkTimeout = 300 * time.Millisecond

	start := time.Now()
	result := runBrowserScript(t, `
var page = browser.open();
page.waitFor("#never-appears");
return { status: "up" };
`, checkTimeout)
	elapsed := time.Since(start)

	r.Equal("timeout", result.Status.String(), "output: %#v", result.Output)
	r.GreaterOrEqual(elapsed, checkTimeout, "it must not have given up early")
	r.Less(elapsed, checkTimeout+2*time.Second, "and it must not have run past the timeout")
	r.Positive(session.closes.Load(), "the session must be closed after the interrupt")
	r.False(session.goneAt.IsZero(), "the blocked binding must have been released by the cancel")
}

// TestPerCallTimeoutNeverWidensTheBudget: an explicit `timeout` shortens a
// wait, and one longer than the check's own budget cannot extend it.
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestPerCallTimeoutNeverWidensTheBudget(t *testing.T) {
	r := require.New(t)

	installFakeBrowser(t, &fakeSession{waitBlock: true})

	start := time.Now()
	short := runBrowserScript(t, `
var page = browser.open();
var wait = page.waitFor("#never", { timeout: "80ms" });
return { status: "down", output: { ok: wait.ok, error: wait.error } };
`, 5*time.Second)

	r.Equal("down", short.Status.String(), "output: %#v", short.Output)
	r.Equal(false, short.Output["ok"])
	r.Less(time.Since(start), 3*time.Second, "the per-call timeout must have cut the wait short")

	installFakeBrowser(t, &fakeSession{waitBlock: true})

	const checkTimeout = 300 * time.Millisecond

	start = time.Now()
	wide := runBrowserScript(t, `
var page = browser.open();
page.waitFor("#never", { timeout: "60s" });
return { status: "up" };
`, checkTimeout)
	elapsed := time.Since(start)

	r.Equal("timeout", wide.Status.String())
	r.Less(elapsed, 5*time.Second, "a per-call timeout must never outlive the check's own")
}

// TestBrowserGlobalIsAlwaysPresent: the global exists whether or not a browser
// can be opened, so a script's own feature detection reads naturally and a
// missing Chrome surfaces as the documented open() failure rather than as
// "browser is not defined".
//
//nolint:paralleltest // mutates the package-level OpenBrowser seam
func TestBrowserGlobalIsAlwaysPresent(t *testing.T) {
	r := require.New(t)

	installFailingBrowser(t, errors.New("no chrome")) //nolint:err113 // message is irrelevant here

	result := runBrowserScript(t, `
return { status: "up", output: { hasBrowser: typeof browser === "object" && typeof browser.open === "function" } };
`, 5*time.Second)

	r.Equal("up", result.Status.String())
	r.Equal(true, result.Output["hasBrowser"])
}

// TestBrowserTextIsCappedAtTheSharedPayloadLimit pins the ONE shared number:
// a page read and an HTTP body are truncated to the same 1 MB.
func TestBrowserTextIsCappedAtTheSharedPayloadLimit(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(maxHTTPBody, checkbrowser.MaxPayloadBytes,
		"the browser payload cap and the HTTP body cap must be one constant")
	r.Equal(1024*1024, checkbrowser.MaxPayloadBytes)
	r.True(strings.HasPrefix(errPageAlreadyOpen.Error(), "a page is already open"))
}
