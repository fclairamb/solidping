package discovery

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
)

const (
	// ErrorCodeDiscoveryRangeTooLarge is returned when CIDRs expand to more than 4096 addresses.
	ErrorCodeDiscoveryRangeTooLarge base.ErrorCode = "DISCOVERY_RANGE_TOO_LARGE"
	// ErrorCodeDiscoveryAlreadyRunning is returned when a scan is already in progress for the org.
	ErrorCodeDiscoveryAlreadyRunning base.ErrorCode = "DISCOVERY_ALREADY_RUNNING"
	// ErrorCodeFreeboxNotGranted is returned when a Freebox discovery targets a
	// channel that has not completed the LCD-pairing flow.
	ErrorCodeFreeboxNotGranted base.ErrorCode = "FREEBOX_NOT_GRANTED"
	// keyData is the standard JSON response wrapper key.
	keyData = "data"
)

// isAdmin returns true if the request context has an admin or super admin role.
func isAdmin(req bunrouter.Request) bool {
	claims, ok := mw.GetClaimsFromContext(req.Context())
	if !ok || claims == nil {
		return false
	}

	return claims.Role == "admin" || claims.IsSuperAdmin()
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
func (h *Handler) RegisterRoutes(group *bunrouter.Group) {
	group.POST("/scans", h.StartScan)
	group.POST("/freebox-scans", h.StartFreeboxScan)
	group.GET("/scans", h.ListScans)
	group.GET("/scans/:jobUid", h.GetScan)
	group.GET("/hosts", h.ListHosts)
	group.POST("/hosts/:uid/promote", h.PromoteHost)
	group.DELETE("/hosts/:uid", h.DismissHost)
}

// StartFreeboxScanRequest is the body for POST /discovery/freebox-scans.
type StartFreeboxScanRequest struct {
	ChannelUID string `json:"channelUid"`
}

// StartFreeboxScan handles POST /orgs/:org/discovery/freebox-scans.
// Admin only. Launches a freebox_lan_discovery run for a paired channel.
func (h *Handler) StartFreeboxScan(writer http.ResponseWriter, req bunrouter.Request) error {
	if !isAdmin(req) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	var body StartFreeboxScanRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "invalid request body")
	}

	if body.ChannelUID == "" {
		return h.WriteError(
			writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, "channelUid is required",
		)
	}

	job, err := h.svc.StartFreeboxScan(req.Context(), org.UID, body.ChannelUID)

	switch {
	case errors.Is(err, ErrFreeboxNotGranted):
		return h.WriteError(
			writer, http.StatusConflict, ErrorCodeFreeboxNotGranted, "Freebox channel is not paired yet",
		)
	case errors.Is(err, ErrFreeboxChannelNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "freebox channel not found")
	case errors.Is(err, ErrAlreadyRunning):
		return h.WriteError(
			writer, http.StatusConflict, ErrorCodeDiscoveryAlreadyRunning,
			"a discovery scan is already running for this organization",
		)
	case err != nil:
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, map[string]any{keyData: job})
}

// StartScan handles POST /orgs/:org/discovery/scans.
// Admin only.
func (h *Handler) StartScan(writer http.ResponseWriter, req bunrouter.Request) error {
	if !isAdmin(req) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	var cfg disc.Config
	if err := json.NewDecoder(req.Body).Decode(&cfg); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "invalid request body")
	}

	if len(cfg.CIDRs) == 0 {
		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, "cidrs is required")
	}

	job, err := h.svc.StartScan(req.Context(), org.UID, cfg)

	switch {
	case errors.Is(err, disc.ErrRangeTooLarge):
		return h.WriteError(writer, http.StatusUnprocessableEntity, ErrorCodeDiscoveryRangeTooLarge, err.Error())
	case errors.Is(err, ErrAlreadyRunning):
		return h.WriteError(
			writer, http.StatusConflict, ErrorCodeDiscoveryAlreadyRunning,
			"a discovery scan is already running for this organization",
		)
	case err != nil:
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, map[string]any{keyData: job})
}

// ListScans handles GET /orgs/:org/discovery/scans.
func (h *Handler) ListScans(writer http.ResponseWriter, req bunrouter.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	jobs, err := h.svc.jobSvc.ListJobs(req.Context(), org.UID, jobsvc.ListJobsOptions{
		Types: []string{
			string(jobdef.JobTypeNetworkDiscovery),
			string(jobdef.JobTypeFreeboxLanDiscovery),
		},
	})
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: jobs})
}

// GetScan handles GET /orgs/:org/discovery/scans/:jobUid.
func (h *Handler) GetScan(writer http.ResponseWriter, req bunrouter.Request) error {
	jobUID := req.Param("jobUid")

	job, err := h.svc.jobSvc.GetJob(req.Context(), jobUID)
	if err != nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "scan not found")
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: job})
}

// ListHosts handles GET /orgs/:org/discovery/hosts.
func (h *Handler) ListHosts(writer http.ResponseWriter, req bunrouter.Request) error {
	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	queryParams := req.URL.Query()
	opts := ListHostsOptions{
		JobUID: queryParams.Get("jobUid"),
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

	hosts, err := h.svc.ListHosts(req.Context(), org.UID, opts)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: hosts})
}

// PromoteHost handles POST /orgs/:org/discovery/hosts/:uid/promote.
// Admin only.
func (h *Handler) PromoteHost(writer http.ResponseWriter, req bunrouter.Request) error {
	if !isAdmin(req) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	hostUID := req.Param("uid")

	var promoteReq PromoteRequest
	if err := json.NewDecoder(req.Body).Decode(&promoteReq); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "invalid request body")
	}

	if len(promoteReq.Checks) == 0 {
		return h.WriteError(
			writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, "at least one check is required",
		)
	}

	for _, spec := range promoteReq.Checks {
		if spec.CheckType == "" {
			return h.WriteError(
				writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, "checkType is required",
			)
		}
	}

	// Need org slug for check creation.
	org2, err := h.svc.dbSvc.GetOrganization(req.Context(), org.UID)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	checkResps, err := h.svc.PromoteHost(req.Context(), org.UID, org2.Slug, hostUID, promoteReq)

	switch {
	case errors.Is(err, ErrHostNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "host not found")
	case errors.Is(err, ErrAlreadyPromoted):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict, "host already promoted")
	case err != nil:
		return h.WriteInternalErrorR(writer, req.Request, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, map[string]any{keyData: checkResps})
}

// DismissHost handles DELETE /orgs/:org/discovery/hosts/:uid.
// Admin only.
func (h *Handler) DismissHost(writer http.ResponseWriter, req bunrouter.Request) error {
	if !isAdmin(req) {
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden, "admin access required")
	}

	org, _ := mw.GetOrganizationFromContext(req.Context())
	if org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "organization not found")
	}

	hostUID := req.Param("uid")

	err := h.svc.SoftDeleteHost(req.Context(), org.UID, hostUID)
	if errors.Is(err, ErrHostNotFound) {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "host not found")
	} else if err != nil {
		return h.WriteInternalError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}
