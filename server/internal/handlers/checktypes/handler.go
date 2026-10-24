package checktypes

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkvendorstatus"
	"github.com/fclairamb/solidping/server/internal/checkers/checkvendorstatus/catalog"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Handler provides HTTP handlers for check type endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
	// guard is read at call time: the egress guard is installed after the
	// server is built.
	guard func() *egress.Guard
	// orgTypes serves the plugin types an org's agents reported. Nil serves
	// none.
	orgTypes OrgPluginTypes
}

// SetEgressGuard wires the SSRF guard used by the vendor-status components lookup.
func (h *Handler) SetEgressGuard(guard func() *egress.Guard) { h.guard = guard }

// NewHandler creates a new check types handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListServerCheckTypes handles GET /api/v1/check-types (public, shows server-level status).
func (h *Handler) ListServerCheckTypes(writer http.ResponseWriter, _ *http.Request) error {
	response := h.svc.ListServerCheckTypes()

	return h.WriteJSON(writer, http.StatusOK, response)
}

// ListOrgCheckTypes handles GET /api/v1/orgs/:org/check-types (auth required, org-resolved).
func (h *Handler) ListOrgCheckTypes(writer http.ResponseWriter, req *http.Request) error {
	// For now, no per-org disabled types are loaded from DB.
	// This will be wired to the parameters table in a follow-up.
	_ = httpx.Param(req, "org")

	response := h.svc.ListOrgCheckTypes(nil)

	// The plugin types this org's agents reported (spec 2026-10-17-05 §18),
	// read under the org the middleware resolved, never another one.
	if orgUID := orgUIDOf(req); h.orgTypes != nil && orgUID != "" {
		remotes, err := h.orgTypes.List(req.Context(), orgUID)
		if err != nil {
			return h.WriteInternalError(writer, req, err)
		}

		response = withOrgPluginTypes(response, remotes)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// ListSampleConfigs handles GET /api/v1/check-types/samples (public, no auth).
func (h *Handler) ListSampleConfigs(writer http.ResponseWriter, req *http.Request) error {
	filterType := req.URL.Query().Get("type")

	response := h.svc.ListSampleConfigs(filterType)

	return h.WriteJSON(writer, http.StatusOK, response)
}

// ListVendorStatusCatalog handles GET /api/v1/check-types/vendor-status/catalog (public, no auth).
func (h *Handler) ListVendorStatusCatalog(writer http.ResponseWriter, _ *http.Request) error {
	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": catalog.All()})
}

// vendorComponentsRequest is the body of the components lookup.
type vendorComponentsRequest struct {
	URL string `json:"url"`
}

// ListVendorStatusComponents handles POST /api/v1/orgs/:org/checks/vendor-status/components.
// The browser cannot fetch a vendor page itself (CSP), so the server does it
// through the egress guard.
func (h *Handler) ListVendorStatusComponents(writer http.ResponseWriter, req *http.Request) error {
	var body vendorComponentsRequest

	raw, err := io.ReadAll(io.LimitReader(req.Body, 4096))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	root := strings.TrimSpace(body.URL)
	parsed, parseErr := url.Parse(root)
	if parseErr != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "url must be an http(s) URL")
	}

	ctx := req.Context()
	if h.guard != nil {
		ctx = egress.WithGuard(ctx, h.guard())
	}

	components, err := checkvendorstatus.ListComponents(ctx, root)
	if err != nil {
		if errors.Is(err, checkvendorstatus.ErrFetch) {
			return h.WriteError(writer, http.StatusBadGateway, base.ErrorCodeValidationError, err.Error())
		}

		return err
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": components})
}
