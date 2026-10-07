package checkruns

import (
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Handler serves the multi-step run endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler builds the handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: svc}
}

// ReportsResponse wraps the report listing, per the API convention.
type ReportsResponse struct {
	Data []attachments.Response `json:"data"`
}

// GetRun handles GET /api/v1/orgs/:org/checks/:checkUid/run.
func (h *Handler) GetRun(writer http.ResponseWriter, req *http.Request) error {
	run, err := h.svc.GetRun(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"))
	if err != nil {
		return h.writeError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, run)
}

// CancelRun handles DELETE /api/v1/orgs/:org/checks/:checkUid/run.
func (h *Handler) CancelRun(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.CancelRun(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid")); err != nil {
		return h.writeError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// ListCrawlReports handles GET /api/v1/orgs/:org/checks/:checkUid/crawl-reports.
func (h *Handler) ListCrawlReports(writer http.ResponseWriter, req *http.Request) error {
	reports, err := h.svc.ListCrawlReports(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "checkUid"))
	if err != nil {
		return h.writeError(writer, req, err)
	}

	if reports == nil {
		reports = []attachments.Response{}
	}

	return h.WriteJSON(writer, http.StatusOK, ReportsResponse{Data: reports})
}

func (h *Handler) writeError(writer http.ResponseWriter, req *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found")
	case errors.Is(err, ErrNotMultiStep):
		return h.WriteErrorErr(writer, req, http.StatusBadRequest, base.ErrorCodeValidationError,
			"This check type does not run in steps", err)
	default:
		return h.WriteInternalError(writer, req, err)
	}
}
