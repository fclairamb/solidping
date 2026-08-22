package checkbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// fakePNG is a minimal byte string starting with the real PNG magic, so a
// test asserting on captured bytes is asserting on something the production
// sniffer would also accept.
//
//nolint:gochecknoglobals // an immutable fixture Go cannot express as const
var fakePNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'f', 'a', 'k', 'e'}

// errCaptureFailed stands in for whatever chromedp returns when it cannot take
// the shot. Static so the tests satisfy the wrapped-errors lint.
var errCaptureFailed = errors.New("cdp said no")

// screenshotChecker builds a checker whose session returns `status` and whose
// capture seam returns whatever `capture` says. Both seams are needed: the
// session decides the verdict, the capture stands in for chromedp.
func screenshotChecker(
	status checkerdef.Status, capture func(context.Context) ([]byte, error),
) *BrowserChecker {
	return &BrowserChecker{
		session: func(
			_ context.Context, _ *BrowserConfig, start time.Time, metrics, output map[string]any,
		) *checkerdef.Result {
			return &checkerdef.Result{
				Status: status, Duration: time.Since(start), Metrics: metrics, Output: output,
			}
		},
		capture: capture,
	}
}

func screenshotSpec(enabled bool) *BrowserConfig {
	return &BrowserConfig{URL: "https://example.com", Timeout: 5 * time.Second, Screenshot: enabled}
}

// TestScreenshotCapturedOnlyOnFailureWhenEnabled is the core matrix: the flag
// gates the capture, and within the flag only a genuine target failure is
// photographed.
//
// StatusError is excluded on purpose — from this checker it means "we could
// not run a browser at all", so there is no page to shoot and a capture there
// would be a picture of our own outage.
//
//nolint:paralleltest // mutates the process-wide settings
func TestScreenshotCapturedOnlyOnFailureWhenEnabled(t *testing.T) {
	withSettings(t, Settings{CDPURL: fakeCDPServer(t).URL})

	cases := []struct {
		name    string
		enabled bool
		status  checkerdef.Status
		want    bool
	}{
		{"down with flag", true, checkerdef.StatusDown, true},
		{"timeout with flag", true, checkerdef.StatusTimeout, true},
		{"up with flag", true, checkerdef.StatusUp, false},
		{"infra error with flag", true, checkerdef.StatusError, false},
		{"down without flag", false, checkerdef.StatusDown, false},
	}

	for _, tc := range cases {
		//nolint:paralleltest // shares the process-wide settings installed above
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			called := false
			checker := screenshotChecker(tc.status, func(context.Context) ([]byte, error) {
				called = true

				return fakePNG, nil
			})

			result, err := checker.Execute(t.Context(), screenshotSpec(tc.enabled))
			r.NoError(err)

			// The verdict is never touched by the capture decision.
			r.Equal(tc.status, result.Status)

			if !tc.want {
				r.False(called, "the capture must not even be attempted")
				r.Nil(result.Diagnostics)

				return
			}

			r.True(called)
			r.NotNil(result.Diagnostics)
			r.NotNil(result.Diagnostics.Screenshot)
			r.Equal(fakePNG, result.Diagnostics.Screenshot.PNG)
			r.False(result.Diagnostics.Screenshot.CapturedAt.IsZero())
		})
	}
}

