package securityheaders

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// directiveOf returns the sources of one directive in a rendered policy, and
// whether the directive is present at all.
func directiveOf(csp, name string) ([]string, bool) {
	for _, part := range strings.Split(csp, ";") {
		fields := strings.Fields(part)
		if len(fields) > 0 && fields[0] == name {
			return fields[1:], true
		}
	}

	return nil, false
}

func newBuilder(t *testing.T, opts Options) *Builder {
	t.Helper()

	builder, errs := New(opts)
	require.Empty(t, errs)

	return builder
}

func TestStatusPagePolicyIsSelfOnlyForFetches(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	headers := newBuilder(t, Options{}).StatusPage(Params{ScriptHashes: []string{"'sha256-abc='"}})

	r.Equal(
		"default-src 'self'; script-src 'self' 'sha256-abc='; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; "+
			"base-uri 'self'; form-action 'self'; frame-ancestors 'self'",
		headers.CSP,
	)
	r.Equal(FrameOptionsSameOrigin, headers.FrameOptions)
	r.Equal(ReferrerNoReferrer, headers.ReferrerPolicy)

	// The whole point: a custom stylesheet's url() to a third party is an
	// img-src / font-src fetch, and neither directive allows any host but
	// our own.
	for _, name := range []string{"img-src", "font-src", "connect-src", "default-src"} {
		sources, ok := directiveOf(headers.CSP, name)
		r.True(ok, name)

		for _, src := range sources {
			r.NotContains(src, "https:", "%s must not allow https: (%v)", name, sources)
			r.NotContains(src, "evil.example", name)
			r.NotEqual("*", src, name)
		}
	}
}

func TestStatusPageEmbedOriginsWidenFrameAncestorsAndDropXFO(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	headers := newBuilder(t, Options{}).StatusPage(Params{
		EmbedOrigins: []string{"https://intranet.acme.com", "https://*.acme.com"},
	})

	sources, ok := directiveOf(headers.CSP, "frame-ancestors")
	r.True(ok)
	r.Equal([]string{"'self'", "https://intranet.acme.com", "https://*.acme.com"}, sources)
	r.Empty(headers.FrameOptions,
		"X-Frame-Options cannot express an allowlist; SAMEORIGIN would contradict frame-ancestors")
}

func TestStatusPageRejectsInjectedEmbedOrigins(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	headers := newBuilder(t, Options{}).StatusPage(Params{
		EmbedOrigins: []string{"https://a.acme.com; script-src *", "*", "https://acme.com/path"},
	})

	sources, _ := directiveOf(headers.CSP, "frame-ancestors")
	r.Equal([]string{"'self'"}, sources)
	r.NotContains(headers.CSP, "script-src *")
	r.Equal(FrameOptionsSameOrigin, headers.FrameOptions)
}

func TestDashboardPolicy(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	builder := newBuilder(t, Options{DashboardThirdParty: []string{"https://eu.posthog.com"}})
	headers := builder.Dashboard(Params{
		ScriptHashes: []string{"'sha256-abc='"},
		SelfOrigins:  []string{"https://solidping.acme.com", "wss://solidping.acme.com"},
	})

	script, _ := directiveOf(headers.CSP, "script-src")
	r.Equal([]string{"'self'", "'sha256-abc='", "https://solidping.acme.com", "https://eu.posthog.com"}, script,
		"the ws(s) origin belongs to connect-src only")

	connect, _ := directiveOf(headers.CSP, "connect-src")
	r.Contains(connect, "wss://solidping.acme.com", "the realtime socket must be allowed")
	r.Contains(connect, "https://eu.posthog.com")

	img, _ := directiveOf(headers.CSP, "img-src")
	r.Contains(img, "https:", "external org logos and provider avatars must keep rendering")

	frame, _ := directiveOf(headers.CSP, "frame-ancestors")
	r.Equal([]string{"'self'"}, frame)
	r.Equal(FrameOptionsSameOrigin, headers.FrameOptions)
	r.Empty(headers.ReferrerPolicy)
}

func TestBaselinePolicy(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	headers := newBuilder(t, Options{}).Baseline()
	r.Equal("frame-ancestors 'self'; base-uri 'self'; object-src 'none'", headers.CSP)
	r.Equal(FrameOptionsSameOrigin, headers.FrameOptions)

	_, hasScript := directiveOf(headers.CSP, "script-src")
	r.False(hasScript, "the baseline must not restrict fetches (the /openapi explorer loads from a CDN)")
}

