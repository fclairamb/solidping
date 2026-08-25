package email

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const brandingTestBaseURL = "https://solidping.example"

// brandingViewModel is the minimal incident view model the branding tests
// render — incident-created.html is used because it exercises the full
// wrapper (header, status banner, details table, footer).
func brandingViewModel() map[string]any {
	return map[string]any{
		"CheckName":      "Production API",
		"CheckType":      "http",
		"IncidentNumber": 42,
		"IncidentUID":    "inc-123",
		"StartedAt":      "2026-07-05 10:00:00",
	}
}

func renderBranded(t *testing.T, baseURL string, data any) string {
	t.Helper()

	formatter, err := NewFormatter(WithBaseURL(baseURL))
	require.NoError(t, err)

	_, html, _, err := formatter.Format("incident-created.html", data)
	require.NoError(t, err)

	return html
}

// TestBranding_ProductLogoIsAbsoluteWithTextFallback covers the resolved
// decision: a REMOTE <img> (never a CID attachment) whose src is absolute, and
// whose alt is the wordmark so a client blocking remote images still shows a
// legible header.
func TestBranding_ProductLogoIsAbsoluteWithTextFallback(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	html := renderBranded(t, brandingTestBaseURL, brandingViewModel())

	r.Contains(html, `src="https://solidping.example/dash0/logo.png"`)
	r.Contains(html, `alt="SolidPing"`)
	// The wordmark survives next to the mark, so the header reads correctly
	// with images off.
	r.Contains(html, ">SolidPing<")
	// PNG, not SVG: mail-client support for SVG is patchy to non-existent.
	r.NotContains(html, "logo.svg")
	// No CID: sender.go's MIME assembly is explicitly out of scope.
	r.NotContains(html, "cid:")
}

// TestBranding_NoBaseURLFallsBackToWordmark is the negative control for the
// test above: with nothing to build an absolute URL from, the header renders
// the text wordmark rather than a broken image.
func TestBranding_NoBaseURLFallsBackToWordmark(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	html := renderBranded(t, "", brandingViewModel())

	r.NotContains(html, "<img")
	r.Contains(html, ">SolidPing<")
}

// TestBranding_OrgLogoReplacesTheProductLogo pins stage 3's org branding: the
// org's logo becomes the primary mark, SolidPing drops to the secondary
// "sent by SolidPing" attribution, and a stored site-relative /pub/assets path
// is made absolute (a relative src in an email is just a broken image).
func TestBranding_OrgLogoReplacesTheProductLogo(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	data := brandingViewModel()
	data["OrgName"] = "Acme Corp"
	data["BrandName"] = "Acme Corp"
	data["OrgLogoURL"] = "/pub/assets/7c9e6679-7425-40de-944b-e07fc1f90ae7"

	html := renderBranded(t, brandingTestBaseURL, data)

	r.Contains(html, `src="https://solidping.example/pub/assets/7c9e6679-7425-40de-944b-e07fc1f90ae7"`)
	r.Contains(html, `alt="Acme Corp"`)
	r.Contains(html, "sent by SolidPing")
	// The product logo is not shown alongside it — one primary mark only.
	r.NotContains(html, "/dash0/logo.png")
	// The footer attribution (the existing wording) still names the org.
	r.Contains(html, "Acme Corp — sent by SolidPing")
}

// TestBranding_ExternalOrgLogoPassesThrough — an org may store a full external
// URL instead of an upload; it must not be prefixed with the base URL.
func TestBranding_ExternalOrgLogoPassesThrough(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	data := brandingViewModel()
	data["OrgLogoURL"] = "https://cdn.acme.com/logo.png"

	html := renderBranded(t, brandingTestBaseURL, data)

	r.Contains(html, `src="https://cdn.acme.com/logo.png"`)
	r.NotContains(html, "https://solidping.example/https://cdn.acme.com")
}

