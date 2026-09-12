package checkbrowser

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// MaxPayloadBytes caps what a single page read hands back to a caller — the
// text of a selector, the JSON of an evaluated expression.
//
// It is checkerdef's shared payload cap, the same number an HTTP body is
// truncated to in the JS runtime, deliberately expressed as ONE constant: a
// script that can read a megabyte of response body and a script that can read
// a megabyte of page text are the same memory risk, and two numbers would
// drift.
const MaxPayloadBytes = checkerdef.MaxPayloadBytes

// ErrSlotTimeout is the slot wait giving up: every one of the
// MaxConcurrentBrowsers slots was busy for as long as the caller's context
// allowed. It is deliberately NOT an infrastructure failure — the browser is
// fine, this worker is simply full — so it maps to a timeout verdict and
// records nothing on the availability cache.
var ErrSlotTimeout = errors.New("timed out waiting for a free browser slot " +
	"(at most 4 browser checks run at a time on one worker)")

// errSessionClosed is a page method called after Close. Infrastructure by
// classification: there is no browser left to drive.
var errSessionClosed = errors.New("the browser page is closed")

// errBrowserStart is the generic "the browser did not come up" failure, used
// when the underlying error matches none of the recognized shapes.
var errBrowserStart = errors.New("failed to start a browser")

// Session is ONE live headless-Chrome page plus the concurrency slot it holds.
//
// It is the single way this codebase opens, drives and tears down a Chrome:
// the `browser` check type is a verdict layer over it, and the JS runtime's
// `browser` global is a set of goja bindings over it. Anything that changes
// how a page is opened (the pre-flight, the isolated context, the eager
// allocate, the slot) changes both by construction.
type Session struct {
	// browserCtx is the chromedp context the page lives on. Every action runs
	// on a CHILD of it, never on it directly — see run.
	browserCtx context.Context //nolint:containedctx // the session IS a context-bound resource
	// cancels tears the chromedp contexts down, innermost first.
	cancels []context.CancelFunc
	// release returns the concurrency slot. Called exactly once, by Close.
	release func()

	mu     sync.Mutex
	closed bool
}

// NavResult is what a navigation reports back. Deliberately no HTTP status
// code: chromedp.Navigate does not surface the main-frame response status, and
// inferring one would mean subscribing to Network.responseReceived. A script
// that needs the status reads it with page.evaluate or hits the URL with
// http.get (spec 2026-09-12-06, Open questions).
type NavResult struct {
	// URL is the top-frame URL AFTER the navigation, so a redirect is visible.
	URL string
	// Title is the document title.
	Title string
	// Duration is how long the navigation plus the body-ready wait took.
	Duration time.Duration
}

// Cookie is one cookie of the page's browsing context, in the shape a script
// reads it. Expires is CDP's own representation: seconds since the epoch, 0
// for a session cookie.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Secure   bool    `json:"secure"`
	HTTPOnly bool    `json:"httpOnly"`
	Expires  float64 `json:"expires"`
}

// Open acquires a browser slot, pre-flights the CDP endpoint on the remote
// path, opens a fresh isolated context and tab, and allocates the browser
// EAGERLY with an empty chromedp.Run.
//
// The eager allocate is what makes the browserWasAllocated hazard a non-issue
// for page methods: a Session that exists has a browser attached, and one
// whose allocation failed is disposed here and never Run again.
//
// Every error it returns is infrastructure by construction — "we could not run
// a browser at all" — except ErrSlotTimeout, which is "this worker is full".
// Both are things the caller must never report as the target being down.
func Open(ctx context.Context) (*Session, error) {
	return openSession(ctx, ctx)
}

// openSession is Open with the browser check's two budgets kept apart:
// sessionCtx bounds the BROWSER (and so outlives the probe by the screenshot
// budget when a capture is configured), probeCtx bounds the slot wait and the
// pre-flight, which are work the check's own timeout must pay for.
//
// Open collapses the two for every other caller, which is the honest default:
// a JS script has one budget.
func openSession(sessionCtx, probeCtx context.Context) (*Session, error) {
	release, acquired := acquireSlot(probeCtx)
	if !acquired {
		// Nothing is recorded on the availability cache: a full worker says
		// nothing about whether a browser could have been driven.
		return nil, ErrSlotTimeout
	}

	current := CurrentSettings()

	if current.Remote() {
		// Pre-flight the endpoint before touching chromedp. This is what keeps
		// "our sidecar is down" from being reported as "the customer's site is
		// down": once the endpoint answers, every later failure is genuinely
		// about the target.
		if err := probeCDP(probeCtx, current.CDPURL); err != nil {
			release()
			MarkUnavailable()

			return nil, errors.New(cdpUnreachableMessage(current.CDPURL, err))
		}
	}

	allocCtx, allocCancel := allocator(sessionCtx, current)
	browserCtx, browserCancel := browserContext(allocCtx, current)

	// The eager allocate. An empty Run does nothing but bring the browser up,
	// which is exactly what turns "the browser is missing" into an error from
	// Open rather than a mysterious failure of the first real action.
	if err := chromedp.Run(browserCtx); err != nil {
		browserCancel()
		allocCancel()
		release()
		MarkUnavailable()

		return nil, errors.New(openFailureMessage(current, err))
	}

	// A real allocation is stronger evidence than the probe — same rule
	// recordOutcome applies to a finished execution.
	MarkAvailable()

	return &Session{
		browserCtx: browserCtx,
		cancels:    []context.CancelFunc{browserCancel, allocCancel},
		release:    release,
	}, nil
}