func TestExtraSourcesWidenOnlyDirectivesASurfaceSets(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	builder := newBuilder(t, Options{
		ExtraSources: "img-src https://cdn.acme.com; connect-src https://sentry.acme.com",
	})

	status := builder.StatusPage(Params{})
	img, _ := directiveOf(status.CSP, "img-src")
	r.Equal([]string{"'self'", "data:", "https://cdn.acme.com"}, img)

	connect, _ := directiveOf(status.CSP, "connect-src")
	r.Contains(connect, "https://sentry.acme.com")

	// The baseline sets no img-src; adding one there would RESTRICT images.
	baseline := builder.Baseline()
	_, hasImg := directiveOf(baseline.CSP, "img-src")
	r.False(hasImg)
}

func TestExtraFrameAncestorsDropsXFOEverywhere(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	builder := newBuilder(t, Options{ExtraSources: "frame-ancestors https://intranet.acme.com"})

	for _, headers := range []Headers{builder.Baseline(), builder.Dashboard(Params{}), builder.StatusPage(Params{})} {
		frame, _ := directiveOf(headers.CSP, "frame-ancestors")
		r.Equal([]string{"'self'", "https://intranet.acme.com"}, frame)
		r.Empty(headers.FrameOptions)
	}
}

func TestParseExtraSourcesRejectsInjection(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	extras, errs := parseExtraSources(
		"img-src https://ok.acme.com 'nonce-abc' https://bad,acme.com; sandbox allow-scripts; report-uri /x; img-src",
	)

	r.Len(extras, 1)
	r.Equal("img-src", extras[0].name)
	r.Equal([]string{"https://ok.acme.com"}, extras[0].sources)
	r.Len(errs, 5) // 'nonce-…', the comma host, sandbox, report-uri, empty img-src

	for _, err := range errs {
		r.ErrorIs(err, ErrInvalidExtraSource)
	}
}

func TestParseExtraSourcesAcceptsKeywordsSchemesAndHashes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	extras, errs := parseExtraSources(
		"SCRIPT-SRC 'unsafe-eval' 'sha256-AbC+/=' https:  ;  font-src *.acme.com:443 https://fonts.acme.com/css/",
	)

	r.Empty(errs)
	r.Equal([]directive{
		{name: "script-src", sources: []string{"'unsafe-eval'", "'sha256-AbC+/='", "https:"}},
		{name: "font-src", sources: []string{"*.acme.com:443", "https://fonts.acme.com/css/"}},
	}, extras)
}

func TestInlineScriptHashes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	html := []byte(`<html><head>
<script type="module" crossorigin src="/s/assets/index.js"></script>
<script>
  (function () { document.documentElement.classList.add("dark"); })();
</script>
<SCRIPT src='/x.js'></SCRIPT>
<script></script>
</head></html>`)

	hashes := InlineScriptHashes(html)
	r.Len(hashes, 1, "only the inline, non-empty script is hashed")
	// sha256 of the exact body between the tags, newlines included (checked
	// against `openssl dgst -sha256 -binary | base64`).
	r.Equal("'sha256-E/a413ROgvT6bnKUgKKu+hlDu8f4Xqao4GV78KLaL6s='", hashes[0])
}

func TestRequestOrigins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		host  string
		proto string
		tls   bool
		want  []string
	}{
		{name: "plain http", host: "localhost:4000", want: []string{"http://localhost:4000", "ws://localhost:4000"}},
		{
			name: "forwarded https", host: "Solidping.Acme.com", proto: "https, http",
			want: []string{"https://solidping.acme.com", "wss://solidping.acme.com"},
		},
		{name: "tls", host: "acme.com", tls: true, want: []string{"https://acme.com", "wss://acme.com"}},
		{
			name: "junk proto ignored", host: "acme.com", proto: "javascript",
			want: []string{"http://acme.com", "ws://acme.com"},
		},
		{name: "semicolon host not reflected", host: "acme.com;script-src", want: nil},
		{name: "ipv6 not reflected", host: "[::1]:4000", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/d/", nil)
			req.Host = tt.host

			if tt.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.proto)
			}

			if tt.tls {
				req.TLS = &tls.ConnectionState{}
			}

			require.Equal(t, tt.want, RequestOrigins(req))
		})
	}
}

func TestHeadersApplyRemovesStaleFrameOptions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	header := http.Header{}
	header.Set(HeaderFrameOptions, "DENY")

	Headers{CSP: "frame-ancestors 'self' https://a.acme.com"}.Apply(header)

	r.Empty(header.Get(HeaderFrameOptions))
	r.Equal("frame-ancestors 'self' https://a.acme.com", header.Get(HeaderCSP))
	r.Empty(header.Get(HeaderReferrerPolicy))
}
