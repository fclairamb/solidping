// Package checks provides HTTP handlers for check management endpoints.
package checks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	entitlementshandler "github.com/fclairamb/solidping/server/internal/handlers/entitlements"
	"github.com/fclairamb/solidping/server/internal/httpx"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// errInvalidStatus is returned when an unknown status token appears in ?status=.
var errInvalidStatus = errors.New("invalid status filter token")

// parseStatusFilter accepts a comma-separated list of status tokens
// (up/down/created/validating/degraded/warning) and returns the matching
// CheckStatus values.
func parseStatusFilter(s string) ([]models.CheckStatus, error) {
	parts := strings.Split(s, ",")
	out := make([]models.CheckStatus, 0, len(parts))

	for _, raw := range parts {
		token := strings.TrimSpace(strings.ToLower(raw))
		switch token {
		case "":
			continue
		case "created":
			out = append(out, models.CheckStatusCreated)
		case "up":
			out = append(out, models.CheckStatusUp)
		case "down":
			out = append(out, models.CheckStatusDown)
		case "validating":
			out = append(out, models.CheckStatusValidating)
		case "degraded":
			out = append(out, models.CheckStatusDegraded)
		case "warning":
			out = append(out, models.CheckStatusWarning)
		default:
			return nil, fmt.Errorf("%w: %s", errInvalidStatus, token)
		}
	}

	return out, nil
}

const (
	fieldType          = "type"
	fieldSlug          = "slug"
	fieldBody          = "body"
	fieldInternal      = "internal"
	msgInvalidJSON     = "Invalid JSON format"
	msgSlugConflictOrg = "A check with this slug already exists in this organization"
	// msgInternalNotWritable explains the refusal of a client-supplied
	// `internal` (spec 2026-08-27-01) — read-only, server-owned.
	msgInternalNotWritable = "The internal flag is read-only: it marks server-created checks and cannot be set by a client"
	// queryTrue is the literal a boolean query flag must equal to be enabled
	// (e.g. ?dryRun=true).
	queryTrue = "true"
)

