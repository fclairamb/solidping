package checkbrowser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock is an injected clock: the TTL is exercised by moving time, never by
// sleeping for fifteen minutes.
type fakeClock struct {
	at time.Time
}

func (c *fakeClock) now() time.Time          { return c.at }
func (c *fakeClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// TestProbeCacheHonorsTheFifteenMinuteTTL: inside the TTL the answer is reused
// (the probe must not run on every heartbeat), past it the probe runs again —
// which is what makes a sidecar that appeared converge without a restart.
func TestProbeCacheHonorsTheFifteenMinuteTTL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(15*time.Minute, AvailabilityTTL, "the spec fixes the staleness ceiling at 15 minutes")

	clock := &fakeClock{at: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)}
	calls := 0
	answer := true

	cache := newAvailabilityCache(AvailabilityTTL, clock.now, func(context.Context, Settings) bool {
		calls++

		return answer
	})

	ctx := t.Context()

	r.True(cache.Get(ctx, Settings{}))
	r.Equal(1, calls)

	// Many reports inside the window: still one probe.
	clock.advance(14 * time.Minute)
	answer = false // would flip the answer if the probe ran

	r.True(cache.Get(ctx, Settings{}), "the cached verdict must be reused inside the TTL")
	r.Equal(1, calls)

	clock.advance(2 * time.Minute)
	r.False(cache.Get(ctx, Settings{}), "past the TTL the probe must run again")
	r.Equal(2, calls)
}

// TestInfraErrorInvalidatesImmediately is the first event-driven correction: a
// browser execution failing on infrastructure drops the capability on the next
// heartbeat, not up to fifteen minutes later.
func TestInfraErrorInvalidatesImmediately(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	clock := &fakeClock{at: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)}
	calls := 0

	cache := newAvailabilityCache(AvailabilityTTL, clock.now, func(context.Context, Settings) bool {
		calls++

		return true // the probe would keep saying yes
	})

	ctx := t.Context()
	r.True(cache.Get(ctx, Settings{}))

	cache.MarkUnavailable()

	clock.advance(time.Minute) // one heartbeat later, far inside the TTL
	r.False(cache.Get(ctx, Settings{}),
		"a real execution failing on infra beats a stale positive probe")
	r.Equal(1, calls, "and it must not have re-probed to find that out")
}

// TestSuccessRefreshesImmediately is the mirror image: recovery is as prompt as
// the drop, so a sidecar that comes back is advertised on the next heartbeat.
func TestSuccessRefreshesImmediately(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	clock := &fakeClock{at: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)}
	calls := 0

	cache := newAvailabilityCache(AvailabilityTTL, clock.now, func(context.Context, Settings) bool {
		calls++

		return false // the probe would keep saying no
	})

	ctx := t.Context()
	r.False(cache.Get(ctx, Settings{}))

	cache.MarkAvailable()

	clock.advance(time.Minute)
	r.True(cache.Get(ctx, Settings{}),
		"a successful execution beats a stale negative probe")
	r.Equal(1, calls)

	// And the refreshed entry still ages out normally.
	clock.advance(AvailabilityTTL + time.Second)
	r.False(cache.Get(ctx, Settings{}))
	r.Equal(2, calls)
}

// TestResetForcesAReprobe covers the settings-changed path: Configure must not
// leave a verdict about the previous backend behind.
func TestResetForcesAReprobe(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	clock := &fakeClock{at: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)}
	calls := 0

	cache := newAvailabilityCache(AvailabilityTTL, clock.now, func(context.Context, Settings) bool {
		calls++

		return true
	})

	r.True(cache.Get(t.Context(), Settings{}))
	r.Equal(1, calls)

	cache.Reset()

	r.True(cache.Get(t.Context(), Settings{}))
	r.Equal(2, calls, "a reset entry must be re-probed even inside the TTL")
}

// TestProbeSaysYesWhenTheCDPEndpointAnswers and its negative twin: the probe is
// the thing that decides the advertised capability, so both directions matter.
func TestProbeSaysYesWhenTheCDPEndpointAnswers(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/json/version" {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = w.Write([]byte(`{"Browser":"HeadlessChrome/140.0.0.0"}`))
	}))
	t.Cleanup(server.Close)

	r.True(probeSettings(t.Context(), Settings{CDPURL: server.URL}))
	r.True(probeSettings(t.Context(), Settings{CDPURL: "ws://" + server.Listener.Addr().String()}),
		"a ws:// endpoint is the same endpoint")
}

func TestProbeSaysNoWhenTheCDPEndpointIsDown(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.False(probeSettings(t.Context(), Settings{CDPURL: closedPortURL(t)}))
}

// TestConfiguredCDPWinsOverALocalBinary: a worker told to use a remote Chrome
// must answer for THAT backend. Reporting "yes" because the host happens to
// have Chrome installed would advertise a capability the check cannot use.
func TestConfiguredCDPWinsOverALocalBinary(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.False(probeSettings(t.Context(), Settings{CDPURL: closedPortURL(t)}),
		"an unreachable CDP endpoint is a no, whatever is installed locally")
}

// TestProbeFindsAConfiguredBinary covers the exec fallback's half of the probe.
func TestProbeFindsAConfiguredBinary(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := filepath.Join(t.TempDir(), "chrome")
	r.NoError(os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755))

	r.Equal(fake, FindChromeBinary(fake))
	r.True(probeSettings(t.Context(), Settings{ChromePath: fake}))

	r.Empty(FindChromeBinary(filepath.Join(t.TempDir(), "absent")),
		"a configured path that does not exist is a no, never a silent fallback")
	r.False(probeSettings(t.Context(), Settings{ChromePath: filepath.Join(t.TempDir(), "absent")}))
}

// TestVersionURLNormalizesEveryAcceptedForm pins the URL the probe hits: the
// same endpoint chromedp resolves the browser websocket through, whichever of
// the accepted forms the operator configured.
func TestVersionURLNormalizesEveryAcceptedForm(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cases := map[string]string{
		"ws://browser:9222":    "http://browser:9222/json/version",
		"ws://browser:9222/":   "http://browser:9222/json/version",
		"wss://browser:9222":   "https://browser:9222/json/version",
		"http://browser:9222":  "http://browser:9222/json/version",
		"https://browser:9222": "https://browser:9222/json/version",
	}

	for in, want := range cases {
		r.Equalf(want, versionURL(in), "versionURL(%q)", in)
	}
}
