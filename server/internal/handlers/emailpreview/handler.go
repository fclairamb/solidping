// Package emailpreview provides a dev/test-only HTTP route that renders any
// shipped email template with fixture data in the browser, so the design
// pass in server/internal/email/templates/ can be iterated on visually
// without round-tripping through SMTP (spec D5). Gated on SP_RUNMODE=test,
// mirroring the existing /api/test/* cluster in internal/app/server.go — not
// compiled out, just 404s outside test mode.
package emailpreview

import (
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// ErrUnknownTemplate is returned when the {template} path param does not
// match one of the fixture-data entries below.
var ErrUnknownTemplate = errors.New("unknown template")

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
func (h *Handler) Preview(w http.ResponseWriter, req bunrouter.Request) error {
	templateName := req.Param("template")

	data, ok := fixtureFor(templateName)
	if !ok {
		return h.WriteError(w, http.StatusNotFound, base.ErrorCodeNotFound,
			"Unknown template '"+templateName+"'. See emailpreview.fixtures for the supported list.")
	}

	subject, html, text, err := h.formatter.Format(templateName, data)
	if err != nil {
		return h.WriteInternalError(w, err)
	}

	format := req.URL.Query().Get("format")
	if format == "" {
		format = "html"
	}

	switch format {
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, writeErr := w.Write([]byte(html))

		return writeErr
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)

		body := text
		if body == "" {
			body = "(this template defines no {{define \"text\"}} block — subject would be: " + subject + ")"
		}

		_, writeErr := w.Write([]byte(body))

		return writeErr
	default:
		return h.WriteError(w, http.StatusBadRequest, base.ErrorCodeValidationError,
			"format must be 'html' or 'text'")
	}
}
