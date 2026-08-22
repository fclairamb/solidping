package checkbrowser

import (
	"bytes"
	"image"
	_ "image/png" // PNG decoder: the capture must be a real, decodable image
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// chromeCandidates are the places a real Chrome/Chromium usually lives. The
// Playwright-managed one comes first because that is what CI has.
//
//nolint:gochecknoglobals // an immutable lookup table Go cannot express as const
var chromeCandidates = []string{
	"/opt/pw-browsers/chromium-1194/chrome-linux/chrome",
	"/usr/bin/chromium",
	"/usr/bin/chromium-browser",
	"/usr/bin/google-chrome",
}

// findChrome returns a usable Chrome path, or skips the test.
//
// Skipping rather than failing: these tests drive a REAL browser, and a
// machine without one is a normal developer machine, not a broken build. The
// pure-seam tests in screenshot_test.go cover the logic on every machine; what
// only a real browser can prove is the lifecycle behavior below.
func findChrome(t *testing.T) string {
	t.Helper()

	if fromEnv := os.Getenv("SP_TEST_CHROME_PATH"); fromEnv != "" {
		return fromEnv
	}

	for _, candidate := range chromeCandidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}

	t.Skip("no Chrome/Chromium binary found; set SP_TEST_CHROME_PATH to run the real-browser tests")

	return ""
}

// refusedURL returns an http:// URL nothing is listening on, so navigation
// fails the way an unreachable target does.
func refusedURL(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	return "http://" + addr
}

// TestRealChromeCaptureNeverPanics is the regression test for the crash this
// feature caused once.
//
// An earlier implementation detached the capture from chromedp's managed
// lifecycle with context.WithoutCancel. Against a real browser that raced the
// allocator's teardown and panicked — `close of closed channel` inside
// `ExecAllocator.Allocate` — which killed the whole monitoring server, not
// just the check. No seam-based test can catch that: the panic came from
// chromedp's own goroutines, so it takes a real browser to reproduce.
//
// A panic here fails the process, so these subtests passing IS the assertion.
//
//nolint:paralleltest // mutates the process-wide browser settings
func TestRealChromeCaptureNeverPanics(t *testing.T) {
	withSettings(t, Settings{ChromePath: findChrome(t)})

	cases := []struct {
		name string
		cfg  *BrowserConfig
	}{
		{"navigation refused", &BrowserConfig{
			URL: refusedURL(t), Timeout: 10 * time.Second, Screenshot: true,
		}},
		{"probe deadline expires", &BrowserConfig{
			// Short enough that the probe's own budget runs out mid-flight,
			// which is precisely when the old code reached for WithoutCancel.
			URL: refusedURL(t), Timeout: 250 * time.Millisecond, Screenshot: true,
		}},
		{"capture disabled", &BrowserConfig{
			URL: refusedURL(t), Timeout: 10 * time.Second, Screenshot: false,
		}},
	}

	for _, tc := range cases {
		//nolint:paralleltest // shares the process-wide settings installed above
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			checker := &BrowserChecker{}

			result, err := checker.Execute(t.Context(), tc.cfg)
			r.NoError(err)
			r.NotNil(result)
		})
	}
}

// TestRealChromeCapturesALoadedPage is the case the feature exists for: the
// page LOADS and then fails its keyword check, so there is a real rendering to
// photograph. End to end against real Chrome, and the bytes are DECODED rather
// than merely sniffed — a truncated blob would pass a magic-byte check.
//
//nolint:paralleltest // mutates the process-wide browser settings
func TestRealChromeCapturesALoadedPage(t *testing.T) {
	withSettings(t, Settings{ChromePath: findChrome(t)})

	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>acme is having a bad day</h1></body></html>`))
	}))
	t.Cleanup(srv.Close)

	checker := &BrowserChecker{}

	result, err := checker.Execute(t.Context(), &BrowserConfig{
		URL:        srv.URL,
		Keyword:    "definitely-not-on-this-page",
		Timeout:    15 * time.Second,
		Screenshot: true,
	})
	r.NoError(err)

	r.Equal(checkerdef.StatusDown, result.Status, "the keyword miss is still a down")
	r.NotNil(result.Diagnostics, "and the loaded page really was captured")
	r.NotNil(result.Diagnostics.Screenshot)
	r.False(result.Diagnostics.Screenshot.CapturedAt.IsZero())

	png := result.Diagnostics.Screenshot.PNG
	r.Equal([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, png[:8], "real PNG magic")
	r.LessOrEqual(len(png), checkerdef.MaxScreenshotBytes)

	decoded, _, decodeErr := image.Decode(bytes.NewReader(png))
	r.NoError(decodeErr, "the bytes must decode as an image, not merely start like one")
	r.Positive(decoded.Bounds().Dx())
	r.Positive(decoded.Bounds().Dy())
}

// TestRealChromeUnreachableTargetDegradesToNoCapture: a connection refused
// means Chrome never attached to a page, so there is genuinely nothing to
// photograph. That must degrade to "no capture", not to an error or a
// zero-byte attachment — and above all not to a different check verdict.
//
//nolint:paralleltest // mutates the process-wide browser settings
func TestRealChromeUnreachableTargetDegradesToNoCapture(t *testing.T) {
	withSettings(t, Settings{ChromePath: findChrome(t)})

	r := require.New(t)

	checker := &BrowserChecker{}

	result, err := checker.Execute(t.Context(), &BrowserConfig{
		URL: refusedURL(t), Timeout: 10 * time.Second, Screenshot: true,
	})
	r.NoError(err)

	r.Equal(checkerdef.StatusDown, result.Status, "the target is still reported down")
	r.Nil(result.Diagnostics, "and no empty capture is invented")
}
