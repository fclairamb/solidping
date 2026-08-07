package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIsExportDocumentShape table-tests the predicate that routes
// `sp checks validate` between the offline whole-document path
// (ValidateDocument, no token/network) and the existing online single-check
// path. A misroute in either direction is user-visible: a document sent
// online would demand a token it shouldn't need, and a single check sent
// offline would be checked against the wrong rules entirely.
func TestIsExportDocumentShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "v2 export document (JSON)",
			raw: `{"version":2,"organization":"acme","secrets":"stripped",
				"checks":[{"slug":"google","type":"http","config":{"url":"https://google.com"}}]}`,
			want: true,
		},
		{
			name: "v2 export document (YAML)",
			raw:  "version: 2\norganization: acme\nchecks:\n  - slug: google\n    type: http\n",
			want: true,
		},
		{
			name: "document with an empty checks array",
			raw:  `{"version":2,"organization":"acme","checks":[]}`,
			want: true,
		},
		{
			name: "single check definition (JSON)",
			raw:  `{"slug":"google","type":"http","config":{"url":"https://google.com"}}`,
			want: false,
		},
		{
			name: "single check definition (YAML)",
			raw:  "slug: google\ntype: http\nconfig:\n  url: https://google.com\n",
			want: false,
		},
		{
			name: "single check whose own config has a nested 'checks' key",
			raw: `{"slug":"kubernetes","type":"kubernetes",
				"config":{"checks":["pod-ready","node-ready"]}}`,
			want: false,
		},
		{
			name: "single check with dependsOn (no top-level checks array)",
			raw: `{"slug":"api","type":"http","config":{"url":"https://api.example.com"},
				"dependsOn":[{"parentSlug":"db","kind":"hard"}]}`,
			want: false,
		},
		{
			name: "checks present but not an array",
			raw:  `{"version":2,"organization":"acme","checks":"not-a-list"}`,
			want: false,
		},
		{
			name: "malformed input (neither JSON nor YAML mapping)",
			raw:  `{not valid json or yaml: [`,
			want: false,
		},
		{
			name: "empty input",
			raw:  ``,
			want: false,
		},
		{
			name: "a bare YAML scalar",
			raw:  "just a string\n",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.Equal(tc.want, isExportDocumentShape([]byte(tc.raw)))
		})
	}
}
