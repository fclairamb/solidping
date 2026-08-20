package agents

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	entitlementshandler "github.com/fclairamb/solidping/server/internal/handlers/entitlements"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Handler provides HTTP handlers for the private-locations admin API.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// statusBody builds the `{"status": "..."}` acknowledgement body returned by
// this API's delete and revoke endpoints.
func statusBody(status string) map[string]string {
	return map[string]string{"status": status}
}

// NewHandler creates a new agents admin handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListPrivateRegions handles GET /api/v1/orgs/:org/private-regions.
func (h *Handler) ListPrivateRegions(writer http.ResponseWriter, req *http.Request) error {
	resp, err := h.svc.ListPrivateRegions(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// CreatePrivateRegion handles POST /api/v1/orgs/:org/private-regions.
func (h *Handler) CreatePrivateRegion(writer http.ResponseWriter, req *http.Request) error {
	var body CreatePrivateRegionRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid request body")
	}

	resp, err := h.svc.CreatePrivateRegion(req.Context(), httpx.Param(req, "org"), &body)
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, resp)
}

// DeletePrivateRegion handles DELETE /api/v1/orgs/:org/private-regions/:slug.
func (h *Handler) DeletePrivateRegion(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.DeletePrivateRegion(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "slug")); err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, statusBody("deleted"))
}

// MintEnrollmentToken handles POST /api/v1/orgs/:org/agent-enrollment-tokens.
// The response is the only time the token secret is readable.
func (h *Handler) MintEnrollmentToken(writer http.ResponseWriter, req *http.Request) error {
	var body MintEnrollmentTokenRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid request body")
	}

	var createdBy *string
	if user, ok := middleware.GetUserFromContext(req.Context()); ok && user != nil {
		createdBy = &user.UID
	}

	resp, err := h.svc.MintEnrollmentToken(req.Context(), httpx.Param(req, "org"), &body, createdBy)
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, resp)
}

// ListEnrollmentTokens handles GET /api/v1/orgs/:org/agent-enrollment-tokens.
func (h *Handler) ListEnrollmentTokens(writer http.ResponseWriter, req *http.Request) error {
	resp, err := h.svc.ListEnrollmentTokens(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// DeleteEnrollmentToken handles DELETE /api/v1/orgs/:org/agent-enrollment-tokens/:uid.
func (h *Handler) DeleteEnrollmentToken(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.DeleteEnrollmentToken(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid")); err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, statusBody("deleted"))
}

// ListAgents handles GET /api/v1/orgs/:org/agents.
func (h *Handler) ListAgents(writer http.ResponseWriter, req *http.Request) error {
	resp, err := h.svc.ListAgents(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// ListAllAgents handles GET /api/v1/system/agents. Superadmin only: returns
// every agent across all orgs plus platform-operated system agents, which are
// otherwise invisible to any org-scoped view.
func (h *Handler) ListAllAgents(writer http.ResponseWriter, req *http.Request) error {
	resp, err := h.svc.ListAllAgents(req.Context())
	if err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// RevokeAgent handles DELETE /api/v1/orgs/:org/agents/:uid.
func (h *Handler) RevokeAgent(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.RevokeAgent(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid")); err != nil {
		return h.writeServiceError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, statusBody("revoked"))
}

// writeServiceError maps domain errors to HTTP responses.
func (h *Handler) writeServiceError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, entcore.ErrEntitlementExceeded):
		var qe *entcore.QuotaError
		if !errors.As(err, &qe) {
			return h.WriteInternalError(writer, request, err)
		}

		body := entitlementshandler.FormatQuotaError(qe)
		body["code"] = string(base.ErrorCodeQuotaExceeded)

		return h.WriteJSON(writer, http.StatusPaymentRequired, body)
	case errors.Is(err, ErrOrgNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrRegionNotFound):
		return h.WriteErrorErr(writer, request, http.StatusNotFound, base.ErrorCodeNotFound, "Private region not found", err)
	case errors.Is(err, ErrAgentNotFound):
		return h.WriteErrorErr(writer, request, http.StatusNotFound, base.ErrorCodeNotFound, "Agent not found", err)
	case errors.Is(err, ErrRegionExists):
		return h.WriteErrorErr(writer, request, http.StatusConflict, base.ErrorCodeConflict, err.Error(), err)
	case errors.Is(err, ErrRegionHasAgents):
		return h.WriteErrorErr(writer, request, http.StatusConflict, base.ErrorCodeConflict, err.Error(), err)
	case errors.Is(err, regions.ErrInvalidPrivateRegionSlug), errors.Is(err, ErrInvalidExpiresIn):
		return h.WriteErrorErr(writer, request, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
