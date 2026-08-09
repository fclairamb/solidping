package incidentnotifications

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

const jsonDataKey = "data"

// Handler exposes the incident notifications read API.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler builds a handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
	}
}

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrgNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrIncidentNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Incident not found")
	case errors.Is(err, ErrNotificationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Notification not found")
	case errors.Is(err, ErrForbidden):
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Forbidden")
	default:
		return h.WriteInternalError(writer, err)
	}
}

// parseListFilter parses shared query-string parameters into a ListFilter.
func parseListFilter(req *http.Request) ListFilter {
	filter := ListFilter{Limit: 100}

	if v := req.URL.Query().Get("status"); v != "" {
		filter.Status = v
	}

	if v := req.URL.Query().Get("userUid"); v != "" {
		filter.UserUID = v
	}

	if v := req.URL.Query().Get("connectionUid"); v != "" {
		filter.ConnectionUID = v
	}

	if v := req.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}

	if v := req.URL.Query().Get("before"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Before = t
		}
	}

	return filter
}

// ListForIncident handles GET /api/v1/orgs/:org/incidents/:uid/notifications.
func (h *Handler) ListForIncident(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, err)
	}

	incidentUID := httpx.Param(req, "uid")
	filter := parseListFilter(req)

	rows, err := h.svc.ListForIncident(req.Context(), orgUID, incidentUID, filter)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{jsonDataKey: rows})
}

// GetForIncident handles
// GET /api/v1/orgs/:org/incidents/:uid/notifications/:notifUid.
func (h *Handler) GetForIncident(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, err)
	}

	incidentUID := httpx.Param(req, "uid")
	notifUID := httpx.Param(req, "notifUid")

	detail, err := h.svc.GetForIncident(req.Context(), orgUID, incidentUID, notifUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, detail)
}

// GetByOrg handles GET /api/v1/orgs/:org/notifications/:notifUid.
// Fetches a single notification scoped only by org UID (no incident required).
func (h *Handler) GetByOrg(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, err)
	}

	notifUID := httpx.Param(req, "notifUid")

	detail, err := h.svc.GetByOrg(req.Context(), orgUID, notifUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, detail)
}

// ListByOrg handles GET /api/v1/orgs/:org/notifications?connectionUid=&limit=.
// Requires connectionUid; returns 400 if absent. Defaults limit to 10, max 50.
func (h *Handler) ListByOrg(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, err)
	}

	connectionUID := req.URL.Query().Get("connectionUid")
	if connectionUID == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"connectionUid query parameter is required")
	}

	limit := 10
	if v := req.URL.Query().Get("limit"); v != "" {
		if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 {
			limit = n
		}
	}

	if limit > 50 {
		limit = 50
	}

	rows, err := h.svc.ListByConnection(req.Context(), orgUID, connectionUID, limit)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{jsonDataKey: rows})
}

// ListForUser handles GET /api/v1/orgs/:org/users/:uid/notifications.
// Admins can query any user; regular members can only query themselves.
func (h *Handler) ListForUser(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, err)
	}

	targetUID := httpx.Param(req, "uid")

	callerUser, ok := middleware.GetUserFromContext(req.Context())
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	// Authorization: self always allowed; other users require admin membership.
	if callerUser.UID != targetUID {
		member, memberErr := h.svc.db.GetMemberByUserAndOrg(req.Context(), callerUser.UID, orgUID)
		if memberErr != nil || !member.Role.AtLeast(models.MemberRoleAdmin) {
			return h.handleError(writer, ErrForbidden)
		}
	}

	filter := parseListFilter(req)

	rows, err := h.svc.ListForUser(req.Context(), orgUID, targetUID, filter)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{jsonDataKey: rows})
}

// ListForMe handles GET /api/v1/orgs/:org/me/notifications.
// Alias for ListForUser with the caller's own UID.
func (h *Handler) ListForMe(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, err)
	}

	callerUser, ok := middleware.GetUserFromContext(req.Context())
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	filter := parseListFilter(req)

	rows, err := h.svc.ListForUser(req.Context(), orgUID, callerUser.UID, filter)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{jsonDataKey: rows})
}
