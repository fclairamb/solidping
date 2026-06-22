package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDocsHostMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		reqHost  string
		docsHost string
		want     bool
	}{
		{name: "exact match", reqHost: "docs.solidping.io", docsHost: "docs.solidping.io", want: true},
		{name: "host with port", reqHost: "docs.solidping.io:443", docsHost: "docs.solidping.io", want: true},
		{name: "case insensitive", reqHost: "Docs.SolidPing.IO", docsHost: "docs.solidping.io", want: true},
		{name: "different host", reqHost: "www.solidping.io", docsHost: "docs.solidping.io", want: false},
		{name: "app host with port", reqHost: "app.solidping.io:4000", docsHost: "docs.solidping.io", want: false},
		{name: "empty request host", reqHost: "", docsHost: "docs.solidping.io", want: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			r.Equal(testCase.want, docsHostMatches(testCase.reqHost, testCase.docsHost))
		})
	}
}
