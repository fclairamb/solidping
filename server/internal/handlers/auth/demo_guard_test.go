package auth

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIsDemoWriteAllowed pins the allowlist exactly. The denied entries are as
// important as the allowed ones: each is a route somebody might reasonably
// assume "a demo can obviously do that", and each is what would make the shared
// public credential dangerous.
func TestIsDemoWriteAllowed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		method  string
		pattern string
		want    bool
	}{
		// Safe methods pass whatever the route.
		{"GET anything", http.MethodGet, "/api/v1/orgs/{org}/tokens", true},
		{"HEAD anything", http.MethodHead, "/api/v1/orgs/{org}/integrations", true},
		{"OPTIONS preflight", http.MethodOptions, "/api/v1/orgs/{org}/status-pages", true},
		{"GET with no pattern at all", http.MethodGet, "", true},

		// The allowlist.
		{"logout", http.MethodPost, "/api/v1/auth/logout", true},
		{"create check", http.MethodPost, "/api/v1/orgs/{org}/checks", true},
		{"validate check", http.MethodPost, "/api/v1/orgs/{org}/checks/validate", true},
		{"patch check", http.MethodPatch, "/api/v1/orgs/{org}/checks/{checkUid}", true},
		{"delete check", http.MethodDelete, "/api/v1/orgs/{org}/checks/{checkUid}", true},
		{"clone check", http.MethodPost, "/api/v1/orgs/{org}/checks/{checkUid}/clone", true},

		// Denied — and each for its own reason.
		{"upsert by slug can overwrite a seeded check", http.MethodPut,
			"/api/v1/orgs/{org}/checks/{slug}", false},
		{"import rewrites the whole check set", http.MethodPost,
			"/api/v1/orgs/{org}/checks/import", false},
		{"apply can delete by absence", http.MethodPost, "/api/v1/orgs/{org}/checks/apply", false},
		{"rotating a heartbeat token breaks a seeded check", http.MethodPost,
			"/api/v1/orgs/{org}/checks/{checkUid}/rotate-token", false},
		{"minting a PAT would outlive the session", http.MethodPost,
			"/api/v1/orgs/{org}/tokens", false},
		{"changing the shared password locks everyone out", http.MethodPost,
			"/api/v1/auth/change-password", false},
		{"editing the shared profile", http.MethodPatch, "/api/v1/auth/me", false},
		{"enrolling 2FA on a shared account locks everyone out", http.MethodPost,
			"/api/v1/auth/2fa/setup", false},
		{"creating a second org escapes the demo entirely", http.MethodPost, "/api/v1/orgs", false},
		{"integrations could page a real person", http.MethodPost,
			"/api/v1/orgs/{org}/integrations", false},
		{"status pages are public artifacts", http.MethodPost,
			"/api/v1/orgs/{org}/status-pages", false},
		{"deleting the org", http.MethodDelete, "/api/v1/orgs/{org}", false},

		// Fail-closed: an unmatched route has no pattern, and a write with no
		// pattern is refused rather than waved through.
		{"POST with no route pattern fails closed", http.MethodPost, "", false},

		// The allowlist is method-specific, not path-specific.
		{"PUT on the checks collection is not POST", http.MethodPut,
			"/api/v1/orgs/{org}/checks", false},
		{"DELETE on the checks collection is not POST", http.MethodDelete,
			"/api/v1/orgs/{org}/checks", false},

		// Raw paths must never match: the guard reads chi's resolved pattern.
		{"a concrete path is not the pattern", http.MethodPost, "/api/v1/orgs/demo/checks", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsDemoWriteAllowed(tc.method, tc.pattern))
		})
	}
}

// TestDemoAllowedRoutesMatchesThePredicate keeps the exported table (which the
// route-table test in internal/app walks) and the predicate the middleware
// actually calls from drifting apart.
func TestDemoAllowedRoutesMatchesThePredicate(t *testing.T) {
	t.Parallel()

	entries := DemoAllowedRoutes()
	require.NotEmpty(t, entries)

	for _, entry := range entries {
		require.Truef(t, IsDemoWriteAllowed(entry[0], entry[1]),
			"%s %s is exported as allowed but the predicate refuses it", entry[0], entry[1])
	}
}
