package emailpreview_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// preheaderRE captures the hidden preview-text div base.html emits as its very
// first body element. Matched on the inline `display:none` style rather than a
// class because premailer inlines (and may drop) classes — the inline style is
// what actually survives into the sent message.
var preheaderRE = regexp.MustCompile(`(?s)<div style="display:none.*?</div>`)

// tagRE and entityRE strip the markup and the zero-width filler run so a
// preheader's real, human-visible text is what gets asserted on.
var (
	tagRE    = regexp.MustCompile(`<[^>]+>`)
	entityRE = regexp.MustCompile(`&#\d+;|&nbsp;|&zwnj;`)
	// The filler run reaches the test as DECODED characters, not entities:
	// premailer re-serializes the document, so &#8203; is a literal zero-width
	// space by then. Stripping only the entity spelling is what made the first
	// version of this test pass on a template with no preheader at all.
	invisibleRE = regexp.MustCompile(`[\x{200B}-\x{200D}\x{034F}\x{FEFF}\x{00A0}\s]+`)
)

// preheaderText extracts the visible preheader text of a rendered email.
// Returns "" when the mail has no preheader div at all.
func preheaderText(body string) string {
	match := preheaderRE.FindString(body)
	if match == "" {
		return ""
	}

	text := entityRE.ReplaceAllString(tagRE.ReplaceAllString(match, ""), "")
	text = invisibleRE.ReplaceAllString(text, " ")

	return strings.TrimSpace(text)
}

// TestPreview_EveryTemplateShipsAPreheader pins the inbox preview line.
//
// A missing {{define "preheader"}} does not fail a render — it produces an
// EMPTY hidden div, and the mail client then falls back to scraping the first
// visible text, which for an alert is the "SolidPing" wordmark and for a CTA
// mail is a raw URL. That failure is invisible in the preview page (the div is
// hidden by design) and only shows up in a real inbox, so it is pinned here.
func TestPreview_EveryTemplateShipsAPreheader(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	for _, tmpl := range shippedTemplates(t) {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			rec := doGet(t, router, "/api/mgmt/email-preview/"+tmpl)
			r.Equal(http.StatusOK, rec.Code)

			text := preheaderText(rec.Body.String())
			r.NotEmpty(text, "%s renders no preheader text — the inbox will scrape the wordmark instead", tmpl)
			r.GreaterOrEqual(len(text), 20, "%s preheader is too short to be useful: %q", tmpl, text)
		})
	}
}

// TestPreview_NoUnresolvedTemplateValues is the tripwire for a view-model key
// that a template asks for and the data does not carry.
//
// html/template renders a missing MAP key as the literal "<no value>" rather
// than failing, so a typo in a new preheader or table row ships a mail that
// reads "…failing since <no value>". Nothing else in the suite catches it: the
// render succeeds, the status is 200, the body is non-empty.
//
// The assertion runs on the PLAINTEXT part on purpose. In the HTML part the
// marker never survives: premailer re-parses the document, reads "<no value>"
// as an unknown start tag and drops it — along with the rest of the element it
// sat in. That silent disappearance is the worse failure of the two, and it is
// invisible to any string search, so the text part is the only place a missing
// key can still be caught. Every shipped template defines a "text" block, and
// both parts are rendered from the same view model.
func TestPreview_NoUnresolvedTemplateValues(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	for _, tmpl := range shippedTemplates(t) {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			rec := doGet(t, router, "/api/mgmt/email-preview/"+tmpl+"?format=text")
			r.Equal(http.StatusOK, rec.Code)

			body := rec.Body.String()
			r.NotContains(body, "<no value>",
				"%s references a key its view model does not carry", tmpl)
			r.NotContains(body, "defines no {{define \"text\"}} block",
				"%s ships no plaintext part, so its keys are unverifiable", tmpl)
		})
	}
}

