// Package jobs provides HTTP handlers for job operations.
package jobs

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
)

const responseKeyData = "data"

// Handler provides HTTP endpoints for job operations.
type Handler struct {
	jobSvc jobsvc.Service
}

// NewHandler creates a new job handler.
func NewHandler(jobSvc jobsvc.Service) *Handler {
	return &Handler{
		jobSvc: jobSvc,
	}
}

// CreateJob creates a new job.
// POST /api/v1/orgs/:org/jobs.
func (h *Handler) CreateJob(writer http.ResponseWriter, req bunrouter.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.writeError(writer, http.StatusNotFound, "ORGANIZATION_NOT_FOUND", "organization not found")
	}

	orgUID := org.UID

	var body struct {
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}

	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body")
	}

	job, err := h.jobSvc.CreateJob(req.Context(), orgUID, body.Type, body.Config, nil)
	if err != nil {
		return h.writeInternalError(writer, err)
	}

	return h.writeJSON(writer, http.StatusCreated, map[string]interface{}{
		responseKeyData: job,
	})
}

// GetJob retrieves a job by UID, scoped to the URL's organization.
// GET /api/v1/orgs/:org/jobs/:uid.
func (h *Handler) GetJob(writer http.ResponseWriter, req bunrouter.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.writeError(writer, http.StatusNotFound, "ORGANIZATION_NOT_FOUND", "organization not found")
	}

	uid := req.Param("uid")

	job, ok := h.jobInOrg(req.Context(), org.UID, uid)
	if !ok {
		return h.writeError(writer, http.StatusNotFound, "NOT_FOUND", "Job not found: "+uid)
	}

	return h.writeJSON(writer, http.StatusOK, map[string]interface{}{
		responseKeyData: job,
	})
}

// jobInOrg fetches a job and verifies it belongs to the given organization.
// Global jobs (nil OrganizationUID — reaper, startup, …) are not exposed on
// org-scoped routes. A job that exists but belongs to another org reports
// not-found rather than forbidden, so probing UIDs across orgs leaks nothing.
func (h *Handler) jobInOrg(ctx context.Context, orgUID, uid string) (*models.Job, bool) {
	job, err := h.jobSvc.GetJob(ctx, uid)
	if err != nil || job.OrganizationUID == nil || *job.OrganizationUID != orgUID {
		return nil, false
	}

	return job, true
}

// ListJobs lists jobs with optional filtering.
// GET /api/v1/orgs/:org/jobs.
func (h *Handler) ListJobs(writer http.ResponseWriter, req bunrouter.Request) error {
	// The :org URL segment is a SLUG that the org middleware has already
	// resolved; filtering on the raw param (as this once did) matches no rows.
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.writeError(writer, http.StatusNotFound, "ORGANIZATION_NOT_FOUND", "organization not found")
	}

	orgUID := org.UID

	opts := jobsvc.ListJobsOptions{
		Type:   req.URL.Query().Get("type"),
		Status: req.URL.Query().Get("status"),
	}

	jobs, err := h.jobSvc.ListJobs(req.Context(), orgUID, opts)
	if err != nil {
		return h.writeInternalError(writer, err)
	}

	return h.writeJSON(writer, http.StatusOK, map[string]interface{}{
		responseKeyData: jobs,
	})
}

// CancelJob cancels a pending job, scoped to the URL's organization. The
// ownership check runs BEFORE the cancel so a member of one org can never
// cancel another org's job by UID (org ownership is immutable, so
// check-then-cancel cannot race).
// DELETE /api/v1/orgs/:org/jobs/:uid.
func (h *Handler) CancelJob(writer http.ResponseWriter, req bunrouter.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.writeError(writer, http.StatusNotFound, "ORGANIZATION_NOT_FOUND", "organization not found")
	}

	uid := req.Param("uid")

	if _, ok := h.jobInOrg(req.Context(), org.UID, uid); !ok {
		return h.writeError(writer, http.StatusNotFound, "NOT_FOUND", "Job not found: "+uid)
	}

	if err := h.jobSvc.CancelJob(req.Context(), uid); err != nil {
		return h.writeError(writer, http.StatusNotFound, "NOT_FOUND", "Job not found: "+uid)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// writeJSON writes a JSON response.
func (h *Handler) writeJSON(w http.ResponseWriter, status int, data interface{}) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	return json.NewEncoder(w).Encode(data)
}

// writeError writes a JSON error response.
func (h *Handler) writeError(w http.ResponseWriter, status int, code, message string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	return json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"title":   message,
			"detail":  message,
			"status":  status,
			"message": message,
		},
	})
}

// writeInternalError writes a 500 error response.
func (h *Handler) writeInternalError(w http.ResponseWriter, err error) error {
	return h.writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
}

// RegisterRoutes registers job routes on the given router group.
func (h *Handler) RegisterRoutes(group *bunrouter.Group) {
	jobs := group.NewGroup("/orgs/:org/jobs")
	jobs.POST("", h.CreateJob)
	jobs.GET("", h.ListJobs)
	jobs.GET("/:uid", h.GetJob)
	jobs.DELETE("/:uid", h.CancelJob)
}
