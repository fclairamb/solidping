package checkbrowser

import "sync/atomic"

// Settings is the process-wide Chrome backend for the `browser` check type.
//
// It lives here rather than on BrowserChecker because the checker registry
// hands out a zero-value &BrowserChecker{} per lookup — there is no seam to
// inject per-instance configuration through. One worker process talks to one
// Chrome, so process-wide is also the honest scope.
type Settings struct {
	// CDPURL is the websocket/http address of a long-lived headless Chrome
	// speaking the Chrome DevTools Protocol. Non-empty selects the remote path.
	CDPURL string
	// ChromePath is the local binary the exec fallback runs. Only consulted
	// when CDPURL is empty; empty itself means "probe the usual names".
	ChromePath string
}

// Remote reports whether these settings select the remote CDP path.
func (s Settings) Remote() bool {
	return s.CDPURL != ""
}

// settings is the process-wide value, swapped wholesale so a reader never sees
// a half-updated pair.
//
//nolint:gochecknoglobals // process-wide checker configuration, see Settings
var settings atomic.Pointer[Settings]

// Configure installs the process-wide browser settings and drops any cached
// availability verdict, so the next capability report reflects the new backend
// instead of a probe of the old one. Called once at worker construction — the
// single constructor shared by the in-process worker and the deported agent.
func Configure(s Settings) {
	settings.Store(&s)
	sharedAvailability.Reset()
}

// CurrentSettings returns the installed settings; the zero value (exec
// fallback, no configured path) when Configure was never called. That default
// is deliberate: a process that never configures anything keeps today's
// zero-config behavior.
func CurrentSettings() Settings {
	if s := settings.Load(); s != nil {
		return *s
	}

	return Settings{}
}
