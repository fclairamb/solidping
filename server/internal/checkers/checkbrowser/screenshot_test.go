package checkbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// testPageURL / cfgKeyURL keep the shared literals in one place.
const (
	testPageURL = "https://example.com"
	cfgKeyURL   = "url"
)

// errRendererGone stands in for a capture that failed inside chromedp.
var errRendererGone = errors.New("renderer gone")

// fakePNG is a recognizable byte blob standing in for a real capture. Nothing
// in the capture path parses the image, so its content only has to be unique
// enough to assert on.
func fakePNG() []byte {
	return []byte("\x89PNG\r\n\x1a\nfake-screenshot-bytes")
}

// screenshotChecker builds a checker whose session returns `status` and whose
// capture seam returns whatever `capture` says. Both seams are needed: one
// decides the verdict, the other decides what the capture does.
func screenshotChecker(
	status checkerdef.Status, capture func(ctx context.Context) ([]byte, error),
) *BrowserChecker {
	return &BrowserChecker{
		session: func(
			_ context.Context, _ *BrowserConfig, start time.Time, metrics, output map[string]any,
		) *checkerdef.Result {
			return &checkerdef.Result{
				Status: status, Duration: time.Since(start), Metrics: metrics, Output: output,
			}
		},
		screenshot: capture,
	}
}

func screenshotSpec(enabled bool) *BrowserConfig {
	return &BrowserConfig{URL: testPageURL, Timeout: 5 * time.Second, Screenshot: enabled}
}

// TestScreenshotCapturedOnlyOnOptedInFailure is the core behavior: the capture
// happens when the check opted in AND failed, and in no other combination.
//
//nolint:paralleltest // mutates the process-wide settings
func TestScreenshotCapturedOnlyOnOptedInFailure(t *testing.T) {
	server := fakeCDPServer(t)
	withSettings(t, Settings{CDPURL: server.URL})

	cases := []struct {
		name    string
		enabled bool
		status  checkerdef.Status
		want    bool
	}{
		{"opted in and down", true, checkerdef.StatusDown, true},
		{"opted in and timed out", true, checkerdef.StatusTimeout, true},
		{"opted in but up", true, checkerdef.StatusUp, false},
		{"opted in but infrastructure error", true, checkerdef.StatusError, false},
		{"failing but not opted in", false, checkerdef.StatusDown, false},
	}

	for _, tc := range cases {
		//nolint:paralleltest // shares the process-wide settings installed above
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			calls := 0
			checker := screenshotChecker(tc.status, func(context.Context) ([]byte, error) {
				calls++

				return fakePNG(), nil
			})

			result, err := checker.Execute(t.Context(), screenshotSpec(tc.enabled))
			r.NoError(err)
			r.Equal(tc.status, result.Status, "the capture must never change the verdict")

			if !tc.want {
				r.Zero(calls, "no capture should have been attempted")
				if result.Diagnostics != nil {
					r.Nil(result.Diagnostics.Screenshot)
				}

				return
			}

			r.Equal(1, calls)
			r.NotNil(result.Diagnostics)
			r.NotNil(result.Diagnostics.Screenshot)
			r.Equal(fakePNG(), result.Diagnostics.Screenshot.PNG)
			r.False(result.Diagnostics.Screenshot.CapturedAt.IsZero())
		})
	}
}

// TestScreenshotFailureNeverChangesOutcome pins the safety property: whatever
// the capture does — error, empty, oversize — the check reports exactly the
// verdict it would have reported with the feature switched off.
//
//nolint:paralleltest // mutates the process-wide settings
func TestScreenshotFailureNeverChangesOutcome(t *testing.T) {
	server := fakeCDPServer(t)
	withSettings(t, Settings{CDPURL: server.URL})

	cases := []struct {
		name    string
		capture func(ctx context.Context) ([]byte, error)
	}{
		{"capture errors", func(context.Context) ([]byte, error) {
			return nil, errRendererGone
		}},
		{"capture returns nothing", func(context.Context) ([]byte, error) {
			return nil, nil
		}},
		{"capture is over the cap", func(context.Context) ([]byte, error) {
			return make([]byte, MaxScreenshotBytes+1), nil
		}},
	}

	for _, tc := range cases {
		//nolint:paralleltest // shares the process-wide settings installed above
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			checker := screenshotChecker(checkerdef.StatusDown, tc.capture)

			result, err := checker.Execute(t.Context(), screenshotSpec(true))
			r.NoError(err)
			r.Equal(checkerdef.StatusDown, result.Status)

			if result.Diagnostics != nil {
				r.Nil(result.Diagnostics.Screenshot,
					"a failed or unusable capture must be dropped, never attached")
			}
		})
	}

	// Positive control: the same harness DOES attach a usable capture, so the
	// assertions above are about the failure handling and not about a capture
	// path that never attaches anything.
	//nolint:paralleltest // shares the process-wide settings installed above
	t.Run("positive control", func(t *testing.T) {
		r := require.New(t)

		checker := screenshotChecker(checkerdef.StatusDown, func(context.Context) ([]byte, error) {
			return make([]byte, MaxScreenshotBytes), nil
		})

		result, err := checker.Execute(t.Context(), screenshotSpec(true))
		r.NoError(err)
		r.NotNil(result.Diagnostics)
		r.NotNil(result.Diagnostics.Screenshot)
		r.Len(result.Diagnostics.Screenshot.PNG, MaxScreenshotBytes,
			"exactly at the cap is accepted; only past it is dropped")
	})
}

