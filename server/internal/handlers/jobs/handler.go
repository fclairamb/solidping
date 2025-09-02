// Package jobs provides HTTP handlers for job operations.
package jobs

import (
	"encoding/json"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

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
	orgUID := req.Param("org")

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
		"data": job,
	})
}

// GetJob retrieves a job by UID.
// GET /api/v1/orgs/:org/jobs/:uid.
func (h *Handler) GetJob(writer http.ResponseWriter, req bunrouter.Request) error {
	uid := req.Param("uid")

	job, err := h.jobSvc.GetJob(req.Context(), uid)
	if err != nil {
		return h.writeError(writer, http.StatusNotFound, "NOT_FOUND", "Job not found: "+uid)
	}

	return h.writeJSON(writer, http.StatusOK, map[string]interface{}{
		"data": job,
	})
}

// ListJobs lists jobs with optional filtering.
// GET /api/v1/orgs/:org/jobs.
func (h *Handler) ListJobs(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")

	opts := jobsvc.ListJobsOptions{
		Type:   req.URL.Query().Get("type"),
		Status: req.URL.Query().Get("status"),
	}

	jobs, err := h.jobSvc.ListJobs(req.Context(), orgUID, opts)
	if err != nil {
		return h.writeInternalError(writer, err)
	}

	return h.writeJSON(writer, http.StatusOK, map[string]interface{}{
		"data": jobs,
	})
}

// CancelJob cancels a pending job.
// DELETE /api/v1/orgs/:org/jobs/:uid.
func (h *Handler) CancelJob(writer http.ResponseWriter, req bunrouter.Request) error {
	uid := req.Param("uid")

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
