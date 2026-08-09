package middleware

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCanonicalOrgURL pins the rewrite rule. The one that matters is
// "check named like the org": the org segment is located through the route
// pattern, never by searching the path for the old slug, so a check, status
// page or file whose own slug equals the org's previous slug is left alone.
func TestCanonicalOrgURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		rawURL  string
		newSlug string
		want    string
		wantOK  bool
	}{
		{
			name:    "org api path",
			pattern: "/api/v1/orgs/{org}/checks",
			rawURL:  "/api/v1/orgs/old/checks",
			newSlug: "new",
			want:    "/api/v1/orgs/new/checks",
			wantOK:  true,
		},
		{
			name:    "check named like the previous org slug is untouched",
			pattern: "/api/v1/orgs/{org}/checks/{check}/badges/{components}",
			rawURL:  "/api/v1/orgs/old/checks/old/badges/old",
			newSlug: "new",
			want:    "/api/v1/orgs/new/checks/old/badges/old",
			wantOK:  true,
		},
		{
			name:    "query string survives",
			pattern: "/api/v1/status-pages/{org}/{slug}/badge",
			rawURL:  "/api/v1/status-pages/old/public/badge?style=flat&label=up%20now",
			newSlug: "new",
			want:    "/api/v1/status-pages/new/public/badge?style=flat&label=up%20now",
			wantOK:  true,
		},
		{
			name:    "escaped path segments survive",
			pattern: "/api/v1/orgs/{org}/checks/{check}",
			rawURL:  "/api/v1/orgs/old/checks/a%2Fb",
			newSlug: "new",
			want:    "/api/v1/orgs/new/checks/a%2Fb",
			wantOK:  true,
		},
		{
			name:    "no org segment in pattern",
			pattern: "/api/v1/auth/providers",
			rawURL:  "/api/v1/auth/providers",
			newSlug: "new",
			wantOK:  false,
		},
		{
			name:    "wildcard before org is refused",
			pattern: "/status0/*",
			rawURL:  "/status0/old/public",
			newSlug: "new",
			wantOK:  false,
		},
		{
			name:    "missing pattern is refused",
			pattern: "",
			rawURL:  "/api/v1/orgs/old/checks",
			newSlug: "new",
			wantOK:  false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			parsed, err := url.Parse(testCase.rawURL)
			r.NoError(err)

			got, ok := canonicalOrgURL(testCase.pattern, parsed, testCase.newSlug)
			r.Equal(testCase.wantOK, ok)

			if testCase.wantOK {
				r.Equal(testCase.want, got)
			}
		})
	}
}

// TestRedirectStatusFor pins the method split. A 301 licenses a client to
// replay a POST as a GET, which would silently drop a heartbeat's body or a
// subscribe form — so only safe methods get 301.
func TestRedirectStatusFor(t *testing.T) {
	t.Parallel()

	tests := map[string]int{
		http.MethodGet:    http.StatusMovedPermanently,
		http.MethodHead:   http.StatusMovedPermanently,
		http.MethodPost:   http.StatusPermanentRedirect,
		http.MethodPatch:  http.StatusPermanentRedirect,
		http.MethodPut:    http.StatusPermanentRedirect,
		http.MethodDelete: http.StatusPermanentRedirect,
	}

	for method, want := range tests {
		require.Equalf(t, want, redirectStatusFor(method), "method %s", method)
	}
}