// TestScreenshotCaptureSurvivesAnExpiredCheckContext proves the two-budget
// split in Execute is load-bearing: the capture-worthy failure IS the timeout,
// and by then the PROBE's context is already Done. A capture inheriting that
// would never run; it runs on the SESSION context, which outlives the probe by
// exactly the screenshot budget.
//
// This used to be achieved with context.WithoutCancel, which also detached the
// capture from the lifecycle chromedp manages and panicked the whole process.
// See TestScreenshotStaysBoundToCallerCancellation for the guard against that
// returning.
//
//nolint:paralleltest // mutates the process-wide settings
func TestScreenshotCaptureSurvivesAnExpiredCheckContext(t *testing.T) {
	server := fakeCDPServer(t)
	withSettings(t, Settings{CDPURL: server.URL})

	r := require.New(t)

	var sawDeadline bool

	checker := &BrowserChecker{
		session: func(
			ctx context.Context, _ *BrowserConfig, start time.Time, metrics, output map[string]any,
		) *checkerdef.Result {
			// Burn the whole (deliberately tiny) check budget, exactly as a
			// real timing-out navigation would.
			<-ctx.Done()

			return &checkerdef.Result{
				Status: checkerdef.StatusTimeout, Duration: time.Since(start),
				Metrics: metrics, Output: output,
			}
		},
		screenshot: func(ctx context.Context) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}

			_, sawDeadline = ctx.Deadline()

			return fakePNG(), nil
		},
	}

	spec := &BrowserConfig{URL: testPageURL, Timeout: 20 * time.Millisecond, Screenshot: true}

	result, err := checker.Execute(t.Context(), spec)
	r.NoError(err)
	r.Equal(checkerdef.StatusTimeout, result.Status)
	r.NotNil(result.Diagnostics)
	r.NotNil(result.Diagnostics.Screenshot, "a timed-out check must still get its capture")
	r.True(sawDeadline, "the capture runs on its own fresh budget, not an already-expired one")
}

// TestScreenshotBytesNeverCrossTheAgentControlChannel is the security pin from
// the spec: Diagnostics rides the agent WebSocket result frame as JSON, and a
// megabyte PNG must never be smuggled through it as base64.
//
// FailureResponse is the positive control — it is on the SAME struct and it
// DOES serialize, so an assertion that merely proved "the frame is small" would
// pass even if the whole Diagnostics field had been dropped by accident.
func TestScreenshotBytesNeverCrossTheAgentControlChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const bodyMarker = "acme-origin-error-page"

	pngMarker := []byte("SCREENSHOT-BYTES-MUST-NOT-APPEAR")

	frame := agents.ClientFrame{
		Type:   agents.MsgTypeResult,
		JobUID: "job-1",
		Diagnostics: &checkerdef.Diagnostics{
			FailureResponse: &checkerdef.FailureResponse{
				StatusCode: 503,
				Body:       "<html>" + bodyMarker + "</html>",
			},
			Screenshot: &checkerdef.Screenshot{
				PNG:        pngMarker,
				CapturedAt: time.Now(),
				Available:  true,
				CaptureID:  "cap-42",
			},
		},
	}

	raw, err := json.Marshal(frame)
	r.NoError(err)

	// Positive control: the frame really does carry diagnostics.
	r.Contains(string(raw), "diagnostics")
	r.Contains(string(raw), bodyMarker,
		"FailureResponse still serializes — that is what makes the absence below meaningful")

	// The marker bytes, and any base64 encoding of them, must be absent.
	r.NotContains(string(raw), string(pngMarker))
	r.NotContains(string(raw), `"png"`)
	r.NotContains(string(raw), `"PNG"`)

	// The MARKER fields the agent path does need are still there.
	r.Contains(string(raw), "cap-42")
	r.Contains(string(raw), `"available":true`)
}

