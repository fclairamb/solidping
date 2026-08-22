// Package checkbrowser provides headless Chrome browser monitoring checks.
package checkbrowser

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// MaxScreenshotBytes caps a single captured PNG (spec 2026-08-21-01).
//
// Deliberately far below files.MaxFileSize (25 MB): a full-page PNG of a real
// site is tens to a few hundred KB, so anything past 4 MiB is a pathological
// page (an enormous infinite-scroll canvas, a print-stylesheet poster) whose
// capture is worth less than the storage and transfer it costs. An over-cap
// capture is DROPPED, never truncated — a half-written PNG is not an image,
// it is a corrupt file that renders as a broken icon three days later.
const MaxScreenshotBytes = 4 * 1024 * 1024

// screenshotTimeout time-boxes the capture. The capture runs AFTER the verdict
// is decided, so this budget can never delay or change a check's outcome; it
// exists so a wedged renderer cannot hold a browser slot open indefinitely.
const screenshotTimeout = 5 * time.Second

// screenshotQuality is chromedp.FullScreenshot's quality argument. It is
// ignored for PNG (lossless) and only matters if this ever switches to JPEG;
// 90 is the conventional value to pass.
const screenshotQuality = 90

// MaxConcurrentBrowsers caps simultaneous browser executions per worker
// process, on BOTH the remote and the exec path.
//
// Four, deliberately low: a browser execution is the most expensive check type
// there is (a tab, a renderer, tens of MB of RAM, seconds of wall clock), and
// the runner pool is sized for cheap I/O-bound probes — 25 concurrent tabs
// would either OOM the sidecar or make every one of them slow enough to time
// out. Waiting happens INSIDE the check's own timeout budget, so a saturated
// worker reports a timeout on the late check instead of silently queueing it.
const MaxConcurrentBrowsers = 4

// browserSlots is the semaphore behind MaxConcurrentBrowsers. Process-wide,
// because the resource it protects (the Chrome the whole process shares) is.
//
//nolint:gochecknoglobals // process-wide concurrency cap, see MaxConcurrentBrowsers
var browserSlots = make(chan struct{}, MaxConcurrentBrowsers)

// BrowserChecker implements the Checker interface for headless Chrome browser checks.
type BrowserChecker struct {
	// session replaces the real browser session, for tests only. Production
	// code always builds a zero-value BrowserChecker (the registry does), so
	// this is nil and runBrowser takes the real path. It is spliced in AFTER
	// the CDP pre-flight so a test still exercises the pre-flight it is meant
	// to be a positive control for.
	session func(
		ctx context.Context, cfg *BrowserConfig, start time.Time, metrics, output map[string]any,
	) *checkerdef.Result

	// screenshot replaces the real chromedp capture, for tests only. Nil in
	// production, where captureScreenshot falls back to fullScreenshot. It is
	// a separate seam from `session` on purpose: a test needs to drive the
	// capture decision (opted in? failing? over cap? errored?) without also
	// having to fake a browser session that produces the right verdict.
	screenshot func(ctx context.Context) ([]byte, error)
}

// acquireSlot waits for one of the MaxConcurrentBrowsers slots, giving up when
// the check's context (which already carries the check's timeout) is done.
// The returned release must be called exactly once.
func acquireSlot(ctx context.Context) (func(), bool) {
	select {
	case browserSlots <- struct{}{}:
		return func() { <-browserSlots }, true
	case <-ctx.Done():
		return nil, false
	}
}

// Type returns the check type identifier.
func (c *BrowserChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeBrowser
}

// Validate checks if the configuration is valid.
func (c *BrowserChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &BrowserConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = hostnameFromURL(cfg.URL)
	}

	if spec.Slug == "" {
		spec.Slug = "browser-" + strings.ReplaceAll(hostnameFromURL(cfg.URL), ".", "-")
	}

	return nil
}

// Execute performs the browser health check and returns the result.
func (c *BrowserChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*BrowserConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.resolveTimeout()

	// TWO nested budgets, and the distinction is load-bearing.
	//
	// The SESSION bounds the browser itself and outlives the probe by exactly
	// the screenshot budget when a capture is configured, because the shot has
	// to be taken while the tab is still alive. The PROBE keeps the check's
	// own timeout exactly as it was, so a capture never buys the target extra
	// time to answer in.
	//
	// Both descend from the caller's ctx, so a worker shutdown still tears the
	// whole thing down — nothing here is ever detached from cancellation.
	sessionBudget := timeout
	if cfg.Screenshot {
		sessionBudget += screenshotTimeout
	}

	sessionCtx, cancelSession := context.WithTimeout(ctx, sessionBudget)
	defer cancelSession()

	probeCtx, cancelProbe := context.WithTimeout(sessionCtx, timeout)
	defer cancelProbe()

	start := time.Now()

	metrics := map[string]any{}
	output := map[string]any{
		"url": cfg.URL,
	}

	// The cap is enforced here, around the whole session, and the wait counts
	// against the check's own timeout — an execution that never gets a slot
	// reports a timeout rather than queueing invisibly.
	release, acquired := acquireSlot(probeCtx)
	if !acquired {
		output["error"] = "timed out waiting for a free browser slot " +
			"(at most 4 browser checks run at a time on one worker)"

		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}, nil
	}

	defer release()

	result := c.runBrowser(sessionCtx, probeCtx, cfg, start, metrics, output)

	recordOutcome(result.Status)

	return result, nil
}