// TestBranding_HideBrandingRendersNoLogo is the white-label requirement from
// the resolved open questions: when the status page sets hide_branding, the
// status-subscriber mail carries NO logo and no SolidPing attribution at all,
// even though a logo URL is present in the view model.
//
// The first half is the positive control — the very same view model WITHOUT
// the flag does render the logo — so a passing test cannot mean "the branding
// never renders anyway".
func TestBranding_HideBrandingRendersNoLogo(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	data := brandingViewModel()
	data["OrgName"] = "Acme Corp"
	data["BrandName"] = "Acme Status"
	data["OrgLogoURL"] = "/pub/assets/page-logo"

	shown := renderBranded(t, brandingTestBaseURL, data)
	r.Contains(shown, "/pub/assets/page-logo")
	r.Contains(shown, "sent by SolidPing")

	data["HideBranding"] = true
	hidden := renderBranded(t, brandingTestBaseURL, data)

	r.NotContains(hidden, "<img")
	r.NotContains(hidden, "/pub/assets/page-logo")
	r.NotContains(hidden, "/dash0/logo.png")
	r.NotContains(hidden, "sent by SolidPing")
	// The page's own name still identifies the sender.
	r.Contains(hidden, "Acme Status")
}

// TestBranding_NonHTTPLogoIsDropped — a stored value that is neither
// absolute-http(s) nor site-relative never reaches an <img src>.
func TestBranding_NonHTTPLogoIsDropped(t *testing.T) {
	t.Parallel()

	for _, hostile := range []string{
		"javascript:alert(1)",
		"data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=",
		"assets/relative-logo.png",
	} {
		t.Run(hostile, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			data := brandingViewModel()
			data["OrgLogoURL"] = hostile

			html := renderBranded(t, brandingTestBaseURL, data)

			r.NotContains(html, hostile)
			// It falls back to the product logo rather than rendering nothing.
			r.Contains(html, "/dash0/logo.png")
		})
	}
}

// structViewModel has none of the branding fields base.html reads. It stands in
// for the real struct-backed view models (uptimereport.Data and friends).
//
// It is rendered through test-email.html rather than an incident template
// because the incident templates read a wider field set of their own — the
// point under test is base.html's wrapper, not the content block.
type structViewModel struct {
	Subject string
	Heading string
	Body    string
}

func renderBrandedStruct(t *testing.T, data any) string {
	t.Helper()

	formatter, err := NewFormatter(WithBaseURL(brandingTestBaseURL))
	require.NoError(t, err)

	_, html, _, err := formatter.Format("test-email.html", data)
	require.NoError(t, err)

	return html
}

// TestBranding_StructViewModelWithoutBrandingFields is the guard for the trap
// documented in supportreply.go: html/template ERRORS on a missing STRUCT
// field, so referencing `.OrgLogoURL` directly in base.html would break every
// struct-backed template at send time. Reading the branding keys through the
// nil-tolerant `field` helper is what makes this render instead of failing.
//
// The positive control below proves the same helper still FINDS the field when
// the struct does carry it — otherwise `field` could be returning nil always
// and this test would still pass.
func TestBranding_StructViewModelWithoutBrandingFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	html := renderBrandedStruct(t, structViewModel{
		Subject: "Hello", Heading: "Hello", Body: "Body text",
	})

	r.Contains(html, "Body text")
	r.Contains(html, "/dash0/logo.png")
}

type brandedStructViewModel struct {
	structViewModel

	OrgName    string
	BrandName  string
	OrgLogoURL string
}

func TestBranding_StructViewModelWithBrandingFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	html := renderBrandedStruct(t, brandedStructViewModel{
		structViewModel: structViewModel{
			Subject: "Hello", Heading: "Hello", Body: "Body text",
		},
		OrgName:    "Acme Corp",
		BrandName:  "Acme Corp",
		OrgLogoURL: "/pub/assets/struct-logo",
	})

	r.Contains(html, "https://solidping.example/pub/assets/struct-logo")
	r.Contains(html, "Acme Corp — sent by SolidPing")
}

// TestBranding_EveryTemplateRendersWithAndWithoutBranding renders every shipped
// template both bare and with the full branding key set, catching a template
// that would break on either shape.
func TestBranding_EveryTemplateRendersWithAndWithoutBranding(t *testing.T) {
	t.Parallel()

	names, err := ShippedTemplateNames()
	require.NoError(t, err)
	require.NotEmpty(t, names)

	formatter, err := NewFormatter(WithBaseURL(brandingTestBaseURL))
	require.NoError(t, err)

	branded := map[string]any{
		"OrgName": "Acme Corp", "BrandName": "Acme Corp",
		"OrgLogoURL": "/pub/assets/logo", "HideBranding": false,
		"IncidentNumber": 0,
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			for label, data := range map[string]any{
				"bare":    map[string]any{"IncidentNumber": 0},
				"branded": branded,
			} {
				_, html, _, formatErr := formatter.Format(name, data)
				r.NoError(formatErr, "%s (%s)", name, label)
				r.NotContains(html, "{{", "unresolved template syntax in %s (%s)", name, label)
			}
		})
	}
}