// TestPreview_PinsLightRendering guards the color-scheme declarations.
//
// Without them Apple Mail, Outlook.com and the Gmail Android app auto-invert a
// light-only email: the white card goes grey and, worse, the status banner that
// carries the entire meaning of an incident alert gets recolored. The design is
// deliberately light-only (see the spec), which is exactly why it has to SAY so
// — "no dark styles" is not the same as "renders in light".
func TestPreview_PinsLightRendering(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	rec := doGet(t, router, "/api/mgmt/email-preview/incident-created.html")
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `name="color-scheme" content="light only"`)
	require.Contains(t, body, `name="supported-color-schemes" content="light only"`)
	require.Contains(t, body, `name="x-apple-disable-message-reformatting"`)
	require.Contains(t, body, "color-scheme: light only")

	// The negative half, and the point of this test now that base.html also
	// ships a designed dark palette: shipping dark styles is NOT a licence to
	// un-pin. Flipping any of the three declarations to "light dark" hands the
	// mail to Gmail's auto-darkening algorithm — a separate, human-gated
	// decision written up in wiki/features/email-dark-mode.md. The trio is one
	// declaration in three places; each spelling is asserted so a partial flip
	// (which would leave the clients disagreeing about the same mail) fails
	// just as loudly as a full one.
	require.NotContains(t, body, "light dark")
	require.NotContains(t, body, `content="dark"`)
	// Newline-anchored: the :root declaration starts a line in premailer's
	// output, which "prefers-color-scheme: dark" (always preceded by "@media (")
	// never does — so this catches a flipped :root without tripping on the
	// dark block's own media query.
	require.NotContains(t, body, "\ncolor-scheme: dark")
	require.NotContains(t, body, "\nsupported-color-schemes: dark")
}

// TestPreview_ShipsADarkPalette pins the dark block onto every rendered mail.
//
// base.html is the single stylesheet all 24 templates dress themselves from, so
// a template can only lose the dark palette by losing the wrapper — which this
// catches. It asserts on the RENDERED output rather than the source file
// because premailer is what decides whether a media block survives inlining at
// all: rules it cannot inline stay in a <style> block (with !important added,
// which is what lets them beat the inlined light values). A premailer upgrade
// that started dropping media blocks would silently ship light-only mail again.
func TestPreview_ShipsADarkPalette(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	for _, tmpl := range shippedTemplates(t) {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			rec := doGet(t, router, "/api/mgmt/email-preview/"+tmpl)
			r.Equal(http.StatusOK, rec.Code)

			body := rec.Body.String()
			r.Contains(body, "prefers-color-scheme: dark",
				"%s renders without the dark block — it will glare white in a dark inbox", tmpl)
			r.Contains(body, "#141e28",
				"%s carries the media query but not the dark card surface", tmpl)
		})
	}
}

// darkBlockRE captures the whole @media (prefers-color-scheme: dark) { ... }
// block out of base.html's SOURCE, brace-matched by the closing brace at the
// block's own indentation.
var darkBlockRE = regexp.MustCompile(`(?s)@media \(prefers-color-scheme: dark\) \{.*?\n        \}`)

// gradientDeclRE captures one background-image gradient declaration.
var gradientDeclRE = regexp.MustCompile(`background-image:\s*linear-gradient`)

// TestDarkPalette_EveryGradientKeepsASolidFallback is the dark-block twin of
// TestPreview_EveryGradientKeepsASolidFallback.
//
// That test walks the INLINED style attributes of a rendered mail, which by
// construction can never contain a media-block rule: premailer leaves those in
// the <style> element. So the dark block's own gradients — .quote,
// .btn-secondary, td.label, .metric, .footer — are invisible to it, and the
// exact Outlook failure it guards (a gradient with no solid fallback renders as
// nothing) would sail straight through. This reads base.html itself, rule by
// rule, and requires the pairing the file's own comment promises.
func TestDarkPalette_EveryGradientKeepsASolidFallback(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	source, err := os.ReadFile(filepath.Join("..", "..", "email", "templates", "base.html"))
	r.NoError(err)

	block := darkBlockRE.FindString(string(source))
	r.NotEmpty(block, "base.html has no dark-mode block, or it is no longer brace-matched at its own indentation")

	for _, line := range strings.Split(block, "\n") {
		if !gradientDeclRE.MatchString(line) {
			continue
		}

		r.Contains(line, "background-color:",
			"a dark-mode gradient has no solid fallback — it renders as nothing in Outlook: %s", strings.TrimSpace(line))
	}
}