// cdpUnreachableMessage is the pre-flight failure, worded so an operator reads
// the fix and not just the symptom.
func cdpUnreachableMessage(cdpURL string, err error) string {
	return "cannot reach the remote Chrome (CDP) endpoint " + cdpURL + ": " + err.Error() +
		" — check that the headless-shell sidecar is running and that " +
		"SP_CHECKERS_BROWSER_CDP_URL points at it"
}

// openFailureMessage classifies a failed eager allocate into the two messages
// operators can act on, falling back to the raw error.
func openFailureMessage(current Settings, err error) string {
	errMsg := err.Error()

	if current.Remote() {
		if isCDPTransportError(errMsg) {
			return "lost the remote Chrome (CDP) connection to " + current.CDPURL + ": " + errMsg +
				" — check that the headless-shell sidecar is running and reachable"
		}
	} else if isChromeMissingError(errMsg) {
		return chromeMissingMessage
	}

	return errBrowserStart.Error() + ": " + errMsg
}

// chromeMissingMessage is the exec path's "there is no binary to run", naming
// every way to give this worker one.
const chromeMissingMessage = "Chrome/Chromium not found: install headless Chrome on this worker, " +
	"set checkers.browser.chrome_path (SP_CHECKERS_BROWSER_CHROME_PATH), or point " +
	"checkers.browser.cdp_url (SP_CHECKERS_BROWSER_CDP_URL) at a headless-shell container"

// Infra reports whether err means the BROWSER is gone (the CDP socket closed,
// the target crashed, the page was closed under us) as opposed to a
// target-side failure such as a selector that never appeared.
//
// It is the split the JS runtime turns into throw-vs-return: infrastructure
// throws and becomes status `error` ("our worker is broken"), everything else
// comes back as `{ ok: false }` and lets the script decide the target is down.
// A context deadline is deliberately NOT infrastructure: that is the check's
// own timeout expiring, which the runtime reports as `timeout`.
func Infra(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, errSessionClosed) || errors.Is(err, errNoBrowserAllocated) {
		return true
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}

	return isCDPTransportError(err.Error())
}

// run executes chromedp actions on a child of the session's browser context
// that dies with the CALLER's context.
//
// This is the load-bearing rule of the whole design (spec 2026-09-12-06 §3).
// The context must be a chromedp DESCENDANT (a detached one races the
// allocator's teardown — see browserWasAllocated) and it must ALSO end when
// the caller's does, because goja's Interrupt is only observed when control
// returns to JavaScript: a binding blocked forever inside chromedp.Run would
// hold a script past its own timeout. context.AfterFunc is what bridges the
// two, since a plain child of browserCtx knows nothing about the caller.
func (s *Session) run(ctx context.Context, actions ...chromedp.Action) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()

	if closed {
		return errSessionClosed
	}

	// The cheap defence the eager allocate already makes redundant: never call
	// Run on a context with no browser attached (it would allocate a second
	// one and can close an already-closed channel inside chromedp).
	if !browserWasAllocated(s.browserCtx) {
		return errNoBrowserAllocated
	}

	runCtx, cancel := context.WithCancel(s.browserCtx)
	defer cancel()

	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	if err := chromedp.Run(runCtx, actions...); err != nil {
		// A cancel that came from the CALLER must surface as the caller's own
		// error, not as chromedp's "context canceled" — that is what lets the
		// runtime tell "the check timed out" from "the page misbehaved".
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		return err
	}

	return nil
}

// Navigate goes to url and waits for the document body to be ready — exactly
// what the browser check does with no waitSelector — then reads the title and
// the (post-redirect) location.
func (s *Session) Navigate(ctx context.Context, url string) (NavResult, error) {
	var (
		title    string
		location string
	)

	start := time.Now()

	err := s.run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body"),
		chromedp.Title(&title),
		chromedp.Location(&location),
	)
	if err != nil {
		return NavResult{}, err
	}

	return NavResult{URL: location, Title: title, Duration: time.Since(start)}, nil
}

// WaitVisible waits for a selector to become visible.
func (s *Session) WaitVisible(ctx context.Context, sel string) error {
	return s.run(ctx, chromedp.WaitVisible(sel))
}

// Click clicks the first node matching sel, scrolling it into view first
// (chromedp.Click's own default).
func (s *Session) Click(ctx context.Context, sel string) error {
	return s.run(ctx, chromedp.Click(sel))
}