func TestFormatterAbsoluteURL(t *testing.T) {
	t.Parallel()

	formatter, err := NewFormatter(WithBaseURL("https://solidping.example/"))
	require.NoError(t, err)

	value := "ptr"

	cases := map[string]struct {
		in   any
		want string
	}{
		"nil":          {nil, ""},
		"empty":        {"", ""},
		"relative":     {"/pub/assets/x", "https://solidping.example/pub/assets/x"},
		"absolute":     {"https://cdn.acme.com/l.png", "https://cdn.acme.com/l.png"},
		"plain http":   {"http://cdn.acme.com/l.png", "http://cdn.acme.com/l.png"},
		"bare name":    {"logo.png", ""},
		"javascript":   {"javascript:alert(1)", ""},
		"pointer":      {&value, ""},
		"wrong type":   {42, ""},
		"whitespace":   {"   ", ""},
		"trailing pad": {"  /pub/a  ", "https://solidping.example/pub/a"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, formatter.absoluteURL(tc.in))
		})
	}
}

func TestFormatterFieldHelper(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Nil(field(nil, "X"))
	r.Nil(field(map[string]any{}, "X"))
	r.Equal("v", field(map[string]any{"X": "v"}, "X"))
	r.Equal("v", field(struct{ X string }{X: "v"}, "X"))
	r.Equal("v", field(&struct{ X string }{X: "v"}, "X"))
	r.Nil(field(struct{ Y string }{}, "X"))
	r.Nil(field((*struct{ X string })(nil), "X"))
	r.Nil(field(map[int]string{1: "v"}, "X"))
	r.Nil(field("not a container", "X"))
	// Unexported fields cannot be read through reflection (.Interface() would
	// panic), so they must read as absent rather than crash a send.
	r.Nil(field(unexportedFieldStruct{value: "v"}, "value"))
}

// unexportedFieldStruct exists only to prove field() refuses to read an
// unexported field instead of panicking on reflect.Value.Interface().
type unexportedFieldStruct struct {
	value string
}

// TestBranding_BaseURLIsResolvedLate pins the late-binding contract behind
// WithBaseURLFunc: config.Server.BaseURL is not final when the formatter is
// constructed (the systemconfig overlay applies SP_BASE_URL afterwards), so
// the formatter must read it per render, not capture it once.
//
// Without this, every production email would carry the pre-overlay default
// (a localhost logo URL) — which is exactly the bug a side-car run surfaced.
func TestBranding_BaseURLIsResolvedLate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	current := "http://localhost:4000"

	formatter, err := NewFormatter(WithBaseURLFunc(func() string { return current }))
	r.NoError(err)

	_, before, _, err := formatter.Format("welcome.html", map[string]any{})
	r.NoError(err)
	r.Contains(before, "http://localhost:4000/dash0/logo.png")

	// The overlay lands after construction.
	current = "https://monitoring.acme.com/"

	_, after, _, err := formatter.Format("welcome.html", map[string]any{})
	r.NoError(err)
	r.Contains(after, "https://monitoring.acme.com/dash0/logo.png")
	r.NotContains(after, "localhost:4000")
}

