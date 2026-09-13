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
	// production, where captureScreenshot delegates to Session.Screenshot —
	// the ONE capture this package makes, shared with the JS runtime's
	// page.screenshot(). It is a separate seam from `session` on purpose: a
	// test needs to drive the capture decision (opted in? failing? over cap?
	// errored?) without also having to fake a browser session that produces
	// the right verdict.
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

	// The concurrency cap lives in openSession, around the whole session, and
	// its wait counts against the check's own timeout (probeCtx) — an
	// execution that never gets a slot reports a timeout rather than queueing
	// invisibly. See runBrowser's ErrSlotTimeout branch.
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

// runBrowser drives one execution, as a thin verdict layer over Session.
//
// sessionCtx bounds the BROWSER (probe timeout plus the screenshot budget);
// probeCtx bounds the PROBE (the check's own timeout, and the slot wait and
// pre-flight inside it). The session is opened on the former and driven on the
// latter, which is what leaves a live tab to photograph after a probe that
// timed out or failed.
func (c *BrowserChecker) runBrowser(
	sessionCtx context.Context,
	probeCtx context.Context,
	cfg *BrowserConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	if c.session != nil {
		// The test seam is spliced in AFTER the CDP pre-flight, so a test that
		// fakes the session still exercises the pre-flight it is a positive
		// control for. openSession does the same pre-flight on the real path.
		if current := CurrentSettings(); current.Remote() {
			if err := probeCDP(probeCtx, current.CDPURL); err != nil {
				return infraResult(cdpUnreachableMessage(current.CDPURL, err), start, metrics, output)
			}
		}

		result := c.session(probeCtx, cfg, start, metrics, output)

		// No Session on this path, so there is nothing to capture FROM unless
		// the test also replaced the capture seam — which is exactly what a
		// screenshot test does. Without it the capture is refused rather than
		// reaching for a browser that was never opened.
		c.captureScreenshot(sessionCtx, cfg, result, nil)

		return result
	}

	session, err := openSession(sessionCtx, probeCtx)
	if err != nil {
		if errors.Is(err, ErrSlotTimeout) {
			output["error"] = err.Error()

			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: time.Since(start),
				Metrics:  metrics,
				Output:   output,
			}
		}

		// Everything else Open returns is infrastructure by construction:
		// StatusError, never StatusDown.
		return infraResult(err.Error(), start, metrics, output)
	}

	defer session.Close()

	result := c.navigateAndCheck(probeCtx, session, cfg, start, metrics, output)

	// Capture here, not inside the verdict paths: the session is still alive
	// (Close runs when this function returns), and this is the ONE place every
	// failing path funnels through — a new verdict branch cannot forget to
	// capture. sessionCtx is the budget that outlives the probe by exactly the
	// screenshot allowance; the session itself supplies the chromedp context.
	c.captureScreenshot(sessionCtx, cfg, result, session)

	return result
}

// captureScreenshot hangs a PNG of the failing page on the result, when the
// check opted in. Best-effort by construction: it runs AFTER the verdict is
// decided and mutates only Diagnostics, so no outcome of this function — a
// capture error, an over-cap image, a dead browser — can change whether the
// check is reported up or down. That is the whole safety argument, and it is
// why this is a void function with no error return to ignore.
func (c *BrowserChecker) captureScreenshot(
	ctx context.Context, cfg *BrowserConfig, result *checkerdef.Result, session *Session,
) {
	if !cfg.Screenshot || result == nil || !capturableStatus(result.Status) {
		return
	}

	// ONE real capture path, with a seam in front of it: the default delegates
	// to Session.Screenshot — the same method the JS runtime's
	// page.screenshot() calls — rather than making a second chromedp.Run of
	// its own. The `screenshot` field stays a seam so a test can drive the
	// capture DECISION (opted in? failing? over cap? errored?) without having
	// to produce a real browser.
	capture := c.screenshot
	if capture == nil {
		if session == nil {
			return
		}

		capture = session.Screenshot
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

// CapturableStatus is capturableStatus for callers outside this package — the
// JS runtime, which lets a SCRIPT decide when to shoot but keeps the capture
// only for the verdicts a browser check would have kept it for (spec
// 2026-09-12-06 §5). Sharing the predicate is what keeps storage, retention
// and the incident card unchanged.
func CapturableStatus(status checkerdef.Status) bool {
	return capturableStatus(status)
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

// navigateAndCheck is the verdict layer: navigate, optionally wait for the
// configured selector, then optionally match the keyword. Every browser
// interaction goes through Session, which is the same code path the JS
// runtime's `browser` global drives.
func (c *BrowserChecker) navigateAndCheck(
	ctx context.Context,
	session *Session,
	cfg *BrowserConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	navStart := time.Now()

	nav, err := session.Navigate(ctx, cfg.URL)
	if err != nil {
		return c.handleBrowserError(ctx, err, start, metrics, output)
	}

	if cfg.WaitSelector != "" {
		if waitErr := session.WaitVisible(ctx, cfg.WaitSelector); waitErr != nil {
			return c.handleBrowserError(ctx, waitErr, start, metrics, output)
		}
	}

	// load_time_ms spans the navigation AND the selector wait, exactly as it
	// did when both were one chromedp.Run.
	metrics["load_time_ms"] = durationMs(time.Since(navStart))

	output["title"] = nav.Title

	if cfg.Keyword != "" {
		return c.checkKeyword(ctx, session, cfg, start, metrics, output)
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
	session *Session,
	cfg *BrowserConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	bodyText, err := session.Text(ctx, "body")
	if err != nil {
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

	// Infrastructure faults, both paths: no browser to drive. StatusError, so
	// the failure reads as ours and never as the target's. Open already
	// classified everything that can go wrong BEFORE the first action (a
	// missing binary, an unreachable endpoint); what is left here is the
	// sidecar dying MID-run, which Infra recognizes.
	if Infra(err) {
		current := CurrentSettings()
		if current.Remote() {
			return infraResult(
				"lost the remote Chrome (CDP) connection to "+current.CDPURL+": "+errMsg+
					" — check that the headless-shell sidecar is running and reachable",
				start, metrics, output,
			)
		}

		return infraResult("lost the connection to the browser: "+errMsg, start, metrics, output)
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