// Fill clears the field and SENDS KEYS into it.
//
// Never SetValue: writing `.value` directly bypasses the input events a
// controlled React/Vue input listens for, so a login form would submit fields
// its own framework still believes are empty.
func (s *Session) Fill(ctx context.Context, sel, text string) error {
	return s.run(ctx, chromedp.Clear(sel), chromedp.SendKeys(sel, text))
}

// Press sends one key to the element — "Enter" to submit a form, "Tab" to move
// on. Key names are the familiar DOM ones; anything else is sent verbatim, so
// a single character works too.
func (s *Session) Press(ctx context.Context, sel, key string) error {
	return s.run(ctx, chromedp.SendKeys(sel, keySequence(key)))
}

// keySequence maps the handful of named keys a form flow needs onto the
// literal runes chromedp's keyboard encoder understands. An unrecognized name
// is passed through untouched, which is what makes `press(sel, "a")` work.
func keySequence(key string) string {
	switch strings.ToLower(key) {
	case "enter", "return":
		return kb.Enter
	case "tab":
		return kb.Tab
	case "escape", "esc":
		return kb.Escape
	case "backspace":
		return kb.Backspace
	case "delete":
		return kb.Delete
	case "arrowdown", "down":
		return kb.ArrowDown
	case "arrowup", "up":
		return kb.ArrowUp
	case "arrowleft", "left":
		return kb.ArrowLeft
	case "arrowright", "right":
		return kb.ArrowRight
	default:
		return key
	}
}

// Text returns the visible text of the first node matching sel, capped at
// MaxPayloadBytes.
func (s *Session) Text(ctx context.Context, sel string) (string, error) {
	var text string

	if err := s.run(ctx, chromedp.Text(sel, &text)); err != nil {
		return "", err
	}

	return capPayload(text), nil
}

// Evaluate runs expr INSIDE the page (real DOM, page origin) and returns its
// JSON-serialisable value. A returned Promise is awaited by chromedp, bounded
// by ctx like every other action. The result is capped at MaxPayloadBytes.
func (s *Session) Evaluate(ctx context.Context, expr string) (any, error) {
	var raw any

	// AwaitPromise so `page.evaluate("fetch(...).then(r => r.status)")` works
	// the way a reader expects; a non-promise value is unaffected.
	if err := s.run(ctx, chromedp.Evaluate(expr, &raw, awaitPromise)); err != nil {
		return nil, err
	}

	if str, ok := raw.(string); ok {
		return capPayload(str), nil
	}

	return raw, nil
}

// URL reports the current top-frame URL — how a script asserts it landed on
// /dashboard rather than back on /login.
func (s *Session) URL(ctx context.Context) (string, error) {
	var location string

	if err := s.run(ctx, chromedp.Location(&location)); err != nil {
		return "", err
	}

	return location, nil
}

// Cookies returns the cookies of the page's browsing context, so a script can
// hand a browser-established session to a plain http.* call.
func (s *Session) Cookies(ctx context.Context) ([]Cookie, error) {
	var cookies []Cookie

	err := s.run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		raw, err := network.GetCookies().Do(actionCtx)
		if err != nil {
			return err
		}

		cookies = make([]Cookie, 0, len(raw))
		for _, cookie := range raw {
			cookies = append(cookies, Cookie{
				Name:     cookie.Name,
				Value:    cookie.Value,
				Domain:   cookie.Domain,
				Path:     cookie.Path,
				Secure:   cookie.Secure,
				HTTPOnly: cookie.HTTPOnly,
				Expires:  cookie.Expires,
			})
		}

		return nil
	}))
	if err != nil {
		return nil, err
	}

	return cookies, nil
}

// Screenshot captures a full-page PNG of the current tab, time-boxed by the
// same screenshotTimeout the browser check's capture uses.
func (s *Session) Screenshot(ctx context.Context) ([]byte, error) {
	shotCtx, cancel := context.WithTimeout(ctx, screenshotTimeout)
	defer cancel()

	var buf []byte

	if err := s.run(shotCtx, chromedp.FullScreenshot(&buf, screenshotQuality)); err != nil {
		return nil, err
	}

	return buf, nil
}

// Close disposes the isolated context and its tab and returns the concurrency
// slot. Idempotent: the JS runtime calls it from a defer AND lets a script
// call page.close() explicitly.
func (s *Session) Close() {
	s.mu.Lock()

	if s.closed {
		s.mu.Unlock()

		return
	}

	s.closed = true
	s.mu.Unlock()

	// Innermost first: the browser context, then the allocator.
	for _, cancel := range s.cancels {
		cancel()
	}

	if s.release != nil {
		s.release()
	}
}

// awaitPromise makes chromedp wait for a returned Promise to settle before
// reading the value. It is bounded by the same context every other action runs
// on, so a promise that never settles ends with the caller's deadline.
func awaitPromise(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
	return p.WithAwaitPromise(true)
}

// capPayload truncates a page read to the shared payload cap. Truncation
// rather than refusal keeps a script that greps a huge page working; the cap
// exists to bound memory, not to police the target.
func capPayload(value string) string {
	if len(value) <= MaxPayloadBytes {
		return value
	}

	return value[:MaxPayloadBytes]
}