// TestBranding_HelperClassesWinOverTheGenericContentRules guards a subtle
// premailer trap: `.content h2` / `.content p` are MORE specific than a lone
// helper class, so a bare `.section-title` silently loses its font-size and
// margins to the generic rule once the CSS is inlined — the exact styling the
// per-template inline styles used to provide. The helpers are therefore
// declared as `.content h2.section-title` / `.content p.eyebrow`.
func TestBranding_HelperClassesWinOverTheGenericContentRules(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter(WithBaseURL(brandingTestBaseURL))
	r.NoError(err)

	_, report, _, err := formatter.Format("uptime-report.html", map[string]any{
		"HasData": true, "OrgName": "Acme Corp", "PeriodLabel": "July 2026",
		"Checks": []map[string]any{{"Name": "API", "HasData": true, "AvailabilityPct": "99.9"}},
	})
	r.NoError(err)
	// The section subhead keeps its own size and spacing, not the generic
	// .content h2 ones, and its accent rule survives the inlining.
	r.Contains(report, "font-size:16px")
	r.Contains(report, "margin:26px 0 10px")
	r.Contains(report, "border-left:3px solid #0072d5")
	r.NotContains(report, "margin:0 0 16px;font-size:16px")

	_, update, _, err := formatter.Format("status-subscriber-update.html", map[string]any{
		"Subject": "s", "Label": "New incident", "Title": "T", "BodyMarkdown": "B",
		"PageName": "Acme Status",
	})
	r.NoError(err)
	// The eyebrow keeps its uppercase treatment and its tight bottom margin.
	r.Contains(update, "text-transform:uppercase")
	r.Contains(update, "margin:0 0 6px")
}

// TestApplyOrgBrandingWritesTheKeysBaseHTMLReads asserts the helper against
// LITERAL key names, deliberately not against the KeyOrgName/KeyBrandName/
// KeyOrgLogoURL constants it uses: a typo inside a constant would rename the
// key on both sides at once and a constant-based assertion would happily
// follow it, silently unbranding every template that goes through this helper.
func TestApplyOrgBrandingWritesTheKeysBaseHTMLReads(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	logo := "/pub/assets/org-logo-uid"
	viewModel := map[string]any{"Existing": "kept"}

	ApplyOrgBranding(viewModel, "Acme Corp", &logo)

	r.Equal("Acme Corp", viewModel["OrgName"])
	r.Equal("Acme Corp", viewModel["BrandName"])
	r.Equal("/pub/assets/org-logo-uid", viewModel["OrgLogoURL"])
	r.Equal("kept", viewModel["Existing"], "the helper must not clobber the caller's keys")

	// A logo-less org writes an EMPTY value rather than omitting the key, so
	// the wrapper's fallback path is taken explicitly. Presence is asserted
	// separately from emptiness on purpose — a missing key is also "empty",
	// and that is exactly the case this pins down.
	logoless := map[string]any{}
	ApplyOrgBranding(logoless, "Acme Corp", nil)

	logoValue, present := logoless["OrgLogoURL"]
	r.True(present, "the key must be written, not omitted")
	r.Empty(logoValue)
	r.Equal("Acme Corp", logoless["OrgName"])

	// A nil view model is a no-op, not a panic: several callers build the map
	// conditionally.
	r.NotPanics(func() { ApplyOrgBranding(nil, "Acme Corp", &logo) })
}

// TestApplyOrgBrandingRendersThroughTheWrapper is the end-to-end half: the
// keys the helper writes must be the keys base.html actually reads. It is what
// catches a rename on either side — the six call sites (invitation, membership
// request new/decision, paging nudge, custom-domain demotion, escalation) all
// brand exclusively through this helper.
//
// The logo-less case is the positive control: it proves the assertion below is
// about the ORG logo specifically and not satisfied by any <img> at all.
func TestApplyOrgBrandingRendersThroughTheWrapper(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := NewFormatter(WithBaseURL(brandingTestBaseURL))
	r.NoError(err)

	logo := "/pub/assets/org-logo-uid"

	branded := map[string]any{"Subject": "s", "Heading": "h", "Body": "b"}
	ApplyOrgBranding(branded, "Acme Corp", &logo)

	_, html, _, err := formatter.Format("test-email.html", branded)
	r.NoError(err)
	r.Contains(html, brandingTestBaseURL+"/pub/assets/org-logo-uid")
	r.Contains(html, `alt="Acme Corp"`)
	r.Contains(html, "Acme Corp — sent by SolidPing")
	r.NotContains(html, "/dash0/logo.png")

	logoless := map[string]any{"Subject": "s", "Heading": "h", "Body": "b"}
	ApplyOrgBranding(logoless, "Acme Corp", nil)

	_, plain, _, err := formatter.Format("test-email.html", logoless)
	r.NoError(err)
	r.NotContains(plain, "/pub/assets/org-logo-uid")
	r.Contains(plain, brandingTestBaseURL+"/dash0/logo.png")
	// The org is still named in the footer even without a logo.
	r.Contains(plain, "Acme Corp — sent by SolidPing")
}
