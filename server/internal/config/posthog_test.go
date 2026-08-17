package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPostHogHostResolution pins the split between the three hosts: the upstream
// the server-side client and proxy talk to (ResolvedHost), the api_host the
// dashboard posts to (BrowserAPIHost), and the posthog-js ui_host override
// (BrowserUIHost). With no configured host the dashboard must post to the
// first-party proxy path while the upstream stays the absolute cloud endpoint.
func TestPostHogHostResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// No host configured: capture is first-party, ui_host is set explicitly,
	// and the upstream stays the absolute cloud endpoint.
	empty := PostHogConfig{}
	r.Equal(DefaultPostHogHost, empty.ResolvedHost())
	r.Equal(PostHogProxyPath, empty.BrowserAPIHost())
	r.Equal(DefaultPostHogUIHost, empty.BrowserUIHost())

	// Explicit host: every path uses it verbatim and no ui_host override is
	// needed — posthog-js derives it from api_host as before.
	custom := PostHogConfig{Host: "https://ph.example.com"}
	r.Equal("https://ph.example.com", custom.ResolvedHost())
	r.Equal("https://ph.example.com", custom.BrowserAPIHost())
	r.Empty(custom.BrowserUIHost())
}
