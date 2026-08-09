package discovery

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/discovery/scantypes"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
)

const (
	// ErrorCodeDiscoveryAlreadyRunning is returned when a scan is already in progress for the org.
	ErrorCodeDiscoveryAlreadyRunning base.ErrorCode = "DISCOVERY_ALREADY_RUNNING"
	// keyData is the standard JSON response wrapper key.
	keyData = "data"
)

// isAdmin returns true if the request context has an admin or super admin role.
func isAdmin(req *http.Request) bool {
	claims, ok := mw.GetClaimsFromContext(req.Context())
	if !ok || claims == nil {
		return false
	}

	return claims.HasOrgRole(models.MemberRoleAdmin)
}

// Handler provides HTTP handlers for network discovery endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new discovery handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
	}
}

// RegisterRoutes registers discovery routes on the given router group.
// The group must already point to /orgs/:org/discovery and have RequireAuth + RequireOrgAccess applied.
func (h *Handler) RegisterRoutes(group *httpx.Group) {
	group.GET("/types", h.ListTypes)
	group.POST("/scans", h.StartScan)
	group.GET("/scans", h.ListScans)
	group.GET("/scans/:jobUid", h.GetScan)
	group.POST("/scans/:jobUid/cancel", h.CancelScan)
	group.GET("/checks", h.ListChecks)
	group.POST("/checks/promote", h.PromoteChecks)
	group.DELETE("/checks/:uid", h.DismissCheck)
	group.DELETE("/checks", h.DismissGroup)
}

// discoveryTypeDescriptor is the public capability descriptor for one registered
// discovery type, driving the frontend type picker.
type discoveryTypeDescriptor struct {
	Type   string `json:"type"`
	Source string `json:"source"`
}

// ListTypes returns the registered discovery types so the frontend can build its
// type picker without hard-coding the set.
func (h *Handler) ListTypes(writer http.ResponseWriter, _ *http.Request) error {
	defs := scantypes.List()
	out := make([]discoveryTypeDescriptor, 0, len(defs))

	for _, d := range defs {
		out = append(out, discoveryTypeDescriptor{Type: d.Type(), Source: string(d.Source())})
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: out})
}

// StartScanRequest is the generic body for POST /discovery/scans.
type StartScanRequest struct {
	Type       string          `json:"type"`
	Parameters json.RawMessage `json:"parameters"`
}

// StartScan handles POST /orgs/:org/discovery/scans. Admin only. The body is a
// generic {type, parameters}; the discovery-type registry validates parameters
// and resolves the job to enqueue. The response carries the scan job plus its
// ScanProgress.
func (h *Handler) StartScan(writer http.ResponseWriter, req *http.Request) error {
	if !isAdmin(req) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	var body StartScanRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "invalid request body")
	}

	if body.Type == "" {
		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, "type is required")
	}

	job, err := h.svc.StartScan(req.Context(), org.UID, body.Type, body.Parameters)
	if err != nil {
		return h.writeScanError(writer, err)
	}

	progress, progErr := h.svc.GetScanProgress(req.Context(), org.UID, job.UID)
	if progErr != nil {
		return h.WriteInternalError(writer, progErr)
	}

	return h.WriteJSON(writer, http.StatusCreated, map[string]any{keyData: job, "progress": progress})
}

// writeScanError maps a StartScan error (coded DiscoveryError, already-running,
// or unexpected) to the proper HTTP response.
func (h *Handler) writeScanError(writer http.ResponseWriter, err error) error {
	if errors.Is(err, ErrAlreadyRunning) {
		return h.WriteError(
			writer, http.StatusConflict, ErrorCodeDiscoveryAlreadyRunning,
			"a discovery scan is already running for this organization",
		)
	}

	var de *scantypes.DiscoveryError
	if errors.As(err, &de) {
		status := statusForDiscoveryCode(de.Code)

		return h.WriteError(writer, status, base.ErrorCode(de.Code), de.Message)
	}

	return h.WriteInternalError(writer, err)
}

// statusForDiscoveryCode maps a discovery error code to an HTTP status.
func statusForDiscoveryCode(code string) int {
	switch code {
	case scantypes.CodeUnknownType:
		return http.StatusBadRequest
	case scantypes.CodeAlreadyRunning:
		return http.StatusConflict
	case scantypes.CodeFreeboxNotGranted:
		return http.StatusConflict
	case scantypes.CodeKubernetesClusterNotFound:
		return http.StatusNotFound
	default: // CodeInvalidParameters, DISCOVERY_RANGE_TOO_LARGE
		return http.StatusUnprocessableEntity
	}
}

