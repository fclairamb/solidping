package labels

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

const (
	defaultLimit = 50
	maxLimit     = 200
	jsonDataKey  = "data"
)

// Handler exposes the label autocomplete API.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler builds a Handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
	}
}

// ListLabels handles GET /api/v1/orgs/:org/labels.
func (h *Handler) ListLabels(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	query := req.URL.Query()

	key := query.Get("key")
	prefix := query.Get("q")

	limit := defaultLimit
	if limitParam := query.Get("limit"); limitParam != "" {
		parsed, err := strconv.Atoi(limitParam)
		if err != nil {
			return h.WriteErrorErr(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Invalid limit parameter", err)
		}

		if parsed < 1 {
			return h.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Limit must be at least 1")
		}

		limit = parsed
	}

	if limit > maxLimit {
		limit = maxLimit
	}

	suggestions, err := h.svc.ListLabels(req.Context(), orgSlug, key, prefix, limit)
	if err != nil {
		return h.handleListError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{jsonDataKey: suggestions})
}

func (h *Handler) handleListError(writer http.ResponseWriter, err error) error {
	if errors.Is(err, ErrOrganizationNotFound) {
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found", err)
	}

	return h.WriteInternalError(writer, err)
}
