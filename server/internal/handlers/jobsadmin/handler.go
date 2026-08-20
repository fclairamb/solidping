package jobsadmin

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

// Handler exposes admin/super-admin read-only background-job endpoints.
type Handler struct {
	base.HandlerBase
	svc Service
}

// NewHandler creates a new jobsadmin handler.
func NewHandler(svc Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
	}
}

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

	opts := ListOptions{
		Status: query.Get("status"),
		Type:   query.Get("type"),
		Limit:  limit,
	}

	if raw := query.Get("offset"); raw != "" {
		if offset, parseErr := strconv.Atoi(raw); parseErr == nil && offset > 0 {
			opts.Offset = offset
		}
	}

	return opts
}

// ListSystemJobs lists jobs across all orgs incl. system jobs (super-admin).
// GET /api/v1/system/jobs.
func (h *Handler) ListSystemJobs(writer http.ResponseWriter, req *http.Request) error {
	jobs, err := h.svc.ListJobs(req.Context(), "", h.parseListOptions(req))
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{responseKeyData: jobs})
}

// GetSystemJob returns a single job across all orgs (super-admin).
// GET /api/v1/system/jobs/:uid.
func (h *Handler) GetSystemJob(writer http.ResponseWriter, req *http.Request) error {
	return h.getJob(writer, req, "")
}

// GetSystemJobChain returns the retry chain for a job across all orgs.
// GET /api/v1/system/jobs/:uid/chain.
func (h *Handler) GetSystemJobChain(writer http.ResponseWriter, req *http.Request) error {
	return h.getChain(writer, req, "")
}

// ListOrgJobs lists jobs for the org, admin-gated.
// GET /api/v1/orgs/:org/jobs/admin.
func (h *Handler) ListOrgJobs(writer http.ResponseWriter, req *http.Request) error {
	orgUID, ok := orgUIDFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Organization context missing")
	}

	jobs, err := h.svc.ListJobs(req.Context(), orgUID, h.parseListOptions(req))
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{responseKeyData: jobs})
}

// GetOrgJob returns a single org job, admin-gated.
// GET /api/v1/orgs/:org/jobs/:uid/detail.
func (h *Handler) GetOrgJob(writer http.ResponseWriter, req *http.Request) error {
	orgUID, ok := orgUIDFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Organization context missing")
	}

	return h.getJob(writer, req, orgUID)
}

// GetOrgJobChain returns the retry chain for an org job, admin-gated.
// GET /api/v1/orgs/:org/jobs/:uid/chain.
func (h *Handler) GetOrgJobChain(writer http.ResponseWriter, req *http.Request) error {
	orgUID, ok := orgUIDFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "Organization context missing")
	}

	return h.getChain(writer, req, orgUID)
}

func (h *Handler) getJob(writer http.ResponseWriter, req *http.Request, orgUID string) error {
	job, err := h.svc.GetJob(req.Context(), orgUID, httpx.Param(req, "uid"))
	if err != nil {
		if errors.Is(err, ErrJobNotFound) {
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Job not found")
		}

		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{responseKeyData: job})
}

func (h *Handler) getChain(writer http.ResponseWriter, req *http.Request, orgUID string) error {
	chain, err := h.svc.RetryChain(req.Context(), orgUID, httpx.Param(req, "uid"))
	if err != nil {
		if errors.Is(err, ErrJobNotFound) {
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Job not found")
		}

		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{responseKeyData: chain})
}