// recordOutcome feeds a finished execution back into the capability cache.
//
// StatusError from this checker is INFRASTRUCTURE by construction — every path
// that produces it is "no Chrome here" or "the CDP endpoint is not answering",
// never a target verdict — so it drops the capability immediately instead of
// waiting out the TTL. Up/down means a browser really ran, which refreshes it.
// A timeout says nothing either way and is left alone.
func recordOutcome(status checkerdef.Status) {
	switch status {
	case checkerdef.StatusError:
		MarkUnavailable()
	case checkerdef.StatusUp, checkerdef.StatusDown:
		MarkAvailable()
	case checkerdef.StatusRunning, checkerdef.StatusTimeout,
		checkerdef.StatusDegraded, checkerdef.StatusWarning:
	}
}

// runBrowser drives one execution.
//
// sessionCtx bounds the BROWSER (probe timeout plus the screenshot budget);
// probeCtx bounds the PROBE (the check's own timeout). The browser is built on
// the former and the navigation on the latter, which is what leaves a live tab
// to photograph after a probe that timed out or failed.
func (c *BrowserChecker) runBrowser(
	sessionCtx context.Context,
	probeCtx context.Context,
	cfg *BrowserConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	current := CurrentSettings()

	if current.Remote() {
		// Pre-flight the endpoint before touching chromedp. This is what keeps
		// "our sidecar is down" from being reported as "the customer's site is
		// down": once the endpoint answers, every later failure is genuinely
		// about the target.
		if err := probeCDP(probeCtx, current.CDPURL); err != nil {
			return infraResult(
				"cannot reach the remote Chrome (CDP) endpoint "+current.CDPURL+": "+err.Error()+
					" — check that the headless-shell sidecar is running and that "+
					"SP_CHECKERS_BROWSER_CDP_URL points at it",
				start, metrics, output,
			)
		}
	}

	if c.session != nil {
		result := c.session(probeCtx, cfg, start, metrics, output)
		c.captureScreenshot(sessionCtx, cfg, result)

		return result
	}

	allocCtx, allocCancel := allocator(sessionCtx, current)
	defer allocCancel()

	browserCtx, browserCancel := browserContext(allocCtx, current)
	defer browserCancel()

	// The probe's deadline, re-applied as a CHILD of the live browser context.
	// A child expiring fails the navigation WITHOUT tearing the browser down,
	// which is what leaves something to photograph — and, unlike a detached
	// context, keeps every context chromedp sees one that it owns.
	navCtx, navCancel := withProbeDeadline(browserCtx, probeCtx)
	defer navCancel()

	result := c.navigateAndCheck(navCtx, cfg, start, metrics, output)

	// Capture here, not inside the verdict paths: this is the last point at
	// which the browser context is still alive (the defers above run when this
	// function returns), and it is the ONE place every failing path funnels
	// through — a new verdict branch cannot forget to capture.
	c.captureScreenshot(browserCtx, cfg, result)

	return result
}

// captureScreenshot hangs a PNG of the failing page on the result, when the
// check opted in. Best-effort by construction: it runs AFTER the verdict is
// decided and mutates only Diagnostics, so no outcome of this function — a
// capture error, an over-cap image, a dead browser — can change whether the
// check is reported up or down. That is the whole safety argument, and it is
// why this is a void function with no error return to ignore.
func (c *BrowserChecker) captureScreenshot(
	ctx context.Context, cfg *BrowserConfig, result *checkerdef.Result,
) {
	if !cfg.Screenshot || result == nil || !capturableStatus(result.Status) {
		return
	}

	capture := c.screenshot
	if capture == nil {
		capture = fullScreenshot
	}

	// A plain child of the still-live SESSION context.
	//
	// NEVER context.WithoutCancel here, however tempting: the common
	// capture-worthy failure is the check timing out, but detaching the
	// capture from the lifecycle chromedp manages lets it race the allocator's
	// teardown and panic the whole process. The session budget in Execute is
	// what guarantees there is time left on this context instead.
	shotCtx, cancel := context.WithTimeout(ctx, screenshotTimeout)
	defer cancel()

	png, err := capture(shotCtx)
	if err != nil {
		slog.WarnContext(ctx, "browser check: screenshot capture failed",
			"url", cfg.URL, "error", err)

		return
	}

	if len(png) == 0 {
		return
	}

	if len(png) > MaxScreenshotBytes {
		slog.WarnContext(ctx, "browser check: screenshot dropped, over cap",
			"url", cfg.URL, "bytes", len(png), "cap", MaxScreenshotBytes)

		return
	}

	if result.Diagnostics == nil {
		result.Diagnostics = &checkerdef.Diagnostics{}
	}

	result.Diagnostics.Screenshot = &checkerdef.Screenshot{
		PNG:        png,
		CapturedAt: time.Now(),
	}
}

