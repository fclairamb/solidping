// Package checkbrowser provides headless Chrome browser monitoring checks.
package checkbrowser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

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

	// capture replaces the real chromedp screenshot action, for tests only.
	// Nil in production, where captureScreenshot drives chromedp directly.
	//
	// A seam of its own rather than folding into `session`: the capture must
	// be exercised on the same code path that decides WHETHER to capture
	// (failing status, flag set, cap, error tolerance), and `session` replaces
	// everything above that decision.
	capture func(ctx context.Context) ([]byte, error)
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
		sessionBudget += screenshotBudget
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
	//
	// This is what replaced an earlier context.WithoutCancel and it is the
	// whole point of the two-budget split: a child expiring fails the
	// navigation WITHOUT tearing the browser down, whereas a capture running
	// on a context chromedp is no longer managing raced its allocator's
	// cleanup and panicked the process ("close of closed channel"). Every
	// context chromedp sees here is one it owns, start to finish.
	navCtx, navCancel := withProbeDeadline(browserCtx, probeCtx)
	defer navCancel()

	result := c.navigateAndCheck(navCtx, cfg, start, metrics, output)

	// Capture on the BROWSER context: still alive (navCtx expiring did not
	// touch it) and not yet disposed by the deferred browserCancel above.
	c.captureScreenshot(browserCtx, cfg, result)

	return result
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

// screenshotBudget time-boxes the capture. Small on purpose: this runs after
// the check has already decided its verdict, on a worker whose browser slots
// are a scarce shared resource. A page that cannot be photographed in two
// seconds does not get photographed.
const screenshotBudget = 2 * time.Second

// screenshotQuality is the PNG "quality" chromedp passes to CDP. Ignored for
// PNG (it is lossless) but required by the API.
const screenshotQuality = 100

// captureScreenshot hangs a PNG of the failing page on the result, in memory.
//
// THE CAPTURE MUST NEVER CHANGE THE CHECK'S OUTCOME. Every failure mode here
// — no budget left, CDP refusing, an over-cap image — logs and returns,
// leaving the result exactly as the checker produced it. That is why nothing
// in this function writes to result.Status, result.Output or result.Metrics.
//
// Only genuine target failures are photographed. StatusError is excluded on
// purpose: from this checker it means "we could not run a browser at all",
// so there is no page, and StatusUp/Warning have nothing worth keeping.
func (c *BrowserChecker) captureScreenshot(
	ctx context.Context, cfg *BrowserConfig, result *checkerdef.Result,
) {
	if !cfg.Screenshot || result == nil {
		return
	}

	if result.Status != checkerdef.StatusDown && result.Status != checkerdef.StatusTimeout {
		return
	}

	// A plain child of the still-live browser context. NEVER
	// context.WithoutCancel: detaching a chromedp context from the lifecycle
	// chromedp is managing lets the capture race the allocator's teardown,
	// which panics the whole process. The session budget in Execute is what
	// guarantees there is time left on this context to use.
	captureCtx, cancel := context.WithTimeout(ctx, screenshotBudget)
	defer cancel()

	png, err := c.runCapture(captureCtx)
	if err != nil {
		slog.DebugContext(ctx, "browser check screenshot capture failed",
			"url", cfg.URL, "error", err)

		return
	}

	if len(png) == 0 {
		return
	}

	// Dropped, never truncated: half a PNG is a corrupt file, not a smaller
	// image, and it would render as a broken icon on the incident page.
	if len(png) > checkerdef.MaxScreenshotBytes {
		slog.WarnContext(ctx, "browser check screenshot dropped: over the size cap",
			"url", cfg.URL, "bytes", len(png), "cap", checkerdef.MaxScreenshotBytes)

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

// runCapture takes the shot, through the test seam when one is installed.
//
// A panic in here is turned into an ordinary error. That is not defensive
// paranoia about our own code: this is the ONE place a monitoring worker hands
// control to a browser-automation library after the verdict is already
// decided, and chromedp has panicked on lifecycle races before (a
// "close of closed channel" from its allocator took the whole server down
// once). Nothing about an optional screenshot is worth a process that stops
// monitoring everything else.
func (c *BrowserChecker) runCapture(ctx context.Context) ([]byte, error) {
	var (
		png []byte
		err error
	)

	// The recover lives in its own immediately-invoked function rather than on
	// runCapture itself, so the results can stay ordinary locals.
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				png, err = nil, fmt.Errorf("%w: %v", errCapturePanicked, recovered)
			}
		}()

		png, err = c.takeShot(ctx)
	}()

	return png, err
}

// takeShot is the capture itself, through the test seam when one is installed.
func (c *BrowserChecker) takeShot(ctx context.Context) ([]byte, error) {
	if c.capture != nil {
		return c.capture(ctx)
	}

	var buf []byte
	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, screenshotQuality)); err != nil {
		return nil, fmt.Errorf("full screenshot: %w", err)
	}

	return buf, nil
}

// errCapturePanicked marks a capture that panicked rather than failing
// cleanly. Reported like any other capture error: logged, result untouched.
var errCapturePanicked = errors.New("screenshot capture panicked")

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
