package checkbrowser

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestScreenshotRequestIsViewportOnly pins spec 2026-09-27-01 §1 on the exact
// CDP request Session.Screenshot sends: no captureBeyondViewport, so Chrome
// never rasterizes the whole document height again. Format, quality and
// fromSurface are asserted too, so the test cannot pass on a request that
// lost its other options along with the full-page one.
func TestScreenshotRequestIsViewportOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	params := screenshotParams()
	r.False(params.CaptureBeyondViewport, "captures must be viewport-only")
	r.Nil(params.Clip, "no clip: the capture is the viewport as laid out")
	r.True(params.FromSurface)
	r.Equal(page.CaptureScreenshotFormatWebp, params.Format)
	r.Equal(int64(screenshotQuality), params.Quality)

	// What actually goes on the wire. cdproto serializes the flag even when
	// false, which makes the positive control free: the full-page request
	// this spec removed serializes it as true.
	raw, err := json.Marshal(params)
	r.NoError(err)
	r.Contains(string(raw), `"captureBeyondViewport":false`)
	r.Contains(string(raw), `"format":"webp"`)

	fullPage, err := json.Marshal(screenshotParams().WithCaptureBeyondViewport(true))
	r.NoError(err)
	r.Contains(string(fullPage), `"captureBeyondViewport":true`, "control: the assertion can tell the two apart")
}

// TestSessionViewportParams pins the device-metrics override every session
// opens with (spec 2026-09-27-01 §2): a 1280x800 desktop viewport, one image
// pixel per CSS pixel.
func TestSessionViewportParams(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	params := viewportParams()
	r.Equal(int64(1280), params.Width)
	r.Equal(int64(800), params.Height)
	r.Equal(int64(ViewportWidth), params.Width)
	r.Equal(int64(ViewportHeight), params.Height)
	r.InDelta(1.0, params.DeviceScaleFactor, 0)
	r.False(params.Mobile)
}

// TestScreenshotErrorIsRecordedOnlyForAttemptedCaptures pins
// Diagnostics.ScreenshotError (spec 2026-09-27-01 §3): every way an attempted
// capture can produce nothing names its reason, a successful capture carries
// none, and a run that never attempts one leaves the field empty.
//
//nolint:paralleltest // mutates the process-wide settings
func TestScreenshotErrorIsRecordedOnlyForAttemptedCaptures(t *testing.T) {
	server := fakeCDPServer(t)
	withSettings(t, Settings{CDPURL: server.URL})

	cases := []struct {
		name    string
		forced  bool
		enabled bool
		status  checkerdef.Status
		capture func(ctx context.Context) (Capture, error)
		want    string // "" = no ScreenshotError
		shot    bool
	}{
		{
			name: "capture errors", enabled: true, status: checkerdef.StatusDown,
			capture: func(context.Context) (Capture, error) { return Capture{}, errRendererGone },
			want:    errRendererGone.Error(),
		},
		{
			name: "capture stalls past its budget", forced: true, status: checkerdef.StatusUp,
			capture: func(context.Context) (Capture, error) { return Capture{}, context.DeadlineExceeded },
			want:    fmt.Sprintf("the capture timed out after %s", screenshotTimeout),
		},
		{
			name: "capture is empty", forced: true, status: checkerdef.StatusUp,
			capture: func(context.Context) (Capture, error) { return Capture{}, nil },
			want:    "the capture returned an empty image",
		},
		{
			name: "capture is over the cap", enabled: true, status: checkerdef.StatusTimeout,
			capture: func(context.Context) (Capture, error) {
				return Capture{Image: make([]byte, MaxScreenshotBytes+1), Format: ScreenshotFormat}, nil
			},
			want: OverCapMessage(MaxScreenshotBytes + 1),
		},
		{
			name: "capture succeeds (control)", forced: true, status: checkerdef.StatusUp,
			capture: func(context.Context) (Capture, error) { return fakeCapture(), nil },
			shot:    true,
		},
		{
			name: "no capture attempted: up and not forced", enabled: true, status: checkerdef.StatusUp,
			capture: func(context.Context) (Capture, error) { return Capture{}, errRendererGone },
		},
		{
			name: "no capture attempted: not opted in", status: checkerdef.StatusDown,
			capture: func(context.Context) (Capture, error) { return Capture{}, errRendererGone },
		},
		{
			name: "no capture attempted: forced but no browser", forced: true, status: checkerdef.StatusError,
			capture: func(context.Context) (Capture, error) { return Capture{}, errRendererGone },
		},
	}

	for _, tc := range cases {
		//nolint:paralleltest // shares the process-wide settings installed above
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			ctx := t.Context()
			if tc.forced {
				ctx = checkerdef.WithForcedCapture(ctx)
			}

			result, err := screenshotChecker(tc.status, tc.capture).Execute(ctx, screenshotSpec(tc.enabled))
			r.NoError(err)
			r.Equal(tc.status, result.Status, "the capture outcome never changes the verdict")

			if tc.want == "" && !tc.shot {
				if result.Diagnostics != nil {
					r.Empty(result.Diagnostics.ScreenshotError, "no attempt, no error")
					r.Nil(result.Diagnostics.Screenshot)
				}

				return
			}

			r.NotNil(result.Diagnostics)
			r.Equal(tc.want, result.Diagnostics.ScreenshotError)
			r.Equal(tc.shot, result.Diagnostics.Screenshot != nil)
		})
	}
}