// TestScreenshotConfigRoundTrip pins the opt-in flag through FromMap/GetConfig,
// including the rejection of a non-boolean.
func TestScreenshotConfigRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &BrowserConfig{}
	r.NoError(cfg.FromMap(map[string]any{cfgKeyURL: testPageURL, "screenshot": true}))
	r.True(cfg.Screenshot)
	r.Equal(true, cfg.GetConfig()["screenshot"])

	// Default is off, and an off flag is omitted from the config map entirely.
	off := &BrowserConfig{}
	r.NoError(off.FromMap(map[string]any{cfgKeyURL: testPageURL}))
	r.False(off.Screenshot)
	r.NotContains(off.GetConfig(), "screenshot")

	bad := &BrowserConfig{}
	r.Error(bad.FromMap(map[string]any{cfgKeyURL: testPageURL, "screenshot": "yes"}))
}

// TestScreenshotStaysBoundToCallerCancellation is the regression guard for a
// crash this capture path caused in CI.
//
// The capture context must remain a DESCENDANT of the context the worker hands
// in. An earlier version reached for context.WithoutCancel to survive a spent
// probe deadline, which also detached it from shutdown and — far worse — from
// the lifecycle chromedp was managing, letting the capture race the allocator's
// teardown and panic the process with "close of closed channel".
//
// Canceling the caller must still reach the capture. If this ever fails,
// something has been detached again.
//
//nolint:paralleltest // mutates the process-wide settings
func TestScreenshotStaysBoundToCallerCancellation(t *testing.T) {
	server := fakeCDPServer(t)
	withSettings(t, Settings{CDPURL: server.URL})

	r := require.New(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var captureErr error

	checker := &BrowserChecker{
		session: func(
			_ context.Context, _ *BrowserConfig, start time.Time, metrics, output map[string]any,
		) *checkerdef.Result {
			// The worker goes away mid-execution (a shutdown).
			cancel()

			return &checkerdef.Result{
				Status: checkerdef.StatusDown, Duration: time.Since(start),
				Metrics: metrics, Output: output,
			}
		},
		screenshot: func(shotCtx context.Context) ([]byte, error) {
			captureErr = shotCtx.Err()

			return fakePNG(), captureErr
		},
	}

	result, err := checker.Execute(ctx, screenshotSpec(true))
	r.NoError(err)

	r.ErrorIs(captureErr, context.Canceled,
		"the capture context must still be canceled by the caller's cancellation")
	r.Nil(result.Diagnostics, "and a canceled capture is dropped, not stored")
	r.Equal(checkerdef.StatusDown, result.Status, "the verdict is untouched either way")
}

// TestBrowserWasAllocatedGatesTheCapture pins the guard that stops the capture
// allocating a SECOND browser — the mechanism that actually panicked the
// process.
//
// chromedp's Run allocates whenever c.Browser == nil, and ExecAllocator.Allocate
// arms a goroutine that closes c.allocated when Chrome exits. A probe whose Run
// fails after that goroutine is armed but before c.Browser is set leaves a nil
// Browser and an already-closing channel; a second Run closes it twice. That
// panic is raised on a goroutine chromedp owns, so no recover() can contain it
// — it has to be prevented.
//
// Browser-free on purpose: the exact chromedp state is not provokable on
// demand, but the rule the guard encodes is assertable everywhere.
func TestBrowserWasAllocatedGatesTheCapture(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A plain context is not a chromedp context at all.
	r.False(browserWasAllocated(t.Context()))

	// A real chromedp context that has never run anything has an Allocator but
	// no Browser — precisely the state in which a capture must not call Run,
	// because Run would allocate one.
	allocCtx, allocCancel := chromedp.NewExecAllocator(
		t.Context(), chromedp.DefaultExecAllocatorOptions[:]...,
	)
	t.Cleanup(allocCancel)

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	t.Cleanup(browserCancel)

	r.NotNil(chromedp.FromContext(browserCtx), "positive control: a real chromedp context")
	r.False(browserWasAllocated(browserCtx), "no browser has been allocated yet")

	// And the production capture refuses rather than calling Run.
	png, err := fullScreenshot(browserCtx)
	r.ErrorIs(err, errNoBrowserAllocated)
	r.Nil(png)
}
