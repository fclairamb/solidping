// Package results provides handlers for results listing endpoints.
package results

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler handles HTTP requests for results.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new results handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListResults handles GET /api/v1/orgs/:org/results.
func (h *Handler) ListResults(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	// Parse query parameters
	opts := ListResultsOptions{}
	query := req.URL.Query()

	// checkUid - comma-separated check UIDs or slugs
	if checkParam := query.Get("checkUid"); checkParam != "" {
		opts.Checks = strings.Split(checkParam, ",")
	}

	// checkType - comma-separated check types
	if checkTypeParam := query.Get("checkType"); checkTypeParam != "" {
		opts.CheckTypes = strings.Split(checkTypeParam, ",")
	}

	// status - comma-separated statuses (up, down, unknown)
	if statusParam := query.Get("status"); statusParam != "" {
		opts.Statuses = strings.Split(statusParam, ",")
	}

	// region - comma-separated regions
	if regionParam := query.Get("region"); regionParam != "" {
		opts.Regions = strings.Split(regionParam, ",")
	}

	// periodType - comma-separated period types
	if periodTypeParam := query.Get("periodType"); periodTypeParam != "" {
		opts.PeriodTypes = strings.Split(periodTypeParam, ",")
	}

	// periodStartAfter - RFC3339 timestamp
	periodStart, parseErr := parseRFC3339(query.Get("periodStartAfter"))
	if parseErr != nil {
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid periodStartAfter: must be RFC3339")
	}
	opts.PeriodStartAfter = periodStart

	// periodEndBefore - RFC3339 timestamp
	periodEnd, parseErr := parseRFC3339(query.Get("periodEndBefore"))
	if parseErr != nil {
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid periodEndBefore: must be RFC3339")
	}
	opts.PeriodEndBefore = periodEnd

	// cursor for pagination
	opts.Cursor = query.Get("cursor")

	// size (default 100, max 1000)
	if sizeParam := query.Get("size"); sizeParam != "" {
		size, err := strconv.Atoi(sizeParam)
		if err != nil {
			return h.WriteErrorErr(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid size parameter", err)
		}
		if size < 1 {
			return h.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Size must be at least 1")
		}
		if size > 1000 {
			size = 1000
		}
		opts.Size = size
	}
	if opts.Size == 0 {
		opts.Size = 100
	}

	// with - comma-separated optional fields
	if withParam := query.Get("with"); withParam != "" {
		opts.With = strings.Split(withParam, ",")
	}

	// Call service
	results, err := h.svc.ListResults(req.Context(), orgSlug, &opts)
	if err != nil {
		return h.handleListError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, results)
}

// parseRFC3339 parses an RFC3339 timestamp string, returning nil for empty strings.
func parseRFC3339(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil //nolint:nilnil // nil,nil is intentional for absent params
	}

	parsedTime, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}

	return &parsedTime, nil
}

func (h *Handler) handleListError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrInvalidCursor):
		return h.WriteErrorErr(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid cursor", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// GetResult handles GET /api/v1/orgs/:org/checks/:check/results/:uid.
// Returns the result row, falling back to the smallest covering aggregation
// when the raw row has been rolled up.
func (h *Handler) GetResult(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkIdent := req.Param("check")
	resultUID := req.Param("uid")

	resp, err := h.svc.GetResult(req.Context(), orgSlug, checkIdent, resultUID)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrganizationNotFound):
			return h.WriteErrorErr(
				writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		case errors.Is(err, ErrCheckNotFound):
			return h.WriteErrorErr(
				writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
		case errors.Is(err, ErrResultNotFound):
			return h.WriteErrorErr(
				writer, http.StatusNotFound, base.ErrorCodeResultNotFound, "Result not found", err)
		default:
			return h.WriteInternalError(writer, err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}
