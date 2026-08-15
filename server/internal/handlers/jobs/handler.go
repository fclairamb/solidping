// Package jobs provides HTTP handlers for job operations.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
)

const (
	responseKeyData = "data"

	errCodeValidation = "VALIDATION_ERROR"
	errCodeForbidden  = "FORBIDDEN"
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
//
// This is the PUBLIC create surface and is deliberately narrow: the route
// requires org admin (see RegisterRoutes) and the job type must be on
// jobdef's allowlist. Without the allowlist any org member could send
// arbitrary email through the deployment's own SMTP sender ("email") or make
// the server issue arbitrary outbound HTTP requests ("webhook" — SSRF against
// link-local metadata and cluster-internal services). Internal enqueue paths
// call jobsvc.CreateJob directly and are intentionally NOT subject to any of
// this (spec 2026-08-15-04).
func (h *Handler) CreateJob(writer http.ResponseWriter, req *http.Request) error {
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
		return h.writeError(writer, http.StatusBadRequest, errCodeValidation, "Invalid request body")
	}

	if denial := h.checkCreatable(jobdef.JobType(body.Type), body.Config); denial != nil {
		return h.writeError(writer, denial.status, denial.code, denial.message)
	}

	job, err := h.jobSvc.CreateJob(req.Context(), orgUID, body.Type, body.Config, nil)
	if err != nil {
		if status, code, ok := classifyCreateError(err); ok {
			return h.writeError(writer, status, code, err.Error())
		}

		return h.writeInternalError(writer, err)
	}

	return h.writeJSON(writer, http.StatusCreated, map[string]interface{}{
		responseKeyData: job,
	})
}

// createDenial is a refusal to create a job, carrying the HTTP status and error
// code the caller must see.
type createDenial struct {
	status  int
	code    string
	message string
}

// checkCreatable applies the public-create rules to a request: the type must
// exist in the registry (unknown → 400, never a 500 at execution time), must be
// on the public allowlist (→ 403), and its config must be well-formed for that
// type (→ 400). The config check is a dry run of the definition's factory, so
// a bad payload is refused at request time instead of failing asynchronously.
func (h *Handler) checkCreatable(jobType jobdef.JobType, config json.RawMessage) *createDenial {
	definition, known := jobtypes.GetJobDefinition(jobType)
	if !known {
		return &createDenial{
			status:  http.StatusBadRequest,
			code:    errCodeValidation,
			message: "Unknown job type " + strconv.Quote(string(jobType)),
		}
	}

	if !jobdef.IsPubliclyCreatable(jobType) {
		return &createDenial{
			status:  http.StatusForbidden,
			code:    errCodeForbidden,
			message: "Job type " + strconv.Quote(string(jobType)) + " cannot be created through this endpoint",
		}
	}

	// An absent config is the type's own business (sleep defaults it), but
	// json.Unmarshal rejects an empty payload outright, so normalize it.
	if len(config) == 0 {
		config = json.RawMessage("{}")
	}

	if _, err := definition.CreateJobRun(config); err != nil {
		return &createDenial{
			status:  http.StatusBadRequest,
			code:    errCodeValidation,
			message: "Invalid job config: " + err.Error(),
		}
	}

	return nil
}

// classifyCreateError maps a jobsvc.CreateJob failure to a client error where
// one is warranted. Matching is on sentinels and error types via
// errors.Is/errors.As — never on message text — so a reworded error can never
// silently turn a 400 into a 500 (or the reverse). Anything unrecognized is a
// genuine infrastructure failure and stays a 500.
func classifyCreateError(err error) (int, string, bool) {
	var (
		syntaxErr    *json.SyntaxError
		unmarshalErr *json.UnmarshalTypeError
	)

	switch {
	case errors.Is(err, jobsvc.ErrInvalidJobConfig),
		errors.As(err, &syntaxErr),
		errors.As(err, &unmarshalErr),
		errors.Is(err, jobtypes.ErrEmailNoRecipients),
		errors.Is(err, jobtypes.ErrEmailNoSubject),
		errors.Is(err, jobtypes.ErrEmailNoContent),
		errors.Is(err, jobtypes.ErrEmailContentConflict):
		return http.StatusBadRequest, errCodeValidation, true
	default:
		return 0, "", false
	}
}

// GetJob retrieves a job by UID, scoped to the URL's organization.
// GET /api/v1/orgs/:org/jobs/:uid.
func (h *Handler) GetJob(writer http.ResponseWriter, req *http.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.writeError(writer, http.StatusNotFound, "ORGANIZATION_NOT_FOUND", "organization not found")
	}

	uid := httpx.Param(req, "uid")

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
func (h *Handler) ListJobs(writer http.ResponseWriter, req *http.Request) error {
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
func (h *Handler) CancelJob(writer http.ResponseWriter, req *http.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.writeError(writer, http.StatusNotFound, "ORGANIZATION_NOT_FOUND", "organization not found")
	}

	uid := httpx.Param(req, "uid")

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

// RouteMiddleware carries the middleware chains the job routes are wired with.
// It exists so there is exactly ONE definition of which guard protects which
// job route — server.go supplies the production chains and the tests supply the
// same ones, instead of a route table that could drift from what is tested.
type RouteMiddleware struct {
	// Shared guards every job route (auth + org membership).
	Shared []httpx.Middleware
	// CreateOnly guards POST on top of Shared (org admin). Creating a job is
	// a privileged write; listing, reading and canceling stay open to members.
	CreateOnly []httpx.Middleware
}

// RegisterRoutes registers the job routes on the given router group. This is
// the single source of truth for the job route table and its guards; see
// RouteMiddleware.
func (h *Handler) RegisterRoutes(group *httpx.Group, middlewares RouteMiddleware) {
	jobs := group.NewGroup("/orgs/:org/jobs").Use(middlewares.Shared...)
	jobs.GET("", h.ListJobs)
	jobs.GET("/:uid", h.GetJob)
	jobs.DELETE("/:uid", h.CancelJob)

	jobs.Use(middlewares.CreateOnly...).POST("", h.CreateJob)
}
