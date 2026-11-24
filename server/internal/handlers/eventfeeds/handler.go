package eventfeeds

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/atomfeed"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// feedCacheControl lets a reader's own cache hold the feed for a minute and
// keeps it out of every shared cache: the body is org-internal.
const feedCacheControl = "private, max-age=60"

// goneBody is the single answer to an unknown, malformed or revoked secret.
const goneBody = "This event feed has been revoked or rotated."

// Handler serves the feed and its management routes. The management routes
// are org-admin only, enforced by the route group in app/server.go.
type Handler struct {
	base.HandlerBase

	svc *Service
	cfg *config.Config
}

// NewHandler constructs a Handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: svc, cfg: cfg}
}

// List handles GET /api/v1/orgs/:org/event-feeds.
func (h *Handler) List(writer http.ResponseWriter, req *http.Request) error {
	res, err := h.svc.List(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, res)
}

// Create handles POST /api/v1/orgs/:org/event-feeds.
func (h *Handler) Create(writer http.ResponseWriter, req *http.Request) error {
	var body CreateRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Body must be a JSON object with a \"name\"")
	}

	userUID := ""
	if user, ok := middleware.GetUserFromContext(req.Context()); ok && user != nil {
		userUID = user.UID
	}

	res, err := h.svc.Create(req.Context(), httpx.Param(req, "org"), userUID, &body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	res.URL = h.baseURL(req) + res.Path

	return h.WriteJSON(writer, http.StatusCreated, res)
}

// Rotate handles POST /api/v1/orgs/:org/event-feeds/:uid/rotate.
func (h *Handler) Rotate(writer http.ResponseWriter, req *http.Request) error {
	res, err := h.svc.Rotate(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	res.URL = h.baseURL(req) + res.Path

	return h.WriteJSON(writer, http.StatusOK, res)
}

// Revoke handles DELETE /api/v1/orgs/:org/event-feeds/:uid.
func (h *Handler) Revoke(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.Revoke(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid")); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// ServeFeed handles GET /api/v1/org-feeds/:secret/feed.xml. It is wired
// outside the auth middleware: the secret is the credential.
func (h *Handler) ServeFeed(writer http.ResponseWriter, req *http.Request) error {
	header := writer.Header()
	header.Set("X-Robots-Tag", "noindex, nofollow")
	header.Set("Referrer-Policy", "no-referrer")

	feed, err := h.svc.Resolve(req.Context(), httpx.Param(req, "secret"))
	if err != nil {
		return writeGone(writer)
	}

	lang := i18n.FromAcceptLanguage(req.Header.Get("Accept-Language"))

	doc, err := h.svc.Render(req.Context(), feed, h.baseURL(req), lang)
	if errors.Is(err, ErrUnknownSecret) {
		return writeGone(writer)
	}

	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	header.Set("Content-Type", atomfeed.ContentType)
	header.Set("Cache-Control", feedCacheControl)
	header.Set("Vary", "Accept-Language")
	writer.WriteHeader(http.StatusOK)

	return atomfeed.Encode(writer, doc)
}

// writeGone answers 410, the signal feed readers take to stop polling, with
// the same body whatever made the secret fail.
func writeGone(writer http.ResponseWriter) error {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusGone)
	_, _ = writer.Write([]byte(goneBody))

	return nil
}

// baseURL is the configured public URL, else the scheme and host the request
// came in on.
func (h *Handler) baseURL(req *http.Request) string {
	if h.cfg != nil && h.cfg.Server.BaseURL != "" {
		return strings.TrimRight(h.cfg.Server.BaseURL, "/")
	}

	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}

	return scheme + "://" + req.Host
}

func (h *Handler) handleError(writer http.ResponseWriter, req *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrgNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Event feed not found")
	case errors.Is(err, ErrInvalidName):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"A feed name is required (at most 100 characters)")
	case errors.Is(err, ErrInvalidScope):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"eventTypes may only contain: "+strings.Join(ScopeChoices, ", "))
	case errors.Is(err, ErrLimitReached):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeQuotaExceeded,
			"An organization can have at most 10 event feeds; revoke one first")
	default:
		return h.WriteInternalError(writer, req, err)
	}
}