// capturableStatus reports whether a verdict is worth a screenshot.
//
// StatusError is excluded on purpose: from this checker it means "no browser
// to drive" (Chrome missing, CDP endpoint unreachable), so there is no page to
// photograph and the attempt would only log noise. Down and Timeout are the
// two verdicts where a real tab really did look at the target.
func capturableStatus(status checkerdef.Status) bool {
	return status == checkerdef.StatusDown || status == checkerdef.StatusTimeout
}

// withProbeDeadline re-applies probeCtx's deadline onto a child of parent.
// Falls back to a plain cancelable child when the probe carries no deadline,
// which only happens if a caller hands in an unbounded context.
func withProbeDeadline(
	parent context.Context, probeCtx context.Context,
) (context.Context, context.CancelFunc) {
	if deadline, ok := probeCtx.Deadline(); ok {
		return context.WithDeadline(parent, deadline)
	}

	return context.WithCancel(parent)
}

// errNoBrowserAllocated marks a capture skipped because no browser was ever
// attached to the context. See browserWasAllocated for why that is not merely
// pointless but dangerous.
var errNoBrowserAllocated = errors.New("no browser was allocated for this execution")

// browserWasAllocated reports whether a browser is actually attached to this
// chromedp context.
//
// This guard is NOT an optimization — it prevents a crash that takes the whole
// server down. chromedp's `Run` funnels through `initContextBrowser`, which
// calls `Allocator.Allocate` **whenever `c.Browser == nil`**, and
// `ExecAllocator.Allocate` in turn arms a goroutine that does
// `close(c.allocated)` once the Chrome process exits.
//
// So when the probe's own Run fails AFTER that goroutine is armed but BEFORE
// `c.Browser` is set — a Chrome that starts and then fails its CDP handshake,
// which is exactly what a sandbox-restricted CI runner produces — the context
// is left with a nil Browser and an already-closing `allocated` channel. A
// second Run on it allocates again and closes that channel a second time:
//
//	panic: close of closed channel
//	chromedp.(*ExecAllocator).Allocate.func2()
//
// That panic happens on a goroutine chromedp owns, so no recover() of ours can
// contain it. Not calling Run at all is the only reliable defense, and it
// costs nothing real: a context with no browser has no page worth capturing.
func browserWasAllocated(ctx context.Context) bool {
	chromeCtx := chromedp.FromContext(ctx)

	return chromeCtx != nil && chromeCtx.Browser != nil
}

// fullScreenshot is the production capture: a full-page PNG of the current tab.
func fullScreenshot(ctx context.Context) ([]byte, error) {
	// The guard sits immediately before the only chromedp.Run this package
	// makes outside the probe itself, because making that call is precisely
	// the hazard — see browserWasAllocated.
	if !browserWasAllocated(ctx) {
		return nil, errNoBrowserAllocated
	}

	var buf []byte

	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, screenshotQuality)); err != nil {
		return nil, err
	}

	return buf, nil
}

// allocator builds the chromedp allocator for the configured backend: a remote
// one against the long-lived Chrome when a CDP URL is set, the historical exec
// allocator otherwise.
func allocator(ctx context.Context, current Settings) (context.Context, context.CancelFunc) {
	if current.Remote() {
		return chromedp.NewRemoteAllocator(ctx, current.CDPURL)
	}

	opts := chromedp.DefaultExecAllocatorOptions[:]
	if current.ChromePath != "" {
		// Copy before appending: DefaultExecAllocatorOptions is a package-level
		// array and appending to a full slice of it would write into it.
		opts = append(append([]chromedp.ExecAllocatorOption{}, opts...), chromedp.ExecPath(current.ChromePath))
	}

	return chromedp.NewExecAllocator(ctx, opts...)
}

