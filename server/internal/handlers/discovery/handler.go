package discovery

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
)

// isAdmin returns true if the request context has an admin or super admin role.
func isAdmin(req bunrouter.Request) bool {
	claims, ok := mw.GetClaimsFromContext(req.Context())
	if !ok || claims == nil {
		return false
	}
	return claims.Role == "admin" || claims.IsSuperAdmin()
}

const (
	// ErrorCodeDiscoveryRangeTooLarge is returned when CIDRs expand to more than 4096 addresses.
	ErrorCodeDiscoveryRangeTooLarge base.ErrorCode = "DISCOVERY_RANGE_TOO_LARGE"
	// ErrorCodeDiscoveryAlreadyRunning is returned when a scan is already in progress for the org.
	ErrorCodeDiscoveryAlreadyRunning base.ErrorCode = "DISCOVERY_ALREADY_RUNNING"
)

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
func (h *Handler) RegisterRoutes(group *bunrouter.Group) {
	group.POST("/scans", h.StartScan)
	group.GET("/scans", h.ListScans)
	group.GET("/scans/:jobUid", h.GetScan)
	group.GET("/hosts", h.ListHosts)
	group.POST("/hosts/:uid/promote", h.PromoteHost)
	group.DELETE("/hosts/:uid", h.DismissHost)
}

// StartScan handles POST /orgs/:org/discovery/scans.
// Admin only.
func (h *Handler) StartScan(w http.ResponseWriter, req bunrouter.Request) error {
	if !isAdmin(req) {
		return h.WriteError(w, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(w, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	var cfg disc.Config
	if err := json.NewDecoder(req.Body).Decode(&cfg); err != nil {
		return h.WriteError(w, http.StatusBadRequest, base.ErrorCodeValidationError, "invalid request body")
	}

	if len(cfg.CIDRs) == 0 {
		return h.WriteError(w, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, "cidrs is required")
	}

	job, err := h.svc.StartScan(req.Context(), org.UID, cfg)
	if errors.Is(err, disc.ErrRangeTooLarge) {
		return h.WriteError(w, http.StatusUnprocessableEntity, ErrorCodeDiscoveryRangeTooLarge, err.Error())
	} else if errors.Is(err, ErrAlreadyRunning) {
		return h.WriteError(w, http.StatusConflict, ErrorCodeDiscoveryAlreadyRunning, "a discovery scan is already running for this organization")
	} else if err != nil {
		return h.WriteInternalError(w, err)
	}

	return h.WriteJSON(w, http.StatusCreated, map[string]any{"data": job})
}

// ListScans handles GET /orgs/:org/discovery/scans.
func (h *Handler) ListScans(w http.ResponseWriter, req bunrouter.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(w, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	jobs, err := h.svc.jobSvc.ListJobs(req.Context(), org.UID, jobsvc.ListJobsOptions{
		Type: string(jobdef.JobTypeNetworkDiscovery),
	})
	if err != nil {
		return h.WriteInternalError(w, err)
	}

	return h.WriteJSON(w, http.StatusOK, map[string]any{"data": jobs})
}

// GetScan handles GET /orgs/:org/discovery/scans/:jobUid.
func (h *Handler) GetScan(w http.ResponseWriter, req bunrouter.Request) error {
	jobUID := req.Param("jobUid")

	job, err := h.svc.jobSvc.GetJob(req.Context(), jobUID)
	if err != nil {
		return h.WriteError(w, http.StatusNotFound, base.ErrorCodeNotFound, "scan not found")
	}

	return h.WriteJSON(w, http.StatusOK, map[string]any{"data": job})
}

// ListHosts handles GET /orgs/:org/discovery/hosts.
func (h *Handler) ListHosts(w http.ResponseWriter, req bunrouter.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(w, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	q := req.URL.Query()
	opts := ListHostsOptions{
		JobUID: q.Get("jobUid"),
	}

	if promoted := q.Get("promoted"); promoted != "" {
		b := promoted == "true"
		opts.Promoted = &b
	}

	hosts, err := h.svc.ListHosts(req.Context(), org.UID, opts)
	if err != nil {
		return h.WriteInternalError(w, err)
	}

	return h.WriteJSON(w, http.StatusOK, map[string]any{"data": hosts})
}

// PromoteHost handles POST /orgs/:org/discovery/hosts/:uid/promote.
// Admin only.
func (h *Handler) PromoteHost(w http.ResponseWriter, req bunrouter.Request) error {
	if !isAdmin(req) {
		return h.WriteError(w, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(w, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	hostUID := req.Param("uid")

	var promoteReq PromoteRequest
	if err := json.NewDecoder(req.Body).Decode(&promoteReq); err != nil {
		return h.WriteError(w, http.StatusBadRequest, base.ErrorCodeValidationError, "invalid request body")
	}

	if promoteReq.CheckType == "" {
		return h.WriteError(w, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, "checkType is required")
	}

	// Need org slug for check creation.
	orgSlug, err := h.svc.dbSvc.GetOrganization(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(w, err)
	}

	checkResp, err := h.svc.PromoteHost(req.Context(), org.UID, orgSlug.Slug, hostUID, promoteReq)
	if errors.Is(err, ErrHostNotFound) {
		return h.WriteError(w, http.StatusNotFound, base.ErrorCodeNotFound, "host not found")
	} else if errors.Is(err, ErrAlreadyPromoted) {
		return h.WriteError(w, http.StatusConflict, base.ErrorCodeConflict, "host already promoted")
	} else if err != nil {
		return h.WriteInternalErrorR(w, req.Request, err)
	}

	return h.WriteJSON(w, http.StatusCreated, map[string]any{"data": checkResp})
}

// DismissHost handles DELETE /orgs/:org/discovery/hosts/:uid.
// Admin only.
func (h *Handler) DismissHost(w http.ResponseWriter, req bunrouter.Request) error {
	if !isAdmin(req) {
		return h.WriteError(w, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(w, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	hostUID := req.Param("uid")

	err := h.svc.SoftDeleteHost(req.Context(), org.UID, hostUID)
	if errors.Is(err, ErrHostNotFound) {
		return h.WriteError(w, http.StatusNotFound, base.ErrorCodeNotFound, "host not found")
	} else if err != nil {
		return h.WriteInternalError(w, err)
	}

	w.WriteHeader(http.StatusNoContent)

	return nil
}