// Handler provides HTTP handlers for check management endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new checks handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ValidateCheck handles validating a check configuration without persisting.
func (h *Handler) ValidateCheck(
	writer http.ResponseWriter, req *http.Request,
) error {
	orgSlug := httpx.Param(req, "org")

	var validateReq ValidateCheckRequest
	if err := json.NewDecoder(req.Body).Decode(&validateReq); err != nil {
		return h.WriteValidationError(
			writer, "Invalid JSON", []base.ValidationErrorField{
				{Name: fieldBody, Message: msgInvalidJSON},
			})
	}

	resp, err := h.svc.ValidateCheck(req.Context(), orgSlug, &validateReq)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// parseTypeFilter splits the `type` query parameter — singular name,
// comma-separated multi-value, per the API convention (`?type=ssh` /
// `?type=http,tcp`) — into the list of check types to filter on. Blank entries
// are dropped, so `?type=` and `?type=http,,tcp` both behave sensibly.
func parseTypeFilter(typeParam string) []string {
	if typeParam == "" {
		return nil
	}

	var types []string

	for _, checkType := range strings.Split(typeParam, ",") {
		if trimmed := strings.TrimSpace(checkType); trimmed != "" {
			types = append(types, trimmed)
		}
	}

	return types
}

// GetCheckStats handles GET /api/v1/orgs/{org}/checks/stats: the org-wide
// aggregate check counters (spec 2026-08-02-06, GitHub issue #172).
//
// This endpoint exists because the dashboard used to derive its KPI counters
// from one page of the checks list, which the list endpoint clamps to 100
// rows — so every counter was silently wrong for orgs with more checks than
// that. The counts here come from a single SQL GROUP BY over the whole table.
//
// The response is served from a per-org in-memory cache with a
// defaultCheckStatsTTL (1 minute) lifetime, so it can lag a check
// create/delete/status flip by up to that long. That is deliberate: these are
// informational counters on a polling dashboard, and the alternative — busting
// the cache from every check write path and from the result pipeline's status
// transitions — buys nothing at this staleness budget. Consumers needing an
// exact, immediately-consistent count must use the list endpoint's
// pagination.total instead.
//
// Route ordering note: this MUST stay registered ahead of
// GET /orgs/:org/checks/:checkUid, or "stats" is captured as a check UID.
func (h *Handler) GetCheckStats(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	stats, err := h.svc.GetCheckStats(req.Context(), orgSlug)
	if err != nil {
		return h.handleListError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, stats)
}

// ListChecks handles listing all checks for an organization.
//
//nolint:funlen,cyclop // List handler has many query parameter extractions
func (h *Handler) ListChecks(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	query := req.URL.Query()

	// Parse the "with" query parameter
	opts := ListChecksOptions{}
	withParam := query.Get("with")
	if withParam != "" {
		// Split by comma to handle multiple values
		parts := strings.Split(withParam, ",")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			switch trimmed {
			case "last_result":
				opts.IncludeLastResult = true
			case "last_status_change":
				opts.IncludeLastStatusChange = true
			}
		}
	}

	// Parse the "labels" query parameter (format: key1:value1,key2:value2)
	labelsParam := query.Get("labels")
	if labelsParam != "" {
		opts.Labels = make(map[string]string)
		pairs := strings.Split(labelsParam, ",")
		for _, pair := range pairs {
			kv := strings.SplitN(pair, ":", 2)
			if len(kv) == 2 {
				opts.Labels[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
			}
		}
	}

	// Parse cursor
	opts.Cursor = query.Get("cursor")

	// Parse limit (default 20, max 100)
	opts.Limit = 20
	if limitParam := query.Get("limit"); limitParam != "" {
		limit, err := strconv.Atoi(limitParam)
		if err != nil {
			return h.WriteErrorErr(
				writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid limit parameter", err)
		}
		if limit < 1 {
			return h.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Limit must be at least 1")
		}
		if limit > 100 {
			limit = 100
		}
		opts.Limit = limit
	}

	// Parse checkGroupUid filter
	if checkGroupUID := query.Get("checkGroupUid"); checkGroupUID != "" {
		opts.CheckGroupUID = &checkGroupUID
	}

	// Parse search query
	opts.Query = query.Get("q")

	opts.Types = parseTypeFilter(query.Get(fieldType))

	// Parse internal filter
	if internalParam := query.Get("internal"); internalParam != "" {
		opts.Internal = &internalParam
	}

	// Parse status filter (comma-separated: up,down,created,validating,degraded,warning)
	if statusParam := query.Get("status"); statusParam != "" {
		statuses, err := parseStatusFilter(statusParam)
		if err != nil {
			return h.WriteErrorErr(
				writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
		}
		opts.Statuses = statuses
	}

	// Parse sort ordering (opt-in). "group" and "targetHost" are recognized
	// today; any other non-empty value is a validation error rather than a
	// silently-ignored no-op. Empty/absent keeps the default created_at DESC
	// ordering.
	if sortParam := query.Get("sort"); sortParam != "" {
		if sortParam != "group" && sortParam != "targetHost" {
			return h.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Invalid sort parameter: only \"group\" or \"targetHost\" is supported")
		}
		opts.Sort = sortParam
	}

	response, err := h.svc.ListChecks(req.Context(), orgSlug, opts)
	if err != nil {
		return h.handleListError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// CreateCheck handles creating a new check for an organization.
func (h *Handler) CreateCheck(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	var createReq CreateCheckRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	// Validate config is required
	if createReq.Config == nil {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "config", Message: "Config is required"},
		})
	}

	// Infer type from URL if not specified
	if createReq.Type == "" {
		inferredType := registry.InferCheckTypeFromConfig(createReq.Config)
		if inferredType == "" {
			return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
				{Name: fieldType, Message: "Type is required when url is not provided or has unrecognized scheme"},
			})
		}

		createReq.Type = string(inferredType)
	}

	// Auto-generate name from URL if not provided
	// Slug auto-generation is handled by the service layer (via checker.Validate + sanitizeSlug)
	// to avoid the handler-generated slug being treated as user-provided.
	if createReq.Name == "" {
		if urlStr, ok := createReq.Config["url"].(string); ok && urlStr != "" {
			parsed, err := urlparse.Parse(urlStr)
			if err == nil {
				name, _ := parsed.SuggestNameSlug()
				createReq.Name = name
			}
		} else if domain, ok := createReq.Config["domain"].(string); ok && domain != "" {
			// For domain checks that don't use URL-based creation
			createReq.Name = "Domain: " + domain
		}
	}

	check, err := h.svc.CreateCheck(req.Context(), orgSlug, createReq)
	if err != nil {
		return h.handleCreateError(writer, req, err)
	}

	// Product analytics (spec 2026-08-02-08). No-op unless PostHog is
	// configured. Only the check TYPE is sent — never the target host, URL,
	// name, slug or any other part of the check configuration.
	captureCheckCreated(req, createReq.Type)

	return h.WriteJSON(writer, http.StatusCreated, check)
}

