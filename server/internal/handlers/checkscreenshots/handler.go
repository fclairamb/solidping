package checkscreenshots

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Handler serves the check screenshot endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler builds the handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: svc}
}

// ListResponse wraps the listing, per the API convention.
type ListResponse struct {
	Data []attachments.CheckScreenshot `json:"data"`
}

// List handles GET /api/v1/orgs/:org/checks/:checkUid/screenshots?limit=N.
func (h *Handler) List(writer http.ResponseWriter, req *http.Request) error {
	limit := DefaultLimit

	if raw := req.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return h.WriteValidationError(writer, "Invalid limit", []base.ValidationErrorField{
				{Name: "limit", Message: "must be a positive integer"},
			})
		}

		limit = parsed
	}

	shots, err := h.svc.ListScreenshots(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"), limit)
	if err != nil {
		return h.writeError(writer, req, err)
	}

	if shots == nil {
		shots = []attachments.CheckScreenshot{}
	}

	return h.WriteJSON(writer, http.StatusOK, ListResponse{Data: shots})
}

// Capture handles POST /api/v1/orgs/:org/checks/:checkUid/screenshots/capture
// ("Capture now"). 202: the run is scheduled, its capture arrives in the
// listing when it completes.
func (h *Handler) Capture(writer http.ResponseWriter, req *http.Request) error {
	resp, err := h.svc.CaptureNow(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"))
	if err != nil {
		return h.writeError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusAccepted, resp)
}

func (h *Handler) writeError(writer http.ResponseWriter, req *http.Request, err error) error {
	var limited *RateLimitedError

	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found")
	case errors.Is(err, ErrNotCapturable):
		return h.WriteErrorErr(writer, req, http.StatusBadRequest, base.ErrorCodeValidationError,
			"This check type cannot capture a screenshot", err)
	case errors.Is(err, ErrNoScheduledJob):
		return h.WriteErrorErr(writer, req, http.StatusConflict, base.ErrorCodeConflict,
			"The check has no scheduled run", err)
	case errors.As(err, &limited):
		seconds := int(limited.RetryAfter.Round(time.Second).Seconds())
		if seconds < 1 {
			seconds = 1
		}

		writer.Header().Set("Retry-After", strconv.Itoa(seconds))

		return h.WriteErrorErr(writer, req, http.StatusTooManyRequests, base.ErrorCodeRateLimited,
			"Too many captures requested", err)
	default:
		return h.WriteInternalError(writer, req, err)
	}
}