// TestDarkPalette_LeavesTheSaturatedChromeAlone pins the deliberate omissions.
//
// The header is already dark navy and the status banners are saturated
// mid-tones that read correctly on a dark card — recoloring the banner is
// precisely the damage the light pin exists to prevent, so the dark block must
// not touch either. Same for the primary/success buttons. This is a real
// tripwire rather than a restatement: the natural instinct when extending the
// block is to "finish the job" and give every surface a dark value.
func TestDarkPalette_LeavesTheSaturatedChromeAlone(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	source, err := os.ReadFile(filepath.Join("..", "..", "email", "templates", "base.html"))
	r.NoError(err)

	block := darkBlockRE.FindString(string(source))
	r.NotEmpty(block)

	for _, selector := range []string{
		".header", ".accent-bar", ".status-banner", ".status-down", ".status-recovered",
		".status-escalated", ".status-reopened", ".status-comment", ".status-acknowledged",
		".btn-primary", ".btn-success", ".cta",
	} {
		r.NotContains(block, selector+" ",
			"the dark block restyles %s — it is deliberately left on its light value", selector)
	}
}

// TestPreview_DetailRowsUseStyledCells keeps the two-column fact grid intact.
//
// base.html styles the grid on td.label / td.value. uptime-report.html reached
// for the semantically-tempting <th> instead and rendered its whole report as
// centered, unpadded, un-backgrounded cells with ragged column widths — a
// silent design break, since the template still rendered fine. base.html now
// also styles <th> as a safety net; this asserts the convention on top of it,
// because the safety net cannot restore the label column's own width rules in
// clients that drop <style> blocks.
func TestPreview_DetailRowsUseStyledCells(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	for _, tmpl := range shippedTemplates(t) {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()

			rec := doGet(t, router, "/api/mgmt/email-preview/"+tmpl)
			require.Equal(t, http.StatusOK, rec.Code)
			require.NotContains(t, rec.Body.String(), "<th",
				"%s uses <th> in a details table — use td.label / td.value", tmpl)
		})
	}
}

// TestPreview_IncidentUIDIsAReferenceNotAFact keeps the opaque UUID out of the
// facts a woken-up human reads first. It stays in the mail (support asks for
// it) but as a footnote under the actions, not as a table row competing with
// "which check" and "since when".
func TestPreview_IncidentUIDIsAReferenceNotAFact(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	incidentTemplates := []string{
		"incident-created.html", "incident-resolved.html", "incident-acknowledged.html",
		"incident-unacknowledged.html", "incident-comment.html", "incident-escalated.html",
		"incident-reopened.html", "incident-burn-created.html", "incident-burn-resolved.html",
	}

	for _, tmpl := range incidentTemplates {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			rec := doGet(t, router, "/api/mgmt/email-preview/"+tmpl)
			r.Equal(http.StatusOK, rec.Code)

			body := rec.Body.String()
			r.Contains(body, "Incident reference", "%s dropped the support reference", tmpl)
			r.NotContains(body, ">Incident ID<", "%s still lists the UUID as a table fact", tmpl)
		})
	}
}

// styleAttrRE captures every inlined style attribute in a rendered mail.
var styleAttrRE = regexp.MustCompile(`style="([^"]*)"`)

// TestPreview_EveryGradientKeepsASolidFallback guards the polish pass.
//
// Outlook's Word rendering engine ignores background-image outright. A gradient
// declared without the background-color it degrades to therefore renders as
// nothing — which for the header and the status banner means white text on a
// white background, i.e. an incident alert whose severity color, and in the
// header its entire content, silently disappears for a large share of corporate
// readers. Every gradient in base.html is paired with a flat color; this is
// what stops the next one from not being.
func TestPreview_EveryGradientKeepsASolidFallback(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	for _, tmpl := range shippedTemplates(t) {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()

			rec := doGet(t, router, "/api/mgmt/email-preview/"+tmpl)
			require.Equal(t, http.StatusOK, rec.Code)

			for _, match := range styleAttrRE.FindAllStringSubmatch(rec.Body.String(), -1) {
				style := match[1]
				if !strings.Contains(style, "background-image:linear-gradient") {
					continue
				}

				require.Contains(t, style, "background-color:",
					"%s declares a gradient with no solid fallback — it renders as nothing in Outlook: %s",
					tmpl, style)
			}
		})
	}
}
