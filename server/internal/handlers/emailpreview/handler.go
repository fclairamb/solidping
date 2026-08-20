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

// Handler serves GET /api/mgmt/email-preview/{template}.
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
