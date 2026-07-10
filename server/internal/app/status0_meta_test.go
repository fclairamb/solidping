package app

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatusPagePathParts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		reqPath  string
		wantOrg  string
		wantSlug string
		wantOK   bool
	}{
		{name: "root", reqPath: "/", wantOK: false},
		{name: "empty", reqPath: "", wantOK: false},
		{name: "org only", reqPath: "/acme", wantOrg: "acme", wantSlug: "", wantOK: true},
		{name: "org trailing slash", reqPath: "/acme/", wantOrg: "acme", wantSlug: "", wantOK: true},
		{name: "org and slug", reqPath: "/acme/api", wantOrg: "acme", wantSlug: "api", wantOK: true},
		{name: "org and slug trailing slash", reqPath: "/acme/api/", wantOrg: "acme", wantSlug: "api", wantOK: true},
		{name: "three segments", reqPath: "/acme/api/extra", wantOK: false},
		{name: "empty middle segment", reqPath: "/acme//api", wantOK: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			org, slug, ok := statusPagePathParts(testCase.reqPath)
			r.Equal(testCase.wantOK, ok)

			if testCase.wantOK {
				r.Equal(testCase.wantOrg, org)
				r.Equal(testCase.wantSlug, slug)
			}
		})
	}
}

func TestRequestScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		forwardedProt string
		tls           bool
		want          string
	}{
		{name: "forwarded https", forwardedProt: "https", want: "https"},
		{name: "forwarded http", forwardedProt: "http", want: "http"},
		{name: "forwarded list takes first", forwardedProt: "https, http", want: "https"},
		{name: "forwarded blank falls through to tls", forwardedProt: "", tls: true, want: "https"},
		{name: "no forwarded no tls defaults http", want: "http"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			req, err := http.NewRequestWithContext(
				context.Background(), http.MethodGet, "http://example.com/status0/acme", nil)
			r.NoError(err)

			if testCase.forwardedProt != "" {
				req.Header.Set("X-Forwarded-Proto", testCase.forwardedProt)
			}

			if testCase.tls {
				req.TLS = &tls.ConnectionState{}
			}

			r.Equal(testCase.want, requestScheme(req))
		})
	}
}

func TestRequestOrigin(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, "http://status.example.com/status0/acme", nil)
	r.NoError(err)
	req.Header.Set("X-Forwarded-Proto", "https")

	r.Equal("https://status.example.com", requestOrigin(req))
}

func TestBuildStatus0MetaTags(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	block := buildStatus0MetaTags(ogMetadata{
		Title:       "Acme API — Status",
		Description: "Our public API status",
		URL:         "https://status.example.com/status0/acme/api",
		Image:       "https://status.example.com/status0/og-default.png",
	})

	r.Contains(block, "<title>Acme API — Status</title>")
	r.Contains(block, `<meta property="og:title" content="Acme API — Status" />`)
	r.Contains(block, `<meta property="og:description" content="Our public API status" />`)
	r.Contains(block, `<meta property="og:type" content="website" />`)
	r.Contains(block, `<meta property="og:site_name" content="SolidPing" />`)
	r.Contains(block, `<meta property="og:url" content="https://status.example.com/status0/acme/api" />`)
	r.Contains(block, `<meta property="og:image" content="https://status.example.com/status0/og-default.png" />`)
	r.Contains(block, `<meta name="twitter:card" content="summary_large_image" />`)
	r.Contains(block, `<meta name="twitter:image" content="https://status.example.com/status0/og-default.png" />`)
	r.Contains(block, `<meta name="description" content="Our public API status" />`)
}

func TestBuildStatus0MetaTagsEscaping(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	block := buildStatus0MetaTags(ogMetadata{
		Title:       `A & B <script> "x"` + ogTitleSuffix,
		Description: `desc & "quoted" <b>`,
		URL:         "https://status.example.com/status0/a%26b",
		Image:       "https://status.example.com/status0/og-default.png",
	})

	// No raw special characters leak into the markup.
	r.NotContains(block, "<script>")
	r.NotContains(block, `"x"</title>`)
	r.Contains(block, "&amp;")
	r.Contains(block, "&lt;script&gt;")
	r.Contains(block, "&#34;")
}

func TestInjectStatus0Meta(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const doc = `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>SolidPing - Status Page</title>
    <meta name="google" content="notranslate" />
  </head>
  <body><div id="root"></div></body>
</html>`

	out := injectStatus0Meta(doc, ogMetadata{
		Title:       "Acme API — Status",
		Description: "Our public API status",
		URL:         "https://status.example.com/status0/acme/api",
		Image:       "https://status.example.com/status0/og-default.png",
	})

	// The static default title is gone, the per-page one is present.
	r.NotContains(out, "SolidPing - Status Page")
	r.Contains(out, "<title>Acme API — Status</title>")
	// The injected block sits before </head>.
	r.Contains(out, `og:title`)
	r.Less(strings.Index(out, "og:title"), strings.Index(out, "</head>"))
	// Untouched markup survives.
	r.Contains(out, `<meta name="google" content="notranslate" />`)
	r.Contains(out, `<div id="root"></div>`)
}

func TestInjectStatus0MetaNoHead(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const doc = `<title>x</title><p>no head here</p>`

	out := injectStatus0Meta(doc, ogMetadata{Title: "T", Description: "D"})
	// Without a </head> the document is returned with the title stripped and
	// no partial block spliced in.
	r.NotContains(out, "og:title")
}
