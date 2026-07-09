package incidents

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildIncidentURL_HappyPath asserts the plain, expected-shape
// construction for well-formed inputs.
func TestBuildIncidentURL_HappyPath(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got := buildIncidentURL("my-org", "11111111-1111-1111-1111-111111111111")
	r.Equal("/dash0/orgs/my-org/incidents/11111111-1111-1111-1111-111111111111", got)
}

// TestBuildIncidentURL_PathEscapesHostileSegments proves buildIncidentURL
// neutralizes path-breaking characters via url.PathEscape — defense in depth
// even though org slugs and incident UIDs are validated elsewhere before
// this point. A '/' in either segment must not be able to insert extra path
// segments, and none of '"', '<', '>' should survive unescaped.
func TestBuildIncidentURL_PathEscapesHostileSegments(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got := buildIncidentURL(`evil"><script>/org`, `also/../etc`)

	r.NotContains(got, `"`)
	r.NotContains(got, `<`)
	r.NotContains(got, `>`)
	r.NotContains(got, "evil\"><script>/org", "the raw slug must not appear unescaped")
	// Fixed separators contribute 5 literal slashes ("/dash0/orgs/" has 3,
	// "/incidents/" has 2). Any slash embedded in a segment must have been
	// %2F-encoded rather than adding extra path segments.
	r.Equal(5, strings.Count(got, "/"), "embedded slashes must not create extra path segments")
}

// TestJSStringLiteral_EscapesScriptBreakout proves jsStringLiteral produces
// a JS string literal that can't prematurely close an inline <script> block,
// even when fed a value containing a literal "</script>".
func TestJSStringLiteral_EscapesScriptBreakout(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got := jsStringLiteral(`</script><script>alert(1)</script>`)

	r.NotContains(got, "</script>", "a literal script-closing sequence must not survive")
	r.NotContains(got, "<", "raw '<' must not survive")
	r.Contains(got, "\\u003c", "'<' must be unicode-escaped")
	r.True(strings.HasPrefix(got, `"`) && strings.HasSuffix(got, `"`), "must be a quoted JS string literal")
}

// TestSuccessRedirectFragment_EscapesPerContext feeds successRedirectFragment
// a redirect URL crafted to break out of both an HTML attribute and a JS
// string literal, and asserts neither the <meta> tag nor the inline <script>
// let it through unescaped. This is the defense-in-depth layer that sits
// after buildIncidentURL's own url.PathEscape — proven independently here in
// case that first layer is ever bypassed or the function is reused
// elsewhere.
func TestSuccessRedirectFragment_EscapesPerContext(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hostile := `/dash0/orgs/x"><script>alert(1)</script>/incidents/y&z`
	headExtra, body := successRedirectFragment(hostile)

	// Neither fragment should contain the raw payload unescaped.
	r.NotContains(headExtra, `"><script>`)
	r.NotContains(body, `"><script>`)

	// The <meta refresh> attribute value must be HTML-escaped.
	r.Contains(headExtra, "&lt;script&gt;")
	r.Contains(headExtra, "&#34;", "the embedded double-quote must be entity-escaped")

	// The visible "go now" link's href must be HTML-escaped too.
	r.Contains(body, "&lt;script&gt;")

	// The inline <script>'s JS string literal must not contain a literal
	// "</script>" — only the real closing tag of our own <script> block.
	r.Equal(1, strings.Count(body, "</script>"), "only our own script tag may close")
	r.Contains(body, "\\u003cscript\\u003e", "the JS literal must unicode-escape the embedded '<script>' text")
}

// TestPageContent_SuccessCarriesRedirect asserts the success page embeds the
// countdown script, the meta-refresh fallback, and both point at the
// expected /dash0/... URL built from orgSlug/incidentUID.
func TestPageContent_SuccessCarriesRedirect(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	title, h1, icon, body, headExtra := pageContent(ackHTMLSuccess, "acme", "abc-123")

	r.Equal("Incident acknowledged", title)
	r.Equal("Incident acknowledged", h1)
	r.Contains(icon, "icon-success")
	r.Contains(headExtra, `<meta http-equiv="refresh" content="3;url=/dash0/orgs/acme/incidents/abc-123">`)
	r.Contains(body, `href="/dash0/orgs/acme/incidents/abc-123"`)
	r.Contains(body, "ack-countdown", "must render the animated countdown element")
	r.Contains(body, "window.location.replace(url)")
	r.Contains(body, `var url="/dash0/orgs/acme/incidents/abc-123"`)
}

// TestPageContent_NonSuccessKindsHaveNoRedirect asserts the redirect
// countdown/meta-refresh is exclusive to the success page — expired,
// invalid, missing-token and error pages must never auto-redirect.
func TestPageContent_NonSuccessKindsHaveNoRedirect(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	nonSuccessKinds := []ackPageKind{ackHTMLExpired, ackHTMLInvalid, ackHTMLMissingToken, ackHTMLError}

	for _, kind := range nonSuccessKinds {
		_, _, _, body, headExtra := pageContent(kind, "acme", "abc-123")

		r.Empty(headExtra, "only the success page should set a meta-refresh")
		r.NotContains(body, "<script>", "only the success page should carry the redirect script")
		r.NotContains(body, "window.location.replace")
		r.NotContains(body, "ack-countdown")
	}
}

// TestPageContent_IconPerKind asserts each outcome renders the documented
// status icon color: green check for success, amber clock for expired, red
// cross for the remaining failure states.
func TestPageContent_IconPerKind(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cases := []struct {
		kind      ackPageKind
		wantClass string
	}{
		{ackHTMLSuccess, "icon-success"},
		{ackHTMLExpired, "icon-warn"},
		{ackHTMLInvalid, "icon-error"},
		{ackHTMLMissingToken, "icon-error"},
		{ackHTMLError, "icon-error"},
	}

	for _, tc := range cases {
		_, _, icon, _, _ := pageContent(tc.kind, "acme", "abc-123")
		r.Contains(icon, tc.wantClass)
	}
}

// TestWriteAckHTML_RendersCardWithDarkMode is an integration-style check on
// the full page: card layout, wordmark, and the prefers-color-scheme dark
// variant must all be present in the served bytes.
func TestWriteAckHTML_RendersCardWithDarkMode(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rec := httptest.NewRecorder()
	writeAckHTML(rec, http.StatusOK, ackHTMLSuccess, "acme", "abc-123")

	body := rec.Body.String()
	r.Equal("text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	r.Equal("no-store", rec.Header().Get("Cache-Control"))
	r.Contains(body, "SolidPing")
	r.Contains(body, `class="card"`)
	r.Contains(body, "prefers-color-scheme:dark")
	r.Contains(body, "icon-success")
	r.Contains(body, "/dash0/orgs/acme/incidents/abc-123")
}

// TestWriteAckHTML_ErrorKindsOmitRedirect confirms the redirect is absent at
// the writeAckHTML entry point too, not just in pageContent — guards against
// a future refactor accidentally wiring the countdown into every kind.
func TestWriteAckHTML_ErrorKindsOmitRedirect(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rec := httptest.NewRecorder()
	writeAckHTML(rec, http.StatusGone, ackHTMLExpired, "acme", "abc-123")

	body := rec.Body.String()
	r.NotContains(body, "<script>")
	r.NotContains(body, "meta http-equiv=\"refresh\"")
	r.Contains(body, "icon-warn")
}
