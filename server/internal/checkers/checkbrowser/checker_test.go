package checkbrowser

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// closedPortURL returns a ws:// URL nothing is listening on: a port is bound,
// its address read, then released, so a connection to it is refused rather than
// hanging on a firewall.
func closedPortURL(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	return "ws://" + addr
}

// fakeCDPServer answers /json/version like a real headless-shell would, so the
// pre-flight passes without a browser behind it.
func fakeCDPServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Browser":"HeadlessChrome/140.0.0.0",` +
			`"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/fake"}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

// withSettings installs settings for one test and restores them afterwards.
// The settings are process-wide, so tests touching them must not run in
// parallel with each other — they deliberately omit t.Parallel().
func withSettings(t *testing.T, s Settings) {
	t.Helper()

	previous := CurrentSettings()
	Configure(s)
	t.Cleanup(func() { Configure(previous) })
}

func browserSpec(timeout time.Duration) *BrowserConfig {
	return &BrowserConfig{URL: "https://example.com", Timeout: timeout}
}

// TestUnreachableCDPIsAnErrorNotADown is the load-bearing assertion of the
// spec: our own browser sidecar being down must never be reported as the
// customer's site being down. StatusError pages us; StatusDown would page them.
//
//nolint:paralleltest // mutates the process-wide settings
func TestUnreachableCDPIsAnErrorNotADown(t *testing.T) {
	withSettings(t, Settings{CDPURL: closedPortURL(t)})

	r := require.New(t)

	checker := &BrowserChecker{}
	result, err := checker.Execute(t.Context(), browserSpec(5*time.Second))

	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status,
		"an unreachable CDP endpoint is OUR outage, never the target's")
	r.NotEqual(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output["error"], "SP_CHECKERS_BROWSER_CDP_URL",
		"the message must name the fix, like the Chrome-not-found message does")
}

// TestReachableCDPYieldsNormalUpAndDown is the positive control for the test
// above: with the SAME code path — real Execute, real semaphore, real CDP
// pre-flight against a server that answers — an up target reports up and a down
// one reports down. A checker that returned StatusError for everything would
// fail here, so the assertion above is about the unreachable endpoint and not
// about a checker that can only ever error.
//
//nolint:paralleltest // mutates the process-wide settings
func TestReachableCDPYieldsNormalUpAndDown(t *testing.T) {
	server := fakeCDPServer(t)
	withSettings(t, Settings{CDPURL: server.URL})

	cases := []struct {
		name string
		want checkerdef.Status
	}{
		{"up", checkerdef.StatusUp},
		{"down", checkerdef.StatusDown},
	}

	for _, tc := range cases {
		//nolint:paralleltest // shares the process-wide settings installed above
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)

			want := tc.want
			checker := &BrowserChecker{
				session: func(
					_ context.Context, _ *BrowserConfig, start time.Time, metrics, output map[string]any,
				) *checkerdef.Result {
					return &checkerdef.Result{
						Status: want, Duration: time.Since(start), Metrics: metrics, Output: output,
					}
				},
			}

			result, err := checker.Execute(t.Context(), browserSpec(5*time.Second))
			r.NoError(err)
			r.Equal(want, result.Status)
		})
	}
}

// TestConcurrencyCapIsFour proves the semaphore both blocks past four and lets
// the fifth through once a slot frees — a cap that never released would pass a
// "never more than four" assertion on its own.
//
//nolint:paralleltest // the semaphore is process-wide
func TestConcurrencyCapIsFour(t *testing.T) {
	r := require.New(t)
	r.Equal(4, MaxConcurrentBrowsers, "the spec fixes the cap at 4")

	var (
		inFlight atomic.Int32
		peak     atomic.Int32
		wg       sync.WaitGroup
	)

	const runners = 12

	for range runners {
		wg.Add(1)

		go func() {
			defer wg.Done()

			release, ok := acquireSlot(t.Context())
			if !ok {
				return
			}

			defer release()

			now := inFlight.Add(1)
			for {
				old := peak.Load()
				if now <= old || peak.CompareAndSwap(old, now) {
					break
				}
			}

			time.Sleep(5 * time.Millisecond)
			inFlight.Add(-1)
		}()
	}

	wg.Wait()

	r.LessOrEqual(int(peak.Load()), MaxConcurrentBrowsers,
		"more than four browser executions ran at once")
	r.Equal(MaxConcurrentBrowsers, int(peak.Load()),
		"the cap must not be lower than four either — twelve runners should reach it")
	r.Zero(inFlight.Load())
	r.Empty(browserSlots, "every slot must be released")
}

// TestExecutionWaitsForASlotInsideItsTimeout: an execution that cannot get a
// slot waits on the semaphore and gives up when the check's own timeout
// expires, rather than queueing invisibly or running a fifth browser.
//
//nolint:paralleltest // the semaphore is process-wide
func TestExecutionWaitsForASlotInsideItsTimeout(t *testing.T) {
	r := require.New(t)

	held := make([]func(), 0, MaxConcurrentBrowsers)

	for range MaxConcurrentBrowsers {
		release, ok := acquireSlot(t.Context())
		r.True(ok)

		held = append(held, release)
	}

	defer func() {
		for _, release := range held {
			release()
		}
	}()

	// A CDP URL nothing answers: if the semaphore were bypassed this would come
	// back StatusError from the pre-flight, so the assertion below also proves
	// the wait happens BEFORE any browser work.
	withSettings(t, Settings{CDPURL: closedPortURL(t)})

	checker := &BrowserChecker{}
	start := time.Now()
	result, err := checker.Execute(t.Context(), browserSpec(150*time.Millisecond))

	r.NoError(err)
	r.Equal(checkerdef.StatusTimeout, result.Status)
	r.Contains(result.Output["error"], "browser slot")
	r.GreaterOrEqual(time.Since(start), 150*time.Millisecond, "it must actually have waited")
}

// TestExecutionOutcomeDrivesTheCapabilityCache pins the two event-driven
// corrections that bypass the 15-minute TTL: an infra error drops the
// capability immediately, a success restores it immediately.
//
//nolint:paralleltest // mutates the process-wide availability cache
func TestExecutionOutcomeDrivesTheCapabilityCache(t *testing.T) {
	r := require.New(t)

	previous := CurrentSettings()
	t.Cleanup(func() { Configure(previous) })

	// A settings value whose probe would say "yes" if it ever ran, so the
	// assertions below are about the recorded outcome and not about the probe.
	server := fakeCDPServer(t)
	Configure(Settings{CDPURL: server.URL})

	recordOutcome(checkerdef.StatusError)
	r.False(Available(t.Context()), "an infra error must drop the capability at once")

	recordOutcome(checkerdef.StatusUp)
	r.True(Available(t.Context()), "a successful execution must restore it at once")

	recordOutcome(checkerdef.StatusError)
	r.False(Available(t.Context()))

	recordOutcome(checkerdef.StatusDown)
	r.True(Available(t.Context()), "a DOWN target still proves a browser ran")

	recordOutcome(checkerdef.StatusTimeout)
	r.True(Available(t.Context()), "a timeout says nothing about the browser")
}

// TestExecPathReportsMissingChromeAsAnError covers the fallback half: with no
// CDP URL and a chrome_path that does not exist, the failure is still OUR
// infrastructure (StatusError), never the target's.
//
//nolint:paralleltest // mutates the process-wide settings
func TestExecPathReportsMissingChromeAsAnError(t *testing.T) {
	withSettings(t, Settings{ChromePath: "/nonexistent/definitely-not-chrome"})

	r := require.New(t)

	checker := &BrowserChecker{}
	result, err := checker.Execute(t.Context(), browserSpec(5*time.Second))

	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status)
	r.NotEqual(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output["error"], "Chrome/Chromium not found")
}
