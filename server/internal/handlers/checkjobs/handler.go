package checkjobs

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

const (
	responseKeyData = "data"
	defaultPageSize = 50
	maxPageSize     = 200
)

// Handler exposes read-only check-job and stats endpoints.
type Handler struct {
	base.HandlerBase
	svc Service
}

// NewHandler creates a new check-jobs handler.
func NewHandler(svc Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
	}
}

// orgUIDFromContext returns the resolved organization UID placed in context by
// RequireOrgAccess. The :org URL param is a slug, never the UID.
func orgUIDFromContext(req *http.Request) (string, bool) {
	org, ok := middleware.GetOrganizationFromContext(req.Context())
	if !ok || org == nil {
		return "", false
	}

	return org.UID, true
}

func (h *Handler) parseListOptions(req *http.Request) ListOptions {
	query := req.URL.Query()

	limit, err := base.ParsePageLimit(query, defaultPageSize, maxPageSize)
	if err != nil {
		limit = defaultPageSize
	}

	opts := ListOptions{Limit: limit}

	if raw := query.Get("offset"); raw != "" {
		if offset, err := strconv.Atoi(raw); err == nil && offset > 0 {
			opts.Offset = offset
		}
	}

	return opts
}

// ListCheckJobs lists check jobs for the org (org-scoped).
// GET /api/v1/orgs/:org/check-jobs.
func (h *Handler) ListCheckJobs(writer http.ResponseWriter, req *http.Request) error {
	orgUID, ok := orgUIDFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Organization context missing")
	}

	views, err := h.svc.ListCheckJobs(req.Context(), orgUID, h.parseListOptions(req))
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{responseKeyData: views})
}

// GetCheckJob returns a single check job (org-scoped).
// GET /api/v1/orgs/:org/check-jobs/:uid.
func (h *Handler) GetCheckJob(writer http.ResponseWriter, req *http.Request) error {
	orgUID, ok := orgUIDFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Organization context missing")
	}

	view, err := h.svc.GetCheckJob(req.Context(), orgUID, httpx.Param(req, "uid"))
	if err != nil {
		if errors.Is(err, ErrCheckJobNotFound) {
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Check job not found")
		}

		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{responseKeyData: view})
}

// Stats returns aggregate counts for the org (org-scoped).
// GET /api/v1/orgs/:org/jobs/stats.
func (h *Handler) Stats(writer http.ResponseWriter, req *http.Request) error {
	orgUID, ok := orgUIDFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Organization context missing")
	}

	stats, err := h.svc.Stats(req.Context(), orgUID)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{responseKeyData: stats})
}

// ListSystemCheckJobs lists check jobs across all orgs (super-admin).
// GET /api/v1/system/check-jobs.
func (h *Handler) ListSystemCheckJobs(writer http.ResponseWriter, req *http.Request) error {
	views, err := h.svc.ListCheckJobs(req.Context(), "", h.parseListOptions(req))
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{responseKeyData: views})
}

// GetSystemCheckJob returns a single check job across all orgs (super-admin).
// GET /api/v1/system/check-jobs/:uid.
func (h *Handler) GetSystemCheckJob(writer http.ResponseWriter, req *http.Request) error {
	view, err := h.svc.GetCheckJob(req.Context(), "", httpx.Param(req, "uid"))
	if err != nil {
		if errors.Is(err, ErrCheckJobNotFound) {
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Check job not found")
		}

		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{responseKeyData: view})
}

// SystemStats returns aggregate counts across all orgs (super-admin).
// GET /api/v1/system/jobs/stats.
func (h *Handler) SystemStats(writer http.ResponseWriter, req *http.Request) error {
	stats, err := h.svc.Stats(req.Context(), "")
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{responseKeyData: stats})
}
