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
	// ErrorCodeDiscoveryRegionUnavailable is returned when a row was found from a private region the org no longer has.
	ErrorCodeDiscoveryRegionUnavailable base.ErrorCode = "DISCOVERY_REGION_UNAVAILABLE"
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

// isAdminMember reports whether the caller holds an admin (or owner)
// membership row in the org, read from the database rather than the token.
func (h *Handler) isAdminMember(req *http.Request, orgUID string) bool {
	claims, ok := mw.GetClaimsFromContext(req.Context())
	if !ok || claims == nil || claims.UserUID == "" {
		return false
	}

	member, err := h.svc.dbSvc.GetMemberByUserAndOrg(req.Context(), claims.UserUID, orgUID)
	if err != nil || member == nil {
		return false
	}

	return member.Role.AtLeast(models.MemberRoleAdmin)
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
// discovery type, driving the frontend type picker. ServerAvailable false means
// only a private region's agents may run it: the scan needs a region.
type discoveryTypeDescriptor struct {
	Type            string `json:"type"`
	Source          string `json:"source"`
	ServerAvailable bool   `json:"serverAvailable"`
}

// ListTypes returns the discovery types the org may start so the frontend can
// build its type picker without hard-coding the set. Types the deployment
// refuses (DISCOVERY_NOT_AVAILABLE) are hidden, except `lan` for an org with a
// private region (flagged serverAvailable: false).
func (h *Handler) ListTypes(writer http.ResponseWriter, req *http.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	types, err := h.svc.TypesFor(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	out := make([]discoveryTypeDescriptor, 0, len(types))

	for _, t := range types {
		out = append(out, discoveryTypeDescriptor{
			Type: t.Definition.Type(), Source: string(t.Definition.Source()), ServerAvailable: t.ServerAvailable,
		})
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: out})
}

// StartScanRequest is the generic body for POST /discovery/scans. Region, only
// for `lan`, runs the scan from that private region's agents (spec
// 2026-10-22-04).
type StartScanRequest struct {
	Type       string          `json:"type"`
	Region     string          `json:"region,omitempty"`
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

	if body.Region != "" && body.Type != lanType {
		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError,
			"region is only supported by lan scans")
	}

	// A scan from a customer's agent needs an admin MEMBERSHIP ROW: claims.Role
	// outlives a demotion until the token expires, and a super admin without a
	// membership gets no bypass (spec 2026-10-22-04).
	if body.Region != "" && !h.isAdminMember(req, org.UID) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden,
			"an admin membership of the organization is required to scan from a private region")
	}

	job, err := h.svc.StartScan(req.Context(), org.UID, body.Type, body.Region, body.Parameters)
	if err != nil {
		return h.writeScanError(writer, req, err)
	}

	progress, progErr := h.svc.GetScanProgress(req.Context(), org.UID, job.UID)
	if progErr != nil {
		return h.WriteInternalError(writer, req, progErr)
	}

	return h.WriteJSON(writer, http.StatusCreated, map[string]any{keyData: job, "progress": progress})
}

// writeScanError maps a StartScan error (coded DiscoveryError, already-running,
// or unexpected) to the proper HTTP response.
func (h *Handler) writeScanError(writer http.ResponseWriter, request *http.Request, err error) error {
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

	return h.WriteInternalError(writer, request, err)
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
	case scantypes.CodeNotAvailable:
		return http.StatusForbidden
	case scantypes.CodeRegionUnavailable, scantypes.CodeQuotaExceeded:
		return http.StatusUnprocessableEntity
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
		return h.WriteInternalError(writer, req, err)
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
		return h.WriteInternalError(writer, req, progErr)
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
		return h.WriteInternalError(writer, req, err)
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
		return h.WriteInternalError(writer, req, err)
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
		return h.WriteInternalError(writer, req, err)
	}

	checkResps, err := h.svc.PromoteChecks(req.Context(), org.UID, org2.Slug, promoteReq)

	switch {
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "discovered check not found")
	case errors.Is(err, ErrAlreadyPromoted):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict, "discovered check already promoted")
	case errors.Is(err, ErrRegionUnavailable):
		return h.WriteError(
			writer, http.StatusUnprocessableEntity, ErrorCodeDiscoveryRegionUnavailable, err.Error(),
		)
	case err != nil:
		return h.WriteInternalError(writer, req, err)
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
		return h.WriteInternalError(writer, req, err)
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

	_, err := h.svc.SoftDeleteGroup(req.Context(), org.UID, queryParams.Get("jobUid"), group, queryParams.Get("region"))
	if errors.Is(err, ErrCheckNotFound) {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "no checks found for group")
	} else if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}
