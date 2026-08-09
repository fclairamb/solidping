// Package statuspages provides HTTP handlers for status page management endpoints.
package statuspages

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	entitlementshandler "github.com/fclairamb/solidping/server/internal/handlers/entitlements"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

const slugValidationMsg = "Slug must start with a lowercase letter, be 3-40 characters, " +
	"and contain only lowercase letters, digits, or hyphens. UUIDs are not allowed."

// visibilityPublic is the status_pages.visibility value that makes a page
// world-readable. Shared across the package so the literal appears once.
const visibilityPublic = "public"

const (
	fieldSlug          = "slug"
	fieldBody          = "body"
	fieldCustomDomain  = "customDomain"
	fieldHistoryPeriod = "historyPeriod"
	fieldCustomCSS     = "customCss"
	fieldCheckUID      = "checkUid"
	fieldCheckGroupUID = "checkGroupUid"
	fieldSettings      = "settings"
	respKeyData        = "data"
	msgInvalidJSON     = "Invalid JSON format"
	historyPeriodMsg   = "History period must be one of: 24h, 7d, 30d, 90d"
	customCSSSizeMsg   = "Custom CSS must be at most 64 KB"
	customCSSImportMsg = "Custom CSS must not contain @import — inline the rules instead " +
		"(external url() references are allowed)"
	availabilityThresholdsMsg = "thresholdDegraded must be greater than 0, less than thresholdUp, " +
		"and thresholdUp must be at most 100"
)

// AvailabilitySettingsInput/SettingsInput live in service.go alongside the
// other request DTOs.

// parseSettingsField strictly decodes the top-level "settings" key from the
// raw request body (unknown keys rejected via DisallowUnknownFields — typos
// in "availability" or its fields become VALIDATION_ERROR instead of being
// silently dropped) and reports whether the "settings" key was present at
// all. present=false means the caller must leave status_pages.settings
// untouched (PATCH semantics). A present-but-null "settings" returns
// (nil, true, nil) — no sections to inspect.
func parseSettingsField(body []byte) (*SettingsInput, bool, error) {
	var top map[string]json.RawMessage
	if uErr := json.Unmarshal(body, &top); uErr != nil {
		return nil, false, uErr
	}

	raw, ok := top[fieldSettings]
	if !ok {
		return nil, false, nil
	}

	if string(raw) == "null" {
		return nil, true, nil
	}

	var sectionPresence map[string]json.RawMessage
	if uErr := json.Unmarshal(raw, &sectionPresence); uErr != nil {
		return nil, true, fmt.Errorf("%w: %s", ErrSettingsUnknownField, uErr.Error())
	}

	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()

	var settingsInput SettingsInput
	if dErr := dec.Decode(&settingsInput); dErr != nil {
		return nil, true, fmt.Errorf("%w: %s", ErrSettingsUnknownField, dErr.Error())
	}

	_, settingsInput.AvailabilitySet = sectionPresence["availability"]

	return &settingsInput, true, nil
}

// mapAvailabilityThresholdsError maps the availability-thresholds validation
// and settings-decode errors to a VALIDATION_ERROR response. Returns
// handled=false when err is neither, so the caller can fall through.
func (h *Handler) mapAvailabilityThresholdsError(writer http.ResponseWriter, err error) (bool, error) {
	switch {
	case errors.Is(err, ErrInvalidAvailabilityThresholds):
		result := h.WriteValidationError(writer, "Invalid availability thresholds", []base.ValidationErrorField{
			{Name: fieldSettings, Message: availabilityThresholdsMsg},
		})

		return true, result
	case errors.Is(err, ErrSettingsUnknownField):
		result := h.WriteValidationError(writer, "Invalid settings", []base.ValidationErrorField{
			{Name: fieldSettings, Message: err.Error()},
		})

		return true, result
	default:
		return false, nil
	}
}

// mapCustomCSSError maps the customCss validation errors to a VALIDATION_ERROR
// response. Returns handled=false when err is not one of them so the caller can
// fall through to its own switch.
func (h *Handler) mapCustomCSSError(writer http.ResponseWriter, err error) (bool, error) {
	switch {
	case errors.Is(err, ErrCustomCSSTooLarge):
		return true, h.WriteValidationError(writer, "Custom CSS is too large", []base.ValidationErrorField{
			{Name: fieldCustomCSS, Message: customCSSSizeMsg},
		})
	case errors.Is(err, ErrCustomCSSImport):
		return true, h.WriteValidationError(writer, "Custom CSS contains @import", []base.ValidationErrorField{
			{Name: fieldCustomCSS, Message: customCSSImportMsg},
		})
	default:
		return false, nil
	}
}

