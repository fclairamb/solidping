package emailpreview_test

import (
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
// server/internal/email/templates/ ships today, kept in sync manually (a
// discrepancy here — a template with no route coverage, or a route entry for
// a deleted template — fails TestPreview_AllShippedTemplatesRender below,
// which is the intended tripwire).
func shippedTemplates() []string {
	return []string{
		"incident-created.html",
		"incident-resolved.html",
		"incident-escalated.html",
		"incident-reopened.html",
		"escalation.html",
		"registration.html",
		"password-reset.html",
		"invitation.html",
		"welcome.html",
		"password-changed.html",
		"membership_request_new.html",
		"membership_request_decision.html",
	}
}

func newTestRouter(t *testing.T) *httpx.Router {
	t.Helper()

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	handler := emailpreview.NewHandler(formatter, &config.Config{})

	router := httpx.New()
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

	for _, tmpl := range shippedTemplates() {
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
