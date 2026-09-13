package orgparams

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// Handler serves /api/v1/orgs/:org/parameters. Every route is org-admin only,
// enforced structurally by the route group in app/server.go.
type Handler struct {
	base.HandlerBase

	svc *Service
}

// NewHandler constructs a Handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: svc}
}

// List returns every org-managed parameter. Secret values are never included.
func (h *Handler) List(writer http.ResponseWriter, req *http.Request) error {
	res, err := h.svc.List(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, res)
}

// Get returns one parameter; a secret one comes back without its value.
func (h *Handler) Get(writer http.ResponseWriter, req *http.Request) error {
	res, err := h.svc.Get(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "key"))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, res)
}

// Set creates or rotates a parameter.
func (h *Handler) Set(writer http.ResponseWriter, req *http.Request) error {
	var body SetRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Body must be a JSON object with a string \"value\"",
		)
	}

	res, err := h.svc.Set(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "key"), &body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, res)
}

// Delete removes a parameter.
func (h *Handler) Delete(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.Delete(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "key")); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

func (h *Handler) handleError(writer http.ResponseWriter, req *http.Request, err error) error {
	switch {
	case errors.Is(err, paramkeys.ErrReservedKey):
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"That parameter key is reserved for SolidPing's own configuration",
		)
	case errors.Is(err, paramkeys.ErrInvalidKey):
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Parameter keys must match "+paramkeys.KeyPattern.String(),
		)
	case errors.Is(err, ErrValueTooLarge):
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Parameter value is too large",
		)
	case errors.Is(err, ErrOrgNotFound):
		return h.WriteError(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found",
		)
	case errors.Is(err, ErrNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Parameter not found")
	default:
		return h.WriteInternalError(writer, req, err)
	}
}
