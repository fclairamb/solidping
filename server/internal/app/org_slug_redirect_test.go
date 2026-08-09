package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSPAOrgSegmentIndex pins where the SPA routes look for the org slug, and —
// more importantly — when they do not look at all. Every asset request under
// /dash0 and /status0 goes through this function, so a false positive here
// would cost two database lookups per static file.
func TestSPAOrgSegmentIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		want   int
		wantOK bool
	}{
		{name: "dash0 org page", path: "/dash0/orgs/acme/checks", want: 3, wantOK: true},
		{name: "dash0 org root", path: "/dash0/orgs/acme", want: 3, wantOK: true},
		{name: "dash0 non-org path", path: "/dash0/login", wantOK: false},
		{name: "dash0 root", path: "/dash0", wantOK: false},
		{name: "dash0 asset", path: "/dash0/assets/index-abc123.js", wantOK: false},
		{name: "status0 org root", path: "/status0/acme", want: 2, wantOK: true},
		{name: "status0 page", path: "/status0/acme/public", want: 2, wantOK: true},
		{name: "status0 asset", path: "/status0/assets/main.css", wantOK: false},
		{name: "status0 favicon", path: "/status0/favicon.ico", wantOK: false},
		{name: "status0 too deep", path: "/status0/acme/public/extra", wantOK: false},
		{name: "status0 root", path: "/status0", wantOK: false},
		{name: "slug too short to be an org", path: "/status0/ab", wantOK: false},
		{name: "slug with uppercase", path: "/status0/Acme", wantOK: false},
		{name: "unrelated path", path: "/docs/guide", wantOK: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			index, ok := spaOrgSegmentIndex(testCase.path)
			r.Equal(testCase.wantOK, ok)

			if testCase.wantOK {
				r.Equal(testCase.want, index)
			}
		})
	}
}
