package emailpreview_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/emailpreview"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// shippedTemplates enumerates every template file
// server/internal/email/templates/ ships today, read from the embedded
// directory itself rather than kept in sync by hand. Reading the real
// directory is what makes TestEveryShippedTemplateHasFixture a tripwire: a
// template added without a preview fixture fails immediately instead of
// shipping unpreviewable.
func shippedTemplates(t *testing.T) []string {
	t.Helper()

	names, err := email.ShippedTemplateNames()
	require.NoError(t, err)
	require.NotEmpty(t, names)

	return names
}

func newTestRouter(t *testing.T) *httpx.Router {
	t.Helper()

	// A base URL is configured so the preview exercises the same absolute-asset
	// path the real send does (the logo <img> in base.html) rather than a
	// degraded no-base-URL rendering nobody ever receives.
	formatter, err := email.NewFormatter(email.WithBaseURL("https://preview.example"))
	require.NoError(t, err)

	handler := emailpreview.NewHandler(formatter, &config.Config{})

	router := httpx.New()
	router.GET("/api/mgmt/email-preview", handler.Index)
	router.GET("/api/mgmt/email-preview/:template", handler.Preview)

	return router
}

func doGet(t *testing.T, router *httpx.Router, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestPreview_AllShippedTemplatesRender confirms every real template renders
// via the preview route (format=html, the default) with a 200 and non-empty
// body, and format=text also succeeds (may be an empty-text placeholder
// message for templates without a {{define "text"}} block — none exist
// today, but the route itself must not error).
func TestPreview_AllShippedTemplatesRender(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	for _, tmpl := range shippedTemplates(t) {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			htmlRec := doGet(t, router, "/api/mgmt/email-preview/"+tmpl)
			r.Equal(http.StatusOK, htmlRec.Code, "html preview for %s", tmpl)
			r.Contains(htmlRec.Header().Get("Content-Type"), "text/html")
			r.NotEmpty(htmlRec.Body.String())
			r.NotContains(htmlRec.Body.String(), "{{", "no unresolved template syntax in %s", tmpl)

			textRec := doGet(t, router, "/api/mgmt/email-preview/"+tmpl+"?format=text")
			r.Equal(http.StatusOK, textRec.Code, "text preview for %s", tmpl)
			r.Contains(textRec.Header().Get("Content-Type"), "text/plain")
			r.NotEmpty(textRec.Body.String())
		})
	}
}

func TestPreview_UnknownTemplate404s(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router := newTestRouter(t)

	rec := doGet(t, router, "/api/mgmt/email-preview/does-not-exist.html")
	r.Equal(http.StatusNotFound, rec.Code)
}

func TestPreview_InvalidFormat400s(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router := newTestRouter(t)

	rec := doGet(t, router, "/api/mgmt/email-preview/welcome.html?format=xml")
	r.Equal(http.StatusBadRequest, rec.Code)
}

// TestPreview_ColorSchemeDarkActivatesTheDarkBlock is what makes the dashboard's
// Light/Dark toggle honest.
//
// An <iframe> cannot be told to report prefers-color-scheme: dark to the page it
// hosts, so the endpoint rewrites the template's own media query to `@media all`
// — the dark CSS a capable client applies becomes unconditional, with no second
// palette to drift from the shipped one. Both halves are asserted: the query is
// gone (otherwise the block still would not fire in the iframe) AND the dark
// declarations are still there (otherwise the "dark" preview is just the light
// one with the block deleted, which would look like a working toggle).
func TestPreview_ColorSchemeDarkActivatesTheDarkBlock(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router := newTestRouter(t)

	rec := doGet(t, router, "/api/mgmt/email-preview/incident-created.html?colorScheme=dark")
	r.Equal(http.StatusOK, rec.Code)

	body := rec.Body.String()
	r.NotContains(body, "prefers-color-scheme", "the media query survived — the dark block never fires in an iframe")
	r.Contains(body, "@media all")
	r.Contains(body, "background-color: #141e28", "the dark card surface is gone, so this is not a dark preview at all")

	// The light pin is NOT rewritten: the preview only unconditionalizes the
	// media block, it does not simulate un-pinning the mail.
	r.Contains(body, `name="color-scheme" content="light only"`)
}

// TestPreview_DefaultsToTheUntouchedTemplate is the positive control for the
// test above: without the param — and with an explicit colorScheme=light — the
// endpoint must serve byte-identical bytes to what the mailer sends, media
// query and all. Without this, a rewrite that fired unconditionally would still
// pass the dark test.
func TestPreview_DefaultsToTheUntouchedTemplate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router := newTestRouter(t)

	plain := doGet(t, router, "/api/mgmt/email-preview/incident-created.html")
	r.Equal(http.StatusOK, plain.Code)
	r.Contains(plain.Body.String(), "@media (prefers-color-scheme: dark)")
	r.NotContains(plain.Body.String(), "@media all")

	light := doGet(t, router, "/api/mgmt/email-preview/incident-created.html?colorScheme=light")
	r.Equal(http.StatusOK, light.Code)
	r.Equal(plain.Body.String(), light.Body.String())
}

