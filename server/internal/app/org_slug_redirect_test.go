package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSPAOrgSegmentIndex pins where the SPA routes look for the org slug, and —
// more importantly — when they do not look at all. Every asset request under
// /d and /s goes through this function, so a false positive here
// would cost two database lookups per static file.
func TestSPAOrgSegmentIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		want   int
		wantOK bool
	}{
		{name: "dash0 org page", path: "/d/orgs/acme/checks", want: 3, wantOK: true},
		{name: "dash0 org root", path: "/d/orgs/acme", want: 3, wantOK: true},
		{name: "dash0 non-org path", path: "/d/login", wantOK: false},
		{name: "dash0 root", path: "/d", wantOK: false},
		{name: "dash0 asset", path: "/d/assets/index-abc123.js", wantOK: false},
		{name: "status0 org root", path: "/s/acme", want: 2, wantOK: true},
		{name: "status0 page", path: "/s/acme/public", want: 2, wantOK: true},
		{name: "status0 asset", path: "/s/assets/main.css", wantOK: false},
		{name: "status0 favicon", path: "/s/favicon.ico", wantOK: false},
		{name: "status0 too deep", path: "/s/acme/public/extra", wantOK: false},
		{name: "status0 root", path: "/s", wantOK: false},
		{name: "slug too short to be an org", path: "/s/ab", wantOK: false},
		{name: "slug with uppercase", path: "/s/Acme", wantOK: false},
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
