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

	formatter, err := email.NewFormatter()
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