// captureCheckCreated records the check_created product event. Pseudonymous by
// construction: org UID + user UID only, plus the low-cardinality check type.
func captureCheckCreated(req *http.Request, checkType string) {
	var orgUID, userUID string
	if org, ok := mw.GetOrganizationFromContext(req.Context()); ok && org != nil {
		orgUID = org.UID
	}

	if claims, ok := mw.GetClaimsFromContext(req.Context()); ok && claims != nil {
		userUID = claims.UserUID
	}

	analytics.Capture(req.Context(), analytics.Event{
		Name:       analytics.EventCheckCreated,
		OrgUID:     orgUID,
		UserUID:    userUID,
		Properties: map[string]any{"checkType": checkType},
	})
}

// GetCheck handles retrieving a single check by UID or slug.
func (h *Handler) GetCheck(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	identifier := httpx.Param(req, "checkUid")

	// Parse the "with" query parameter
	opts := GetCheckOptions{}
	withParam := req.URL.Query().Get("with")
	if withParam != "" {
		parts := strings.Split(withParam, ",")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			switch trimmed {
			case "last_result":
				opts.IncludeLastResult = true
			case "last_status_change":
				opts.IncludeLastStatusChange = true
			}
		}
	}

	check, err := h.svc.GetCheck(req.Context(), orgSlug, identifier, opts)
	if err != nil {
		return h.handleGetError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, check)
}

// UpdateCheck handles updating an existing check by UID or slug.
func (h *Handler) UpdateCheck(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	identifier := httpx.Param(req, "checkUid")

	var updateReq UpdateCheckRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	check, err := h.svc.UpdateCheck(req.Context(), orgSlug, identifier, &updateReq)
	if err != nil {
		return h.handleUpdateError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, check)
}

// RotateHeartbeatToken handles regenerating the ping token of a heartbeat
// check. Every previously issued ping URL stops working immediately (see
// Service.RotateHeartbeatToken for why there's no grace period). 400 for
// non-heartbeat checks, 404 for unknown checks.
func (h *Handler) RotateHeartbeatToken(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	identifier := httpx.Param(req, "checkUid")

	check, err := h.svc.RotateHeartbeatToken(req.Context(), orgSlug, identifier)
	if err != nil {
		return h.handleRotateHeartbeatTokenError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, check)
}

// handleRotateHeartbeatTokenError handles errors from RotateHeartbeatToken.
func (h *Handler) handleRotateHeartbeatTokenError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	case errors.Is(err, ErrNotHeartbeatCheck):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeValidationError, "Check is not a heartbeat check", err)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