// Handler provides HTTP handlers for status page management endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new status pages handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// --- Status Page handlers ---

// ListStatusPages handles listing all status pages for an organization.
func (h *Handler) ListStatusPages(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	pages, err := h.svc.ListStatusPages(req.Context(), orgSlug)
	if err != nil {
		return h.handleOrgError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		respKeyData: pages,
	})
}

// CreateStatusPage handles creating a new status page.
func (h *Handler) CreateStatusPage(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	body, readErr := io.ReadAll(req.Body)
	if readErr != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	var createReq CreateStatusPageRequest
	if err := json.Unmarshal(body, &createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	// Settings is decoded separately (not via the lenient default Decode
	// above) so unknown keys are rejected (DisallowUnknownFields). Presence
	// is irrelevant at create time — there's nothing to merge against.
	settings, _, settingsErr := parseSettingsField(body)
	if settingsErr != nil {
		return h.handleCreatePageError(writer, settingsErr)
	}

	createReq.Settings = settings

	page, err := h.svc.CreateStatusPage(req.Context(), orgSlug, &createReq)
	if err != nil {
		return h.handleCreatePageError(writer, err)
	}

	// No analytics capture here on purpose: status_page_published is emitted by
	// Service.CreateStatusPage / Service.UpdateStatusPage, so the MCP tools go
	// through the same code path (spec 2026-08-02-08).
	return h.WriteJSON(writer, http.StatusCreated, page)
}

// GetStatusPage handles retrieving a single status page by UID or slug.
func (h *Handler) GetStatusPage(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	identifier := httpx.Param(req, "statusPageUid")

	opts := GetStatusPageOptions{}
	withParam := req.URL.Query().Get("with")
	if withParam != "" {
		parts := strings.Split(withParam, ",")
		for _, part := range parts {
			if strings.TrimSpace(part) == "sections" {
				opts.IncludeSections = true
			}
		}
	}

	page, err := h.svc.GetStatusPage(req.Context(), orgSlug, identifier, opts)
	if err != nil {
		return h.handlePageError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, page)
}

// UpdateStatusPage handles updating an existing status page.
func (h *Handler) UpdateStatusPage(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	identifier := httpx.Param(req, "statusPageUid")

	body, readErr := io.ReadAll(req.Body)
	if readErr != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	var updateReq UpdateStatusPageRequest
	if err := json.Unmarshal(body, &updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	// Presence detection: a `customDomain` key that is present (even as null or
	// "") means "change the domain"; an omitted key leaves it untouched. A plain
	// *string cannot tell those apart, so probe the raw object.
	var presence map[string]json.RawMessage
	if err := json.Unmarshal(body, &presence); err == nil {
		_, updateReq.CustomDomainSet = presence["customDomain"]
	}

	settings, settingsPresent, settingsErr := parseSettingsField(body)
	if settingsErr != nil {
		return h.handleUpdatePageError(writer, settingsErr)
	}

	updateReq.Settings = settings
	updateReq.SettingsPresent = settingsPresent

	page, err := h.svc.UpdateStatusPage(req.Context(), orgSlug, identifier, &updateReq)
	if err != nil {
		return h.handleUpdatePageError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, page)
}

// VerifyCustomDomain runs the DNS verification for a status page's custom domain
// synchronously and returns the updated (authenticated) page.
func (h *Handler) VerifyCustomDomain(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	identifier := httpx.Param(req, "statusPageUid")

	page, err := h.svc.VerifyCustomDomain(req.Context(), orgSlug, identifier)
	if err != nil {
		return h.handleVerifyError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, page)
}

// CustomDomainAllowed is the public edge-TLS "ask" endpoint. It answers 204 when
// the queried domain currently resolves to a verified, enabled, public page and
// 404 otherwise — no body, so it leaks nothing beyond the boolean the TLS edge
// (Caddy on_demand_tls / cert-manager) needs.
func (h *Handler) CustomDomainAllowed(writer http.ResponseWriter, req *http.Request) error {
	domain := req.URL.Query().Get("domain")

	if h.svc.CustomDomainServable(req.Context(), domain) {
		writer.WriteHeader(http.StatusNoContent)

		return nil
	}

	writer.WriteHeader(http.StatusNotFound)

	return nil
}

// DeleteStatusPage handles deleting a status page.
func (h *Handler) DeleteStatusPage(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	identifier := httpx.Param(req, "statusPageUid")

	if err := h.svc.DeleteStatusPage(req.Context(), orgSlug, identifier); err != nil {
		return h.handlePageError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}

// --- Section handlers ---

// ListSections handles listing all sections for a status page.
func (h *Handler) ListSections(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")

	sections, err := h.svc.ListSections(req.Context(), orgSlug, pageIdentifier)
	if err != nil {
		return h.handlePageError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"data": sections,
	})
}

// CreateSection handles creating a new section within a status page.
func (h *Handler) CreateSection(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")

	var createReq CreateSectionRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	section, err := h.svc.CreateSection(req.Context(), orgSlug, pageIdentifier, createReq)
	if err != nil {
		return h.handleCreateSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, section)
}

// GetSection handles retrieving a single section.
func (h *Handler) GetSection(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")
	sectionIdentifier := httpx.Param(req, "sectionUid")

	section, err := h.svc.GetSection(req.Context(), orgSlug, pageIdentifier, sectionIdentifier)
	if err != nil {
		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, section)
}

// UpdateSection handles updating an existing section.
func (h *Handler) UpdateSection(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")
	sectionIdentifier := httpx.Param(req, "sectionUid")

	var updateReq UpdateSectionRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	section, err := h.svc.UpdateSection(req.Context(), orgSlug, pageIdentifier, sectionIdentifier, updateReq)
	if err != nil {
		return h.handleUpdateSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, section)
}

// DeleteSection handles deleting a section.
func (h *Handler) DeleteSection(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")
	sectionIdentifier := httpx.Param(req, "sectionUid")

	if err := h.svc.DeleteSection(req.Context(), orgSlug, pageIdentifier, sectionIdentifier); err != nil {
		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}

// --- Resource handlers ---

// ListResources handles listing all resources for a section.
func (h *Handler) ListResources(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")
	sectionIdentifier := httpx.Param(req, "sectionUid")

	resources, err := h.svc.ListResources(req.Context(), orgSlug, pageIdentifier, sectionIdentifier)
	if err != nil {
		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"data": resources,
	})
}

// CreateResource handles adding a check to a section.
func (h *Handler) CreateResource(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")
	sectionIdentifier := httpx.Param(req, "sectionUid")

	var createReq CreateResourceRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	resource, err := h.svc.CreateResource(req.Context(), orgSlug, pageIdentifier, sectionIdentifier, createReq)
	if err != nil {
		return h.handleResourceError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, resource)
}

// UpdateResource handles updating a resource.
func (h *Handler) UpdateResource(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")
	sectionIdentifier := httpx.Param(req, "sectionUid")
	resourceUID := httpx.Param(req, "resourceUid")

	var updateReq UpdateResourceRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	resource, err := h.svc.UpdateResource(
		req.Context(), orgSlug, pageIdentifier, sectionIdentifier, resourceUID, updateReq,
	)
	if err != nil {
		return h.handleResourceError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resource)
}

// ReorderResourcesRequest is the body for the reorder endpoint: an ordered
// list of resource UIDs that exactly matches the section's current set.
type ReorderResourcesRequest struct {
	UIDs []string `json:"uids"`
}

// ReorderResources handles renumbering resources within a section to match
// the supplied order. Used by the dashboard's drag-and-drop UI.
func (h *Handler) ReorderResources(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")
	sectionIdentifier := httpx.Param(req, "sectionUid")

	var reorderReq ReorderResourcesRequest
	if err := json.NewDecoder(req.Body).Decode(&reorderReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if err := h.svc.ReorderResources(
		req.Context(), orgSlug, pageIdentifier, sectionIdentifier, reorderReq.UIDs,
	); err != nil {
		if errors.Is(err, ErrReorderUIDsMismatch) {
			return h.WriteValidationError(writer, "Reorder UIDs do not match the section", []base.ValidationErrorField{
				{Name: "uids", Message: "must list every resource in the section exactly once"},
			})
		}

		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}

// ReorderSectionsRequest is the body for the section-reorder endpoint: an
// ordered list of section UIDs that exactly matches the page's current set.
type ReorderSectionsRequest struct {
	UIDs []string `json:"uids"`
}

// ReorderSections handles renumbering sections within a page to match the
// supplied order. Used by the dashboard's section drag-and-drop UI.
func (h *Handler) ReorderSections(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")

	var reorderReq ReorderSectionsRequest
	if err := json.NewDecoder(req.Body).Decode(&reorderReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if err := h.svc.ReorderSections(
		req.Context(), orgSlug, pageIdentifier, reorderReq.UIDs,
	); err != nil {
		if errors.Is(err, ErrReorderUIDsMismatch) {
			return h.WriteValidationError(writer, "Reorder UIDs do not match the page", []base.ValidationErrorField{
				{Name: "uids", Message: "must list every section in the page exactly once"},
			})
		}

		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}

// DeleteResource handles removing a check from a section.
func (h *Handler) DeleteResource(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	pageIdentifier := httpx.Param(req, "statusPageUid")
	sectionIdentifier := httpx.Param(req, "sectionUid")
	resourceUID := httpx.Param(req, "resourceUid")

	if err := h.svc.DeleteResource(
		req.Context(), orgSlug, pageIdentifier, sectionIdentifier, resourceUID,
	); err != nil {
		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}

// --- Public handlers ---

// ViewStatusPage handles the public view of a status page.
func (h *Handler) ViewStatusPage(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	slug := httpx.Param(req, "slug")

	page, err := h.svc.ViewStatusPage(req.Context(), orgSlug, slug)
	if err != nil {
		return h.handlePublicError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, page)
}

// ViewDefaultStatusPage handles the public view of an organization's default status page.
func (h *Handler) ViewDefaultStatusPage(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	page, err := h.svc.ViewDefaultStatusPage(req.Context(), orgSlug)
	if err != nil {
		return h.handlePublicError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, page)
}

// StatusPageSummaryResponse is the lightweight "is it up?" public response
// (spec 2026-08-08-06).
type StatusPageSummaryResponse struct {
	Status      string                    `json:"status"`
	Counts      StatusCountsResponse      `json:"counts"`
	Page        StatusPageSummaryPageInfo `json:"page"`
	GeneratedAt time.Time                 `json:"generatedAt"`
}

// StatusPageSummaryPageInfo identifies the page a summary belongs to.
type StatusPageSummaryPageInfo struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
	// URL is the canonical public URL: the verified custom domain when
	// active, otherwise the absolute path-based /status0/{org}/{slug} URL
	// derived from the request — same derivation the OG-meta injection uses
	// (see internal/app/status0_meta.go).
	URL string `json:"url"`
}

// ViewStatusPageSummary handles the public, cheap "is it up?" rollup for a
// status page: GET /api/v1/status-pages/:org/:slug/summary. It reuses the
// same visibility gate and rollup as ViewStatusPage/ViewDefaultStatusPage
// (models.RollupPageStatus) so it can never disagree with them, but skips the
// expensive sections/availability payload.
func (h *Handler) ViewStatusPageSummary(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	slug := httpx.Param(req, "slug")

	summary, err := h.svc.ViewStatusPageSummary(req.Context(), orgSlug, slug)
	if err != nil {
		return h.handlePublicError(writer, err)
	}

	response := StatusPageSummaryResponse{
		Status: string(summary.Status),
		Counts: StatusCountsResponse{
			Operational: summary.Counts.Operational,
			Degraded:    summary.Counts.Degraded,
			Down:        summary.Counts.Down,
			Maintenance: summary.Counts.Maintenance,
			Unknown:     summary.Counts.Unknown,
		},
		Page: StatusPageSummaryPageInfo{
			Name: summary.PageName,
			Slug: summary.PageSlug,
			URL:  publicPageURL(req, orgSlug, &summary),
		},
		GeneratedAt: time.Now().UTC(),
	}

	writer.Header().Set("Cache-Control", "public, max-age=60")

	return h.WriteJSON(writer, http.StatusOK, response)
}

// publicPageURL derives the canonical public URL for a status page: the
// verified custom domain when active, otherwise the absolute
// /status0/{org}/{slug} URL built from the incoming request's scheme/host —
// the same derivation status0's OG-meta injection uses
// (internal/app/status0_meta.go:requestOrigin/requestScheme). Duplicated here
// rather than imported because that helper lives in the app package, which
// this handler package must not depend on.
func publicPageURL(req *http.Request, orgSlug string, summary *StatusPageSummary) string {
	if summary.CustomDomain != "" {
		return "https://" + summary.CustomDomain + "/"
	}

	return requestOrigin(req) + "/status0/" + orgSlug + "/" + summary.PageSlug
}

// GetBadge handles GET /api/v1/status-pages/:org/:slug/badge — a public SVG
// badge reflecting the page-level rollup status (spec 2026-08-08-07). It is
// registered alongside the other public status-page routes and shares their
// visibility gate via Service.GenerateBadge (which delegates to
// ViewStatusPageSummary), so a private or disabled page 404s exactly like
// ViewStatusPage/ViewStatusPageSummary — never leaking existence.
func (h *Handler) GetBadge(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	slug := httpx.Param(req, "slug")

	opts := BadgeOptions{
		Label: req.URL.Query().Get("label"),
		Style: req.URL.Query().Get("style"),
	}

	if minWidth, ok := parseBadgeIntParam(req.URL.Query().Get("minWidth"), 0, 800); ok {
		opts.MinWidth = minWidth
	}

	if width, ok := parseBadgeIntParam(req.URL.Query().Get("width"), 60, 800); ok {
		opts.Width = width
	}

	svg, err := h.svc.GenerateBadge(req.Context(), orgSlug, slug, opts)
	if err != nil {
		return h.handlePublicError(writer, err)
	}

	writer.Header().Set("Content-Type", "image/svg+xml")
	writer.Header().Set("Cache-Control", "public, max-age=60")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(svg))

	return nil
}

// parseBadgeIntParam parses an integer query parameter, clamping it within
// [minVal, maxVal]. Returns 0, false if the parameter is empty or invalid.
// Mirrors badges.parseIntParam (unexported in its own package) so the two
// badge endpoints clamp width/minWidth identically.
func parseBadgeIntParam(raw string, minVal, maxVal int) (int, bool) {
	if raw == "" {
		return 0, false
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}

	if parsed < minVal {
		parsed = minVal
	}

	if parsed > maxVal {
		parsed = maxVal
	}

	return parsed, true
}

// requestScheme resolves the request scheme, preferring the first token of a
// proxy-set X-Forwarded-Proto, then the TLS state, defaulting to http.
func requestScheme(req *http.Request) string {
	if proto := req.Header.Get("X-Forwarded-Proto"); proto != "" {
		if idx := strings.IndexByte(proto, ','); idx != -1 {
			proto = proto[:idx]
		}

		if proto = strings.TrimSpace(proto); proto != "" {
			return proto
		}
	}

	if req.TLS != nil {
		return "https"
	}

	return "http"
}

// requestOrigin builds the "scheme://host" origin for a request.
func requestOrigin(req *http.Request) string {
	return requestScheme(req) + "://" + req.Host
}

// --- Error handlers ---

func (h *Handler) handleOrgError(writer http.ResponseWriter, err error) error {
	if errors.Is(err, ErrOrganizationNotFound) {
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	}

	return h.WriteInternalError(writer, err)
}

func (h *Handler) handlePageError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// mapCustomDomainError maps the custom-domain-specific service errors to HTTP.
// Returns handled=false when err is not a custom-domain error so the caller can
// fall through to its own switch.
func (h *Handler) mapCustomDomainError(writer http.ResponseWriter, err error) (bool, error) {
	switch {
	case errors.Is(err, entcore.ErrEntitlementExceeded):
		var qe *entcore.QuotaError
		if !errors.As(err, &qe) {
			return true, h.WriteInternalError(writer, err)
		}

		body := entitlementshandler.FormatQuotaError(qe)
		body["code"] = string(base.ErrorCodeQuotaExceeded)

		return true, h.WriteJSON(writer, http.StatusPaymentRequired, body)
	case errors.Is(err, ErrCustomDomainInvalid):
		return true, h.WriteValidationError(writer, "Invalid custom domain", []base.ValidationErrorField{
			{Name: fieldCustomDomain, Message: err.Error()},
		})
	case errors.Is(err, ErrCustomDomainSelfShadow):
		return true, h.WriteValidationError(writer, "Invalid custom domain", []base.ValidationErrorField{
			{Name: fieldCustomDomain, Message: "This domain shadows one of the installation's own hostnames"},
		})
	case errors.Is(err, ErrCustomDomainTaken):
		return true, h.WriteErrorErr(
			writer, http.StatusConflict, base.ErrorCodeConflict, "Custom domain already in use", err)
	case errors.Is(err, ErrCustomDomainNotSet):
		return true, h.WriteValidationError(writer, "No custom domain configured", []base.ValidationErrorField{
			{Name: fieldCustomDomain, Message: "Set a custom domain before verifying it"},
		})
	case errors.Is(err, ErrCustomDomainRateLimited):
		return true, h.WriteErrorErr(
			writer, http.StatusTooManyRequests, base.ErrorCodeRateLimited,
			"Too many verification attempts, please retry shortly", err)
	default:
		return false, nil
	}
}

// handleVerifyError maps errors from the verify-now endpoint.
func (h *Handler) handleVerifyError(writer http.ResponseWriter, err error) error {
	if handled, result := h.mapCustomDomainError(writer, err); handled {
		return result
	}

	return h.handlePageError(writer, err)
}

func (h *Handler) handleCreatePageError(writer http.ResponseWriter, err error) error {
	if handled, result := h.mapCustomDomainError(writer, err); handled {
		return result
	}

	if handled, result := h.mapCustomCSSError(writer, err); handled {
		return result
	}

	if handled, result := h.mapAvailabilityThresholdsError(writer, err); handled {
		return result
	}

	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{Name: fieldSlug, Message: "A status page with this slug already exists in this organization"},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{Name: fieldSlug, Message: slugValidationMsg},
		})
	case errors.Is(err, ErrInvalidHistoryPeriod):
		return h.WriteValidationError(writer, "Invalid history period", []base.ValidationErrorField{
			{Name: fieldHistoryPeriod, Message: historyPeriodMsg},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) handleUpdatePageError(writer http.ResponseWriter, err error) error {
	if handled, result := h.mapCustomDomainError(writer, err); handled {
		return result
	}

	if handled, result := h.mapCustomCSSError(writer, err); handled {
		return result
	}

	if handled, result := h.mapAvailabilityThresholdsError(writer, err); handled {
		return result
	}

	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{Name: fieldSlug, Message: "A status page with this slug already exists in this organization"},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{Name: fieldSlug, Message: slugValidationMsg},
		})
	case errors.Is(err, ErrInvalidHistoryPeriod):
		return h.WriteValidationError(writer, "Invalid history period", []base.ValidationErrorField{
			{Name: fieldHistoryPeriod, Message: historyPeriodMsg},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) handleSectionError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found", err)
	case errors.Is(err, ErrStatusPageSectionNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageSectionNotFound, "Section not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) handleCreateSectionError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{Name: fieldSlug, Message: "A section with this slug already exists in this status page"},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{Name: fieldSlug, Message: slugValidationMsg},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) handleUpdateSectionError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found", err)
	case errors.Is(err, ErrStatusPageSectionNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageSectionNotFound, "Section not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{Name: fieldSlug, Message: "A section with this slug already exists in this status page"},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{Name: fieldSlug, Message: slugValidationMsg},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) handleResourceError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found", err)
	case errors.Is(err, ErrStatusPageSectionNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageSectionNotFound, "Section not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	case errors.Is(err, ErrCheckGroupNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeCheckGroupNotFound, "Check group not found", err)
	case errors.Is(err, ErrResourceTargetInvalid):
		return h.writeResourceTargetError(writer)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// resourceTargetMsg names BOTH fields so the caller can see which pair is
// mutually exclusive, whether it sent zero or two of them.
const resourceTargetMsg = "Exactly one of checkUid or checkGroupUid must be set"

// writeResourceTargetError renders the checkUid-XOR-checkGroupUid violation as
// a VALIDATION_ERROR naming both fields (spec 2026-08-01-03).
func (h *Handler) writeResourceTargetError(writer http.ResponseWriter) error {
	return h.WriteValidationError(writer, resourceTargetMsg, []base.ValidationErrorField{
		{Name: fieldCheckUID, Message: resourceTargetMsg},
		{Name: fieldCheckGroupUID, Message: resourceTargetMsg},
	})
}

func (h *Handler) handlePublicError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound), errors.Is(err, ErrStatusPageNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}
