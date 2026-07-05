package unsubscribe

import (
	"errors"
	"html"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler serves the public (unauthenticated) unsubscribe surface:
//   - POST /unsubscribe?token=…       RFC 8058 one-click, idempotent.
//   - GET  /unsubscribe?token=…       Confirmation page (scope choice) or,
//     after a successful action, the outcome page.
//   - GET  /unsubscribe/undo?token=…&uid=…  Re-subscribe (deletes the row).
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new unsubscribe handler. cfg supplies the HMAC secret
// (cfg.Auth.JWTSecret) — the same secret ack tokens use; purpose tagging
// (not key separation) is what keeps the two token kinds apart.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
	}
}

// OneClickUnsubscribe handles POST /unsubscribe?token=… — RFC 8058's
// List-Unsubscribe-Post: List-Unsubscribe=One-Click contract. Mail clients
// (Gmail, Apple Mail, etc.) submit this on the recipient's behalf, often
// without the recipient seeing any page, so the response body is minimal —
// what matters is the status code and that repeated submissions are
// harmless. No auth: the signed token IS the authentication.
func (h *Handler) OneClickUnsubscribe(w http.ResponseWriter, req bunrouter.Request) error {
	token := req.URL.Query().Get("token")
	if token == "" {
		w.WriteHeader(http.StatusBadRequest)

		return nil
	}

	_, err := h.svc.Unsubscribe(req.Context(), token, ScopeFromToken, models.EmailSuppressionSourceHeader)
	if err != nil {
		// RFC 8058 doesn't define a body contract here, and leaking WHY the
		// token failed to an automated mail-client POST serves no one — a
		// tampered/expired/replayed token gets the same flat rejection as
		// any other invalid request. No suppression is created either way.
		if errors.Is(err, ErrTokenInvalid) || errors.Is(err, ErrOrgNotFound) {
			w.WriteHeader(http.StatusBadRequest)

			return nil
		}

		return h.WriteInternalError(w, err)
	}

	w.WriteHeader(http.StatusOK)

	return nil
}

// ConfirmationPage handles GET /unsubscribe?token=…&scope=check|org — the
// human-facing page. Without a scope query param it renders the choice page
// (two buttons); with scope=check or scope=org it performs that unsubscribe
// and renders the outcome page with an undo link. This means the same GET
// route both shows the form and handles its submission (the two buttons
// below are plain <a> links with the scope baked into the href — no POST
// needed from the browser since GET here has no side effect until a scope
// is chosen, and choosing IS the confirmation).
func (h *Handler) ConfirmationPage(w http.ResponseWriter, req bunrouter.Request) error {
	token := req.URL.Query().Get("token")
	if token == "" {
		writePage(w, http.StatusBadRequest, "Missing token", missingTokenBody())

		return nil
	}

	scope := Scope(req.URL.Query().Get("scope"))

	if scope == ScopeFromToken {
		return h.renderChoicePage(w, req, token)
	}

	result, err := h.svc.Unsubscribe(req.Context(), token, scope, models.EmailSuppressionSourceLink)
	if err != nil {
		return h.renderErrorPage(w, err)
	}

	writePage(w, http.StatusOK, "Unsubscribed", successBody(token, result))

	return nil
}

// renderChoicePage shows the two scope buttons without unsubscribing yet.
// It still needs to verify the token (to show the check name and reject a
// bad token up front) but performs no write.
func (h *Handler) renderChoicePage(w http.ResponseWriter, req bunrouter.Request, token string) error {
	org, email, checkUID, err := h.svc.resolve(req.Context(), token, ScopeFromToken)
	if err != nil {
		return h.renderErrorPage(w, err)
	}

	checkName := ""
	if checkUID != "" {
		if check, checkErr := h.svc.dbService.GetCheck(req.Context(), org.UID, checkUID); checkErr == nil && check != nil {
			if check.Name != nil {
				checkName = *check.Name
			} else if check.Slug != nil {
				checkName = *check.Slug
			}
		}
	}

	writePage(w, http.StatusOK, "Unsubscribe from SolidPing alerts", choiceBody(token, email, checkUID, checkName))

	return nil
}

// Undo handles GET /unsubscribe/undo?token=…&uid=… — the re-subscribe action
// from the outcome page's undo link. Re-verifies the token server-side
// (Service.ResubscribeByUID) rather than trusting the uid alone, so an
// expired/forged undo link can't delete an arbitrary suppression row.
func (h *Handler) Undo(w http.ResponseWriter, req bunrouter.Request) error {
	token := req.URL.Query().Get("token")
	uid := req.URL.Query().Get("uid")

	if token == "" || uid == "" {
		writePage(w, http.StatusBadRequest, "Missing parameters", missingTokenBody())

		return nil
	}

	if err := h.svc.ResubscribeByUID(req.Context(), token, uid); err != nil {
		return h.renderErrorPage(w, err)
	}

	writePage(w, http.StatusOK, "Re-subscribed", resubscribedBody())

	return nil
}

// renderErrorPage maps a Service error to the right status/page.
func (h *Handler) renderErrorPage(w http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrTokenInvalid):
		writePage(w, http.StatusBadRequest, "Invalid or expired link", invalidTokenBody())
	case errors.Is(err, ErrOrgNotFound):
		writePage(w, http.StatusNotFound, "Organization not found", orgNotFoundBody())
	default:
		return h.WriteInternalError(w, err)
	}

	return nil
}

// escapeHTML is a short alias used throughout page.go to keep the templates
// readable — every value interpolated into HTML below is user-influenced
// (email address, check name from the DB) and must be escaped.
func escapeHTML(s string) string {
	return html.EscapeString(s)
}
