// Package emailpreview provides a dev/test-only HTTP route that renders any
// shipped email template with fixture data in the browser, so the design
// pass in server/internal/email/templates/ can be iterated on visually
// without round-tripping through SMTP (spec D5). Gated on SP_RUNMODE=test,
// mirroring the existing /api/test/* cluster in internal/app/server.go — not
// compiled out, just 404s outside test mode.
package emailpreview

import (
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// previewPathPrefix is the route prefix the index echoes back so the dashboard
// does not rebuild the URL shape by hand.
const previewPathPrefix = "/api/mgmt/email-preview/"

// Handler serves GET /api/mgmt/email-preview and
// GET /api/mgmt/email-preview/{template}.
type Handler struct {
	base.HandlerBase
	formatter email.Formatter
}

// NewHandler creates a new preview handler. formatter is the same
// email.Formatter used by the real send path (services.Registry.EmailFormatter)
// — the preview is provably identical rendering, not a reimplementation.
func NewHandler(formatter email.Formatter, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		formatter:   formatter,
	}
}

// Preview renders a template with fixture data.
// GET /api/mgmt/email-preview/{template}?format=html|text (default html).
func (h *Handler) Preview(writer http.ResponseWriter, req *http.Request) error {
	templateName := httpx.Param(req, "template")

	data, ok := fixtureFor(templateName)
	if !ok {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound,
			"Unknown template '"+templateName+"'. See emailpreview.fixtures for the supported list.")
	}

	subject, html, text, err := h.formatter.Format(templateName, data)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	format := req.URL.Query().Get("format")
	if format == "" {
		format = "html"
	}

	switch format {
	case "html":
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusOK)
		_, writeErr := writer.Write([]byte(html))

		return writeErr
	case "text":
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusOK)

		body := text
		if body == "" {
			body = "(this template defines no {{define \"text\"}} block — subject would be: " + subject + ")"
		}

		_, writeErr := writer.Write([]byte(body))

		return writeErr
	default:
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"format must be 'html' or 'text'")
	}
}

// TemplateSummary is one row of the preview index. Names are camelCase per the
// repo's REST conventions; `template` is the on-disk file name, which is also
// the path segment the per-template preview route takes.
type TemplateSummary struct {
	Template string `json:"template"`
	// Subject is the rendered {{define "subject"}} block, "" when the template
	// defines none. Rendered through the real Format() path, so what the index
	// lists is what the mailer would send.
	Subject string `json:"subject"`
	// HasText reports whether the template ships a {{define "text"}} block. The
	// UI uses it to disable the text toggle rather than showing a placeholder.
	HasText bool `json:"hasText"`
	// PreviewURL is the HTML preview URL for this template (append
	// &format=text for the plaintext part).
	PreviewURL string `json:"previewUrl"`
	// Error is the render failure message when this template could not be
	// rendered with its fixture. Non-empty here is a real bug (a view-model /
	// template key mismatch) — the index reports it instead of failing the
	// whole listing, so one broken template does not blind the catalog.
	Error string `json:"error,omitempty"`
}

// Index lists every previewable template with its rendered subject.
// GET /api/mgmt/email-preview.
func (h *Handler) Index(writer http.ResponseWriter, _ *http.Request) error {
	names := FixtureTemplateNames()
	summaries := make([]TemplateSummary, 0, len(names))

	for _, name := range names {
		summaries = append(summaries, h.summarize(name))
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": summaries})
}

// summarize renders one template with its fixture to extract the subject line
// and whether a plaintext part exists.
func (h *Handler) summarize(name string) TemplateSummary {
	summary := TemplateSummary{
		Template:   name,
		PreviewURL: previewPathPrefix + name,
	}

	data, ok := fixtureFor(name)
	if !ok {
		summary.Error = "no fixture"

		return summary
	}

	subject, _, text, err := h.formatter.Format(name, data)
	if err != nil {
		summary.Error = err.Error()

		return summary
	}

	summary.Subject = subject
	summary.HasText = text != ""

	return summary
}