// TestScreenshotFailureNeverChangesTheVerdict pins the rule the capture is
// built around: nothing it can do may alter what the check reported.
//
//nolint:paralleltest // mutates the process-wide settings
func TestScreenshotFailureNeverChangesTheVerdict(t *testing.T) {
	withSettings(t, Settings{CDPURL: fakeCDPServer(t).URL})

	cases := []struct {
		name    string
		capture func(context.Context) ([]byte, error)
	}{
		{"capture errors", func(context.Context) ([]byte, error) {
			return nil, errCaptureFailed
		}},
		{"capture returns nothing", func(context.Context) ([]byte, error) {
			return nil, nil
		}},
		{"capture is over the cap", func(context.Context) ([]byte, error) {
			// Dropped, not truncated: half a PNG is a corrupt file.
			return make([]byte, checkerdef.MaxScreenshotBytes+1), nil
		}},
	}

	for _, tc := range cases {
		//nolint:paralleltest // shares the process-wide settings installed above
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			checker := screenshotChecker(checkerdef.StatusDown, tc.capture)

			result, err := checker.Execute(t.Context(), screenshotSpec(true))
			r.NoError(err)

			r.Equal(checkerdef.StatusDown, result.Status, "the verdict survives a failed capture")
			r.Nil(result.Diagnostics, "no capture is better than a broken one")
		})
	}
}

// TestScreenshotCapturesEvenOnAnExpiredContext proves the WithoutCancel in
// captureScreenshot is load-bearing: a timed-out check arrives with a context
// that is already Done, and a capture that simply inherited it could never run.
//
//nolint:paralleltest // mutates the process-wide settings
func TestScreenshotCapturesEvenOnAnExpiredContext(t *testing.T) {
	withSettings(t, Settings{CDPURL: fakeCDPServer(t).URL})

	r := require.New(t)

	var sawDeadline bool

	checker := &BrowserChecker{
		session: func(
			ctx context.Context, _ *BrowserConfig, start time.Time, metrics, output map[string]any,
		) *checkerdef.Result {
			// Simulate the timeout path: the check's own context is spent by
			// the time the verdict is in.
			expired, cancel := context.WithCancel(ctx)
			cancel()

			<-expired.Done()

			return &checkerdef.Result{
				Status: checkerdef.StatusTimeout, Duration: time.Since(start),
				Metrics: metrics, Output: output,
			}
		},
		capture: func(ctx context.Context) ([]byte, error) {
			// Positive control: the capture context is alive AND bounded.
			if err := ctx.Err(); err != nil {
				return nil, err
			}

			_, sawDeadline = ctx.Deadline()

			return fakePNG, nil
		},
	}

	result, err := checker.Execute(t.Context(), screenshotSpec(true))
	r.NoError(err)
	r.Equal(checkerdef.StatusTimeout, result.Status)
	r.NotNil(result.Diagnostics)
	r.NotNil(result.Diagnostics.Screenshot)
	r.True(sawDeadline, "the capture must be time-boxed by its own budget")
}

// TestScreenshotBytesNeverReachTheAgentFrame is the security assertion of §3:
// the PNG must not be serializable onto the agent's JSON control channel,
// which is exactly what the dedicated upload endpoint exists to avoid.
//
// The FailureResponse capture is the POSITIVE CONTROL — it still rides that
// frame — so this cannot pass by the whole Diagnostics block having quietly
// stopped being serialized.
func TestScreenshotBytesNeverReachTheAgentFrame(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const bodyMarker = "acme-failure-body-marker"

	frame := agentcrypto.ClientFrame{
		Type: "result",
		Diagnostics: &checkerdef.Diagnostics{
			FailureResponse: &checkerdef.FailureResponse{
				StatusCode: 503,
				Body:       bodyMarker,
			},
			Screenshot: &checkerdef.Screenshot{
				PNG:        fakePNG,
				CapturedAt: time.Now(),
			},
		},
	}

	raw, err := json.Marshal(frame)
	r.NoError(err)

	// Positive control: diagnostics really are on this frame.
	r.Contains(string(raw), "failureResponse")
	r.Contains(string(raw), bodyMarker)

	r.NotContains(string(raw), "screenshot", "the Screenshot field must not be serialized at all")
	// "PNG" is the Go field name a `json:"-"` removal would restore; base64 of
	// the fixture's leading bytes is what its value would look like on the wire.
	r.NotContains(string(raw), `"PNG"`)
	r.NotContains(string(raw), "iVBORw", "no base64 PNG anywhere on the wire")
}