// browserContext opens the per-execution browser context.
//
// On the remote path that is a fresh ISOLATED (incognito) browser context plus
// its tab, disposed when this context is canceled — so consecutive checks on
// one long-lived Chrome never share cookies, storage or a service worker. The
// exec path allocates its own short-lived process, so its default context is
// already isolated.
func browserContext(allocCtx context.Context, current Settings) (context.Context, context.CancelFunc) {
	if current.Remote() {
		return chromedp.NewContext(allocCtx, chromedp.WithNewBrowserContext())
	}

	return chromedp.NewContext(allocCtx)
}

// infraResult is the shape of "we could not run a browser at all". StatusError,
// never StatusDown: the target was never contacted, so calling it down would
// page a customer for our own outage.
func infraResult(
	message string, start time.Time, metrics, output map[string]any,
) *checkerdef.Result {
	output["error"] = message

	return &checkerdef.Result{
		Status:   checkerdef.StatusError,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func (c *BrowserChecker) navigateAndCheck(
	ctx context.Context,
	cfg *BrowserConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	var title string

	navStart := time.Now()

	actions := []chromedp.Action{
		chromedp.Navigate(cfg.URL),
	}

	if cfg.WaitSelector != "" {
		actions = append(actions, chromedp.WaitVisible(cfg.WaitSelector))
	} else {
		actions = append(actions, chromedp.WaitReady("body"))
	}

	actions = append(actions, chromedp.Title(&title))

	if err := chromedp.Run(ctx, actions...); err != nil {
		return c.handleBrowserError(ctx, err, start, metrics, output)
	}

	metrics["load_time_ms"] = durationMs(time.Since(navStart))

	output["title"] = title

	if cfg.Keyword != "" {
		return c.checkKeyword(ctx, cfg, start, metrics, output)
	}

	metrics["total_time_ms"] = durationMs(time.Since(start))

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func (c *BrowserChecker) checkKeyword(
	ctx context.Context,
	cfg *BrowserConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	var bodyText string
	if err := chromedp.Run(ctx, chromedp.Text("body", &bodyText)); err != nil {
		output["error"] = "failed to read page text: " + err.Error()

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	found := strings.Contains(bodyText, cfg.Keyword)
	if cfg.InvertKeyword {
		found = !found
	}

	output["keywordFound"] = found
	metrics["total_time_ms"] = durationMs(time.Since(start))

	if !found {
		output["error"] = "keyword check failed"

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func (c *BrowserChecker) handleBrowserError(
	ctx context.Context,
	err error,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	if ctx.Err() != nil {
		output["error"] = "browser check timed out"

		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	errMsg := err.Error()
	current := CurrentSettings()

	// Infrastructure faults, both paths: no browser to drive. StatusError, so
	// the failure reads as ours and never as the target's.
	if current.Remote() {
		if isCDPTransportError(errMsg) {
			return infraResult(
				"lost the remote Chrome (CDP) connection to "+current.CDPURL+": "+errMsg+
					" — check that the headless-shell sidecar is running and reachable",
				start, metrics, output,
			)
		}
	} else if isChromeMissingError(errMsg) {
		return infraResult(
			"Chrome/Chromium not found: install headless Chrome on this worker, "+
				"set checkers.browser.chrome_path (SP_CHECKERS_BROWSER_CHROME_PATH), or point "+
				"checkers.browser.cdp_url (SP_CHECKERS_BROWSER_CDP_URL) at a headless-shell container",
			start, metrics, output,
		)
	}

	output["error"] = errMsg

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func durationMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / microsecondsPerMilli
}

// isChromeMissingError recognizes the exec allocator failing because there is
// no binary to run. Matched on the message because chromedp wraps os/exec's
// error without a typed sentinel.
func isChromeMissingError(errMsg string) bool {
	return strings.Contains(errMsg, "exec") ||
		strings.Contains(errMsg, "not found") ||
		strings.Contains(errMsg, "no such file")
}

// isCDPTransportError recognizes the remote allocator failing to reach or keep
// the connection to the browser, as opposed to the page failing to load.
//
// This is the load-bearing half of the remote path's status mapping: the
// pre-flight already catches an endpoint that is down before the run starts, so
// this covers the sidecar dying MID-run, which would otherwise surface as a
// navigation error and be reported as the target being down.
func isCDPTransportError(errMsg string) bool {
	for _, marker := range []string{
		"connection refused",
		"connection reset",
		"websocket",
		"could not dial",
		"failed to modify wsURL",
		"use of closed network connection",
		"unexpected EOF",
		"EOF",
		"no such host",
		"i/o timeout",
		"broken pipe",
	} {
		if strings.Contains(errMsg, marker) {
			return true
		}
	}

	return false
}