// UpsertCheck handles creating or updating a check by slug (idempotent operation).
func (h *Handler) UpsertCheck(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	slug := httpx.Param(req, "slug")

	var upsertReq UpsertCheckRequest
	if err := json.NewDecoder(req.Body).Decode(&upsertReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	// Validate config is required
	if upsertReq.Config == nil {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "config", Message: "Config is required"},
		})
	}

	// Infer type from URL if not specified
	if upsertReq.Type == "" {
		inferredType := registry.InferCheckTypeFromConfig(upsertReq.Config)
		if inferredType == "" {
			return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
				{Name: fieldType, Message: "Type is required when url is not provided or has unrecognized scheme"},
			})
		}

		upsertReq.Type = string(inferredType)
	}

	// Auto-generate name from URL if not provided
	if upsertReq.Name == "" {
		if urlStr, ok := upsertReq.Config["url"].(string); ok && urlStr != "" {
			parsed, err := urlparse.Parse(urlStr)
			if err == nil {
				name, _ := parsed.SuggestNameSlug()
				upsertReq.Name = name
			}
		} else if domain, ok := upsertReq.Config["domain"].(string); ok && domain != "" {
			// For domain checks that don't use URL-based creation
			upsertReq.Name = "Domain: " + domain
		}
	}

	check, created, err := h.svc.UpsertCheck(req.Context(), orgSlug, slug, &upsertReq)
	if err != nil {
		return h.handleUpsertError(writer, req, err)
	}

	if created {
		return h.WriteJSON(writer, http.StatusCreated, check)
	}

	return h.WriteJSON(writer, http.StatusOK, check)
}

