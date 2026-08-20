package checkbrowser

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// AvailabilityTTL bounds how stale the reported `browser` capability may get.
//
// There is no timer behind it: the verdict is re-evaluated lazily when the
// heartbeat (~50s) assembles the capability set, so the TTL is a staleness
// ceiling rather than a poll interval. Fifteen minutes is cheap because the
// probe is cheap, and the two cases where fifteen minutes would be too slow are
// handled event-driven instead — see MarkAvailable / MarkUnavailable.
const AvailabilityTTL = 15 * time.Minute

// probeTimeout bounds the CDP reachability probe. It runs on the heartbeat
// path, so it must never be the reason a heartbeat is late; a healthy sidecar
// answers /json/version in single-digit milliseconds.
const probeTimeout = 2 * time.Second

// availabilityCache memoizes "can this process actually run a browser check?"
// with a TTL, an injected clock and an injected prober (tests drive both rather
// than sleeping).
type availabilityCache struct {
	ttl   time.Duration
	now   func() time.Time
	probe func(context.Context, Settings) bool

	mu     sync.Mutex
	valid  bool
	at     time.Time
	answer bool
}

//nolint:unparam // ttl is a parameter so tests can pin a short/zero TTL explicitly
func newAvailabilityCache(
	ttl time.Duration, now func() time.Time, probe func(context.Context, Settings) bool,
) *availabilityCache {
	return &availabilityCache{ttl: ttl, now: now, probe: probe}
}

// Get returns the cached verdict, re-probing when there is none or it aged out.
func (c *availabilityCache) Get(ctx context.Context, current Settings) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.valid && c.ttl > 0 && now.Sub(c.at) < c.ttl {
		return c.answer
	}

	c.answer = c.probe(ctx, current)
	c.at = now
	c.valid = true

	return c.answer
}

// MarkAvailable records a successful browser execution as a fresh positive.
// A real execution is strictly stronger evidence than the probe, and recording
// it here is what makes RECOVERY prompt: a sidecar that comes back advertises
// the capability on the next heartbeat instead of up to one TTL later.
func (c *availabilityCache) MarkAvailable() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.answer = true
	c.at = c.now()
	c.valid = true
}

// MarkUnavailable records an execution that failed on an INFRASTRUCTURE fault
// (CDP unreachable, no binary). It bypasses the TTL in the other direction: the
// capability drops on the next heartbeat rather than up to fifteen minutes
// later, which is the difference between "the sidecar died" being visible in
// one beat and being invisible for a quarter of an hour.
func (c *availabilityCache) MarkUnavailable() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.answer = false
	c.at = c.now()
	c.valid = true
}

// Reset forgets the verdict entirely, so the next Get probes. Used when the
// settings change under the cache.
func (c *availabilityCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.valid = false
	c.answer = false
}

// sharedAvailability is the process-wide verdict. One process, one browser
// backend, one answer.
//
//nolint:gochecknoglobals // deliberate process-lifetime cache, mirrors egressreport
var sharedAvailability = newAvailabilityCache(AvailabilityTTL, time.Now, probeSettings)

// Available reports whether this process can run a browser check right now.
// It is the value behind the `browser` worker capability, so it must answer
// honestly for the CONFIGURED backend: a CDP URL that nothing answers is a "no"
// even on a laptop with Chrome installed, because that is the backend the check
// would actually use.
func Available(ctx context.Context) bool {
	return sharedAvailability.Get(ctx, CurrentSettings())
}

// MarkAvailable records a successful browser execution against the shared
// verdict — the event-driven refresh described on availabilityCache.
func MarkAvailable() { sharedAvailability.MarkAvailable() }

// MarkUnavailable records an infrastructure failure against the shared verdict
// — the event-driven drop described on availabilityCache.
func MarkUnavailable() { sharedAvailability.MarkUnavailable() }

// probeSettings is the real prober: reachability for the remote path, a binary
// lookup for the exec path.
func probeSettings(ctx context.Context, current Settings) bool {
	if current.Remote() {
		return probeCDP(ctx, current.CDPURL) == nil
	}

	return FindChromeBinary(current.ChromePath) != ""
}

// probeCDP asks the CDP endpoint for its version. It is deliberately an HTTP
// GET rather than a websocket handshake: /json/version is the endpoint
// chromedp itself resolves the browser websocket through, so an endpoint that
// answers it is one chromedp can attach to, and a plain GET costs nothing.
//
// The returned error is surfaced verbatim in the check output, so an operator
// sees "connection refused" rather than a generic "browser unavailable".
func probeCDP(ctx context.Context, cdpURL string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionURL(cdpURL), nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return &cdpStatusError{status: resp.Status}
	}

	return nil
}

// versionURL turns a CDP endpoint into its /json/version URL. chromedp accepts
// both ws:// and http:// forms for the same endpoint, so both are normalized to
// the http(s) scheme the JSON endpoint is served on.
func versionURL(cdpURL string) string {
	url := cdpURL

	switch {
	case strings.HasPrefix(url, "wss://"):
		url = "https://" + strings.TrimPrefix(url, "wss://")
	case strings.HasPrefix(url, "ws://"):
		url = "http://" + strings.TrimPrefix(url, "ws://")
	}

	return strings.TrimSuffix(url, "/") + "/json/version"
}

// chromeBinaryNames mirrors the lookup chromedp's own default allocator does,
// so the probe's answer matches what the exec path would actually find. Kept as
// names (resolved through PATH) plus the few absolute locations that are not on
// PATH on a default install.
//
//nolint:gochecknoglobals // static lookup table
var chromeBinaryNames = []string{
	"headless-shell",
	"headless_shell",
	"chromium",
	"chromium-browser",
	"google-chrome",
	"google-chrome-stable",
	"google-chrome-beta",
	"google-chrome-unstable",
	"chrome",
	"chrome-browser",
	"/usr/bin/google-chrome",
	"/usr/local/bin/chrome",
	"/snap/bin/chromium",
}

// darwinChromePaths are the app-bundle executables, which never sit on PATH.
//
//nolint:gochecknoglobals // static lookup table
var darwinChromePaths = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
}

// FindChromeBinary resolves the local Chrome/Chromium the exec fallback would
// use, or "" when there is none. An explicit path wins and is NOT second-
// guessed beyond existence: an operator who names a binary gets that binary or
// a clear "not found", never a silent fallback to some other Chrome.
//
// Nothing is ever downloaded here — the absence of a binary is an answer, not a
// task (spec non-goal).
func FindChromeBinary(configured string) string {
	if configured != "" {
		return resolveBinary(configured)
	}

	candidates := chromeBinaryNames
	if runtime.GOOS == "darwin" {
		candidates = append(append([]string{}, chromeBinaryNames...), darwinChromePaths...)
	}

	for _, name := range candidates {
		if found := resolveBinary(name); found != "" {
			return found
		}
	}

	return ""
}

// resolveBinary accepts either a PATH name or an absolute/relative path.
func resolveBinary(name string) string {
	if strings.ContainsRune(name, filepath.Separator) {
		info, err := os.Stat(name)
		if err != nil || info.IsDir() {
			return ""
		}

		return name
	}

	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}

	return path
}

// cdpStatusError is the non-200 answer from /json/version, as an error value so
// the probe has a single error-shaped return (err113: no dynamic errors).
type cdpStatusError struct {
	status string
}

func (e *cdpStatusError) Error() string {
	return "CDP endpoint answered " + e.status
}
