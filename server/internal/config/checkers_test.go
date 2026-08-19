package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBrowserCheckerEnvRoundTrips is the guard the spec asks for by name: both
// keys are snake_case, so koanf's env loader maps SP_CHECKERS_BROWSER_CDP_URL
// onto checkers.browser.cdp.url and never reaches the `cdp_url` tag. Without
// applyCheckersEnv the variable parses and silently does nothing — and a test
// that merely filled CheckersConfig by hand would still pass. This one goes
// through Load(), so it fails the moment the manual reader is dropped.
func TestBrowserCheckerEnvRoundTrips(t *testing.T) {
	t.Setenv("SP_CHECKERS_BROWSER_CDP_URL", "ws://browser.internal:9222")
	t.Setenv("SP_CHECKERS_BROWSER_CHROME_PATH", "/opt/chrome/chrome")

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.Equal("ws://browser.internal:9222", cfg.Checkers.Browser.CDPURL)
	r.Equal("/opt/chrome/chrome", cfg.Checkers.Browser.ChromePath)
}

// TestBrowserCheckerEnvUnsetLeavesEmpty pins the zero-config default: no env,
// no CDP URL, no chrome path — which is what keeps the exec fallback (and
// today's dev-laptop behavior) the default.
func TestBrowserCheckerEnvUnsetLeavesEmpty(t *testing.T) {
	t.Setenv("SP_CHECKERS_BROWSER_CDP_URL", "")
	t.Setenv("SP_CHECKERS_BROWSER_CHROME_PATH", "")

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.Empty(cfg.Checkers.Browser.CDPURL)
	r.Empty(cfg.Checkers.Browser.ChromePath)
}

// TestBrowserCheckerEnvNeedsManualReader documents WHY the manual reader has to
// exist: the koanf path is unreachable from any SP_* name, so the reflection
// half of RecognizedEnvVars cannot produce these two — they are only recognized
// because manualReaderEnvVars lists them.
func TestBrowserCheckerEnvNeedsManualReader(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, reachable := envNameForKoanfPath("checkers.browser.cdp_url")
	r.False(reachable, "cdp_url must be unreachable via koanf's env loader")

	_, reachable = envNameForKoanfPath("checkers.browser.chrome_path")
	r.False(reachable, "chrome_path must be unreachable via koanf's env loader")

	set := make(map[string]struct{})
	for _, name := range RecognizedEnvVars() {
		set[name] = struct{}{}
	}

	r.Contains(set, "SP_CHECKERS_BROWSER_CDP_URL")
	r.Contains(set, "SP_CHECKERS_BROWSER_CHROME_PATH")
}