// DeleteCheck handles deleting a check by UID or slug.
func (h *Handler) DeleteCheck(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	identifier := httpx.Param(req, "checkUid")

	if err := h.svc.DeleteCheck(req.Context(), orgSlug, identifier); err != nil {
		return h.handleDeleteError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}

// CloneCheck handles POST /api/v1/orgs/:org/checks/:checkUid/clone.
// Empty body is valid; defaults produce an enabled clone with `(copy)` /
// `-copy` suffixes on name/slug.
func (h *Handler) CloneCheck(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	identifier := httpx.Param(req, "checkUid")

	var cloneReq CloneCheckRequest
	if err := json.NewDecoder(req.Body).Decode(&cloneReq); err != nil && !errors.Is(err, io.EOF) {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	check, err := h.svc.CloneCheck(req.Context(), orgSlug, identifier, &cloneReq)
	if err != nil {
		return h.handleCloneError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, check)
}

// ExportChecks handles exporting all checks for an organization as JSON.
func (h *Handler) ExportChecks(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	query := req.URL.Query()

	opts := ListChecksOptions{}

	// Parse optional type filter
	if typeParam := query.Get("type"); typeParam != "" {
		opts.Query = typeParam // Reuse query for type filtering via labels
	}

	// Parse labels filter
	labelsParam := query.Get("labels")
	if labelsParam != "" {
		opts.Labels = make(map[string]string)
		pairs := strings.Split(labelsParam, ",")
		for _, pair := range pairs {
			kv := strings.SplitN(pair, ":", 2)
			if len(kv) == 2 {
				opts.Labels[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
			}
		}
	}

	// Parse checkGroupUid filter
	if checkGroupUID := query.Get("checkGroupUid"); checkGroupUID != "" {
		opts.CheckGroupUID = &checkGroupUID
	}

	doc, err := h.svc.ExportChecks(req.Context(), orgSlug, opts)
	if err != nil {
		return h.handleListError(writer, req, err)
	}

	// Render the v2 wire format: pretty-printed, with the defaults block and
	// duration strings. The document is meant to be read and diffed by humans.
	body, err := MarshalExportDocument(doc)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	// Set download headers
	writer.Header().Set("Content-Disposition",
		"attachment; filename=\"solidping-checks-"+orgSlug+".json\"")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body)

	return nil
}

// ImportChecks handles importing checks from an export document. The body is
// accepted as **JSON or YAML** (sniffed from Content-Type and the first
// non-space byte, exactly like /apply): export emits JSON, but a hand-authored
// or converted manifest is just as likely to be YAML, and both parse to the
// same document.
func (h *Handler) ImportChecks(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	dryRun := req.URL.Query().Get("dryRun") == queryTrue

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return h.WriteValidationError(writer, "Invalid body", []base.ValidationErrorField{
			{Name: fieldBody, Message: "could not read request body"},
		})
	}

	doc, err := ParseManifest(body, req.Header.Get("Content-Type"))
	if err != nil {
		return h.WriteValidationError(writer, "Invalid document", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	result, err := h.svc.ImportChecks(req.Context(), orgSlug, doc, dryRun)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrganizationNotFound):
			return h.WriteErrorErr(
				writer, req, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		default:
			return h.WriteErrorErr(
				writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// ApplyChecks handles POST /api/v1/orgs/:org/checks/apply — the reconcile
// sibling of import. It accepts a JSON or YAML manifest (the existing export
// document shape, plus an optional managed-label scope and secret references)
// and reconciles the managed scope against it. Admin-only (gated by route
// middleware). Query flags: dryRun, prune, force.
func (h *Handler) ApplyChecks(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	query := req.URL.Query()

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return h.WriteValidationError(writer, "Invalid body", []base.ValidationErrorField{
			{Name: fieldBody, Message: "could not read request body"},
		})
	}

	doc, err := ParseManifest(body, req.Header.Get("Content-Type"))
	if err != nil {
		return h.WriteValidationError(writer, "Invalid manifest", []base.ValidationErrorField{
			{Name: fieldBody, Message: err.Error()},
		})
	}

	opts := ApplyOptions{
		DryRun: query.Get("dryRun") == queryTrue,
		Prune:  query.Get("prune") == queryTrue,
		Force:  query.Get("force") == queryTrue,
	}
	if capStr := query.Get("deletionCap"); capStr != "" {
		if parsed, convErr := strconv.Atoi(capStr); convErr == nil && parsed >= 0 {
			opts.DeletionCap = parsed
		}
	}

	result, err := h.svc.ApplyChecks(req.Context(), orgSlug, doc, opts)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrganizationNotFound):
			return h.WriteErrorErr(
				writer, req, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		case errors.Is(err, ErrDeletionCapExceeded):
			return h.WriteErrorErr(
				writer, req, http.StatusConflict, base.ErrorCodeConflict, err.Error(), err)
		case errors.Is(err, ErrUnresolvedSecretRef):
			return h.WriteErrorErr(
				writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
		default:
			return h.WriteErrorErr(
				writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// handleListError handles errors from ListChecks.
func (h *Handler) handleListError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrInvalidCursor):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid cursor parameter", err)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

// writeInternalFieldError renders the refusal of a client-supplied `internal`
// as a field-level VALIDATION_ERROR (spec 2026-08-27-01). Naming the field is
// the whole point: the caller has to be able to tell WHICH property of its
// payload the server will not take, or it just retries the same body.
func (h *Handler) writeInternalFieldError(writer http.ResponseWriter) error {
	return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
		{Name: fieldInternal, Message: msgInternalNotWritable},
	})
}

// handleCreateError handles errors from CreateCheck.
func (h *Handler) handleCreateError(writer http.ResponseWriter, request *http.Request, err error) error {
	// Check for configuration validation errors
	if configErr := checkerdef.IsConfigError(err); configErr != nil {
		return h.WriteValidationError(writer, "Configuration validation failed", []base.ValidationErrorField{
			{
				Name:    configErr.Parameter,
				Message: configErr.Message,
			},
		})
	}

	switch {
	case errors.Is(err, ErrInternalFieldNotWritable):
		return h.writeInternalFieldError(writer)
	case errors.Is(err, entcore.ErrEntitlementExceeded):
		var qe *entcore.QuotaError
		if !errors.As(err, &qe) {
			return h.WriteInternalError(writer, request, err)
		}
		body := entitlementshandler.FormatQuotaError(qe)
		body["code"] = string(base.ErrorCodeQuotaExceeded)

		return h.WriteJSON(writer, http.StatusPaymentRequired, body)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrInvalidCheckType):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid check type", err)
	case errors.Is(err, ErrNoAgentsToSealTo):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
	case isCheckFieldValidationError(err):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{
				Name:    fieldSlug,
				Message: msgSlugConflictOrg,
			},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{
				Name: fieldSlug,
				Message: "Slug must start with a lowercase letter, be 3-100 characters, " +
					"and contain only lowercase letters, digits, or hyphens. UUIDs are not allowed.",
			},
		})
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

// handleGetError handles errors from GetCheck.
func (h *Handler) handleGetError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

// handleUpdateError handles errors from UpdateCheck.
func (h *Handler) handleUpdateError(writer http.ResponseWriter, request *http.Request, err error) error {
	// Configuration validation errors (e.g. the uniform per-check timeout
	// cap) surface as field-level VALIDATION_ERRORs, same as on create.
	if configErr := checkerdef.IsConfigError(err); configErr != nil {
		return h.WriteValidationError(writer, "Configuration validation failed", []base.ValidationErrorField{
			{
				Name:    configErr.Parameter,
				Message: configErr.Message,
			},
		})
	}

	switch {
	case errors.Is(err, ErrInternalFieldNotWritable):
		return h.writeInternalFieldError(writer)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	case errors.Is(err, ErrTunnelRegionNarrowed):
		return h.WriteErrorErr(
			writer, request, http.StatusConflict, base.ErrorCodeConflict, err.Error(), err)
	case errors.Is(err, ErrNoAgentsToSealTo):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
	case isCheckFieldValidationError(err):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{
				Name:    fieldSlug,
				Message: msgSlugConflictOrg,
			},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{
				Name: fieldSlug,
				Message: "Slug must start with a lowercase letter, be 3-100 characters, " +
					"and contain only lowercase letters, digits, or hyphens. UUIDs are not allowed.",
			},
		})
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

// isCheckFieldValidationError reports whether err is one of the request-field
// validation failures (incident periods, flapping knobs, or per-type period
// bounds) that should surface as a 400 VALIDATION_ERROR rather than a 500.
// These are returned (possibly wrapped) by CreateCheck / UpdateCheck.
func isCheckFieldValidationError(err error) bool {
	var periodErr *periodBoundError

	return errors.Is(err, errIncidentPeriodOutOfRange) ||
		errors.Is(err, errRegionSpreadOutOfRange) ||
		// A legacy `@<org>/<slug>` region naming somebody ELSE's org is a
		// caller mistake (or an attempt), not a server fault — 400, never 500.
		errors.Is(err, regions.ErrForeignPrivateRegion) ||
		errors.Is(err, regions.ErrInvalidPrivateRegionSlug) ||
		errors.Is(err, errFlappingWindowNegative) ||
		errors.Is(err, errFlapBackoffTooSmall) ||
		errors.Is(err, errMaxRecoveryMultTooSmall) ||
		errors.Is(err, errInvalidTraceroutePolicy) ||
		errors.As(err, &periodErr)
}

// handleUpsertError handles errors from UpsertCheck.
func (h *Handler) handleUpsertError(writer http.ResponseWriter, request *http.Request, err error) error {
	if configErr := checkerdef.IsConfigError(err); configErr != nil {
		return h.WriteValidationError(writer, "Configuration validation failed", []base.ValidationErrorField{
			{
				Name:    configErr.Parameter,
				Message: configErr.Message,
			},
		})
	}

	switch {
	case errors.Is(err, ErrInternalFieldNotWritable):
		return h.writeInternalFieldError(writer)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrInvalidCheckType):
		return h.WriteValidationError(writer, "Invalid check type", []base.ValidationErrorField{
			{
				Name:    fieldType,
				Message: "Unsupported check type",
			},
		})
	case isCheckFieldValidationError(err):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

// handleDeleteError handles errors from DeleteCheck.
func (h *Handler) handleDeleteError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrTunnelInUse):
		return h.WriteErrorErr(
			writer, request, http.StatusConflict, base.ErrorCodeConflict, err.Error(), err)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

// handleCloneError handles errors from CloneCheck.
func (h *Handler) handleCloneError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, entcore.ErrEntitlementExceeded):
		var qe *entcore.QuotaError
		if !errors.As(err, &qe) {
			return h.WriteInternalError(writer, request, err)
		}
		body := entitlementshandler.FormatQuotaError(qe)
		body["code"] = string(base.ErrorCodeQuotaExceeded)

		return h.WriteJSON(writer, http.StatusPaymentRequired, body)
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{
				Name:    fieldSlug,
				Message: msgSlugConflictOrg,
			},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{
				Name: fieldSlug,
				Message: "Slug must start with a lowercase letter, be 3-100 characters, " +
					"and contain only lowercase letters, digits, or hyphens. UUIDs are not allowed.",
			},
		})
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