// ListScans handles GET /orgs/:org/discovery/scans.
func (h *Handler) ListScans(writer http.ResponseWriter, req *http.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	jobs, err := h.svc.jobSvc.ListJobs(req.Context(), org.UID, jobsvc.ListJobsOptions{
		Types: scanJobTypes(),
	})
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	// Child network_discovery jobs (those carrying a parentJobUid) are an
	// implementation detail of a fan-out scan — the scan itself is the plan job.
	// Filter them out so the list shows one entry per user-initiated scan.
	scans := make([]*models.Job, 0, len(jobs))

	for _, job := range jobs {
		if job.Type == string(jobdef.JobTypeNetworkDiscovery) && job.Config != nil {
			if parent, ok := job.Config["parentJobUid"]; ok {
				if pStr, _ := parent.(string); pStr != "" {
					continue
				}
			}
		}

		scans = append(scans, job)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: scans})
}

// GetScan handles GET /orgs/:org/discovery/scans/:jobUid. It returns the scan's
// job plus a uniform progress block (chunk counts, derived status, group/check
// counts) for every scan type.
func (h *Handler) GetScan(writer http.ResponseWriter, req *http.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	jobUID := httpx.Param(req, "jobUid")

	job, err := h.svc.jobSvc.GetJob(req.Context(), jobUID)
	if err != nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "scan not found")
	}

	progress, progErr := h.svc.GetScanProgress(req.Context(), org.UID, jobUID)
	if progErr != nil {
		return h.WriteInternalError(writer, progErr)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: job, "progress": progress})
}

// CancelScan handles POST /orgs/:org/discovery/scans/:jobUid/cancel.
// Admin only. Cancels the plan job (if pending) and all pending child chunks.
func (h *Handler) CancelScan(writer http.ResponseWriter, req *http.Request) error {
	if !isAdmin(req) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	jobUID := httpx.Param(req, "jobUid")

	err := h.svc.CancelScan(req.Context(), org.UID, jobUID)
	if errors.Is(err, ErrScanNotFound) {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "scan not found")
	} else if err != nil {
		return h.WriteInternalError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// ListChecks handles GET /orgs/:org/discovery/checks.
func (h *Handler) ListChecks(writer http.ResponseWriter, req *http.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	queryParams := req.URL.Query()
	opts := ListChecksOptions{
		JobUID: queryParams.Get("jobUid"),
		Group:  queryParams.Get("group"),
	}

	if promoted := queryParams.Get("promoted"); promoted != "" {
		b := promoted == "true"
		opts.Promoted = &b
	}

	// ?source=lan,freebox — singular param, comma-separated per the API
	// convention. Blank segments are skipped.
	if source := queryParams.Get("source"); source != "" {
		for _, part := range strings.Split(source, ",") {
			if part = strings.TrimSpace(part); part != "" {
				opts.Sources = append(opts.Sources, models.DiscoverySource(part))
			}
		}
	}

	rows, err := h.svc.ListDiscoveredChecks(req.Context(), org.UID, &opts)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: rows})
}

// PromoteChecks handles POST /orgs/:org/discovery/checks/promote. Admin only.
func (h *Handler) PromoteChecks(writer http.ResponseWriter, req *http.Request) error {
	if !isAdmin(req) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	var promoteReq PromoteRequest
	if err := json.NewDecoder(req.Body).Decode(&promoteReq); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "invalid request body")
	}

	if len(promoteReq.UIDs) == 0 {
		return h.WriteError(
			writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, "at least one uid is required",
		)
	}

	org2, err := h.svc.dbSvc.GetOrganization(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	checkResps, err := h.svc.PromoteChecks(req.Context(), org.UID, org2.Slug, promoteReq)

	switch {
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "discovered check not found")
	case errors.Is(err, ErrAlreadyPromoted):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict, "discovered check already promoted")
	case err != nil:
		return h.WriteInternalErrorR(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, map[string]any{keyData: checkResps})
}

// DismissCheck handles DELETE /orgs/:org/discovery/checks/:uid. Admin only.
func (h *Handler) DismissCheck(writer http.ResponseWriter, req *http.Request) error {
	if !isAdmin(req) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	uid := httpx.Param(req, "uid")

	err := h.svc.SoftDeleteCheck(req.Context(), org.UID, uid)
	if errors.Is(err, ErrCheckNotFound) {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "discovered check not found")
	} else if err != nil {
		return h.WriteInternalError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// DismissGroup handles DELETE /orgs/:org/discovery/checks?jobUid=&group=.
// Admin only. Soft-deletes every check in a group.
func (h *Handler) DismissGroup(writer http.ResponseWriter, req *http.Request) error {
	if !isAdmin(req) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	queryParams := req.URL.Query()

	group := queryParams.Get("group")
	if group == "" {
		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, "group is required")
	}

	_, err := h.svc.SoftDeleteGroup(req.Context(), org.UID, queryParams.Get("jobUid"), group)
	if errors.Is(err, ErrCheckNotFound) {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "no checks found for group")
	} else if err != nil {
		return h.WriteInternalError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}