// TestScreenshotErrorCrossesTheAgentControlChannel: unlike the image bytes,
// the reason is serialized, so a deported agent's result frame carries it.
func TestScreenshotErrorCrossesTheAgentControlChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	raw, err := json.Marshal(&checkerdef.Diagnostics{ScreenshotError: "the capture timed out after 5s"})
	r.NoError(err)
	r.JSONEq(`{"screenshotError":"the capture timed out after 5s"}`, string(raw))

	var back checkerdef.Diagnostics
	r.NoError(json.Unmarshal(raw, &back))
	r.Equal("the capture timed out after 5s", back.ScreenshotError)

	empty, err := json.Marshal(&checkerdef.Diagnostics{})
	r.NoError(err)
	r.JSONEq(`{}`, string(empty), "omitted when empty, so older servers see nothing new")
}

// webpSize reads the pixel dimensions out of a WebP header (VP8, VP8L or
// VP8X), enough for a test to assert on the size of a real capture without an
// image-decoding dependency.
func webpSize(t *testing.T, img []byte) (int, int) {
	t.Helper()

	require.GreaterOrEqual(t, len(img), 30, "too short to be a WebP")
	require.Equal(t, "RIFF", string(img[:4]))
	require.Equal(t, "WEBP", string(img[8:12]))

	data := img[20:]

	switch string(img[12:16]) {
	case "VP8 ":
		require.Equal(t, []byte{0x9d, 0x01, 0x2a}, data[3:6], "VP8 start code")

		return int(binary.LittleEndian.Uint16(data[6:8]) & 0x3fff), int(binary.LittleEndian.Uint16(data[8:10]) & 0x3fff)
	case "VP8L":
		bits := binary.LittleEndian.Uint32(data[1:5])

		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1
	case "VP8X":
		le24 := func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }

		return le24(data[4:7]) + 1, le24(data[7:10]) + 1
	default:
		t.Fatalf("unknown WebP chunk %q", img[12:16])

		return 0, 0
	}
}

// TestSessionViewportAndCaptureOnARealTallPage is the live half of spec
// 2026-09-27-01 §1-§2, against a real Chrome:
//
//   - the page's own inline script, which runs while the document is being
//     PARSED, already sees a 1280x800 window: the viewport is in place before
//     the first navigation, not applied after load;
//   - the capture of a page 3000 px tall is exactly 1280x800 — the positive
//     control that the capture is viewport-only, since a full-page capture of
//     the same page would be 3000 px high.
//
//nolint:paralleltest // mutates the process-wide settings
func TestSessionViewportAndCaptureOnARealTallPage(t *testing.T) {
	settings, ok := liveBrowserSettings()
	if !ok {
		t.Skip(liveBrowserSkipReason)
	}

	r := require.New(t)

	withSettings(t, settings)

	fixture := liveFixtureServer(t)
	base := browserReachableURL(t, fixture.URL)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	session, err := Open(ctx)
	r.NoError(err)

	defer session.Close()

	nav, err := session.Navigate(ctx, base+"/tall")
	r.NoError(err)
	r.Equal("1280x800", nav.Title, "the page laid out at the session viewport from its first script")

	docHeight, err := session.Evaluate(ctx, "document.documentElement.scrollHeight")
	r.NoError(err)
	r.InDelta(3000, docHeight, 50, "control: the page really is taller than the viewport")

	shot, err := session.Screenshot(ctx)
	r.NoError(err)
	r.Equal(checkerdef.ImageFormatWebP, shot.Format)

	width, height := webpSize(t, shot.Image)
	r.Equal(ViewportWidth, width)
	r.Equal(ViewportHeight, height, "viewport-only: a full-page capture would be ~3000 px high")
}