// TestPreview_ColorSchemeDoesNotTouchThePlaintextPart — the plaintext
// alternative has no styling to switch, and the rewrite is scoped to the HTML
// branch. A shared post-processing step applied before the format switch would
// be caught here.
func TestPreview_ColorSchemeDoesNotTouchThePlaintextPart(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router := newTestRouter(t)

	plain := doGet(t, router, "/api/mgmt/email-preview/welcome.html?format=text")
	dark := doGet(t, router, "/api/mgmt/email-preview/welcome.html?format=text&colorScheme=dark")

	r.Equal(http.StatusOK, plain.Code)
	r.Equal(http.StatusOK, dark.Code)
	r.Equal(plain.Body.String(), dark.Body.String())
}

func TestPreview_InvalidColorScheme400s(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router := newTestRouter(t)

	rec := doGet(t, router, "/api/mgmt/email-preview/welcome.html?colorScheme=sepia")
	r.Equal(http.StatusBadRequest, rec.Code)
}

// TestEveryShippedTemplateHasFixture is the "no template ships unpreviewable"
// guard the spec asks for: every file in server/internal/email/templates/
// except base.html must have a preview fixture. Adding a template without one
// fails here, at the moment it is added, rather than the next time somebody
// tries to look at it.
func TestEveryShippedTemplateHasFixture(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	covered := make(map[string]bool)
	for _, name := range emailpreview.FixtureTemplateNames() {
		covered[name] = true
	}

	for _, tmpl := range shippedTemplates(t) {
		r.True(covered[tmpl],
			"template %s has no fixture in emailpreview.fixtureBuilders — add one so it can be previewed", tmpl)
	}

	// The reverse direction: a fixture for a template that no longer exists is
	// dead weight that would also make the index list a 404-ing row.
	shipped := make(map[string]bool)
	for _, tmpl := range shippedTemplates(t) {
		shipped[tmpl] = true
	}

	for name := range covered {
		r.True(shipped[name], "fixture %s has no matching template file", name)
	}

	// base.html is the wrapper, never rendered on its own: it must NOT be
	// listed. Without this the two loops above would still pass if the shipped
	// list started including it.
	r.False(covered[email.TemplateBase], "base.html must not be previewable on its own")
}

// TestPreviewIndex_ListsEveryTemplate covers the index endpoint's contract:
// wrapped in {"data": [...]} per the repo's REST conventions, one row per
// shipped template, subjects rendered through the real Format() path, and no
// row reporting a render error.
func TestPreviewIndex_ListsEveryTemplate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router := newTestRouter(t)

	rec := doGet(t, router, "/api/mgmt/email-preview")
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Header().Get("Content-Type"), "application/json")

	var body struct {
		Data []emailpreview.TemplateSummary `json:"data"`
	}

	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))

	expected := shippedTemplates(t)
	r.Len(body.Data, len(expected))

	seen := make(map[string]emailpreview.TemplateSummary, len(body.Data))
	for _, row := range body.Data {
		seen[row.Template] = row
	}

	for _, tmpl := range expected {
		row, ok := seen[tmpl]
		r.True(ok, "index is missing %s", tmpl)
		r.Empty(row.Error, "index reports a render error for %s", tmpl)
		r.Equal("/api/mgmt/email-preview/"+tmpl, row.PreviewURL)
		r.NotEmpty(row.Subject, "no subject rendered for %s", tmpl)
		r.True(row.HasText, "no plaintext part for %s", tmpl)
		r.NotContains(row.Subject, "<no value>", "unresolved view-model key in %s subject", tmpl)
	}
}

// TestPreview_RendersTheBrandedHeader pins that the preview shows the real
// branded wrapper — the absolute logo URL and its text fallback — rather than
// a stripped-down rendering. It is the surface stage 2 and 3 are iterated on,
// so a preview that silently lost the header would defeat the whole point.
func TestPreview_RendersTheBrandedHeader(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router := newTestRouter(t)

	rec := doGet(t, router, "/api/mgmt/email-preview/welcome.html")
	r.Equal(http.StatusOK, rec.Code)

	body := rec.Body.String()
	r.Contains(body, `src="https://preview.example/dash0/logo.png"`)
	r.Contains(body, `alt="SolidPing"`)
	// Premailer really ran: the class-based rules are inlined as style="…".
	r.Contains(body, "background-color:#0f1a24")
}
