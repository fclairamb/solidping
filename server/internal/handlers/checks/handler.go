// Package checks provides HTTP handlers for check management endpoints.
package checks

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

const (
	fieldType      = "type"
	fieldSlug      = "slug"
	msgInvalidJSON = "Invalid JSON format"
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
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	var validateReq ValidateCheckRequest
	if err := json.NewDecoder(req.Body).Decode(&validateReq); err != nil {
		return h.WriteValidationError(
			writer, "Invalid JSON", []base.ValidationErrorField{
				{Name: "body", Message: msgInvalidJSON},
			})
	}

	resp, err := h.svc.ValidateCheck(req.Context(), validateReq)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// ListChecks handles listing all checks for an organization.
//
//nolint:funlen // List handler has many query parameter extractions
func (h *Handler) ListChecks(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
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
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid limit parameter", err)
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

	// Parse internal filter
	if internalParam := query.Get("internal"); internalParam != "" {
		opts.Internal = &internalParam
	}

	response, err := h.svc.ListChecks(req.Context(), orgSlug, opts)
	if err != nil {
		return h.handleListError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// CreateCheck handles creating a new check for an organization.
func (h *Handler) CreateCheck(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	var createReq CreateCheckRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
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
		return h.handleCreateError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, check)
}

// GetCheck handles retrieving a single check by UID or slug.
func (h *Handler) GetCheck(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("checkUid")

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
		return h.handleGetError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, check)
}

// UpdateCheck handles updating an existing check by UID or slug.
func (h *Handler) UpdateCheck(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("checkUid")

	var updateReq UpdateCheckRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	check, err := h.svc.UpdateCheck(req.Context(), orgSlug, identifier, &updateReq)
	if err != nil {
		return h.handleUpdateError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, check)
}

// UpsertCheck handles creating or updating a check by slug (idempotent operation).
func (h *Handler) UpsertCheck(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	slug := req.Param("slug")

	var upsertReq UpsertCheckRequest
	if err := json.NewDecoder(req.Body).Decode(&upsertReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
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
		return h.handleUpsertError(writer, err)
	}

	if created {
		return h.WriteJSON(writer, http.StatusCreated, check)
	}

	return h.WriteJSON(writer, http.StatusOK, check)
}

// DeleteCheck handles deleting a check by UID or slug.
func (h *Handler) DeleteCheck(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("checkUid")

	if err := h.svc.DeleteCheck(req.Context(), orgSlug, identifier); err != nil {
		return h.handleDeleteError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}

// ExportChecks handles exporting all checks for an organization as JSON.
func (h *Handler) ExportChecks(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
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
		return h.handleListError(writer, err)
	}

	// Set download headers
	writer.Header().Set("Content-Disposition",
		"attachment; filename=\"solidping-checks-"+orgSlug+".json\"")

	return h.WriteJSON(writer, http.StatusOK, doc)
}

// ImportChecks handles importing checks from a JSON export document.
func (h *Handler) ImportChecks(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	dryRun := req.URL.Query().Get("dryRun") == "true"

	var doc ExportDocument
	if err := json.NewDecoder(req.Body).Decode(&doc); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: msgInvalidJSON},
		})
	}

	result, err := h.svc.ImportChecks(req.Context(), orgSlug, &doc, dryRun)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrganizationNotFound):
			return h.WriteErrorErr(
				writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		default:
			return h.WriteErrorErr(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// handleListError handles errors from ListChecks.
func (h *Handler) handleListError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrInvalidCursor):
		return h.WriteErrorErr(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid cursor parameter", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleCreateError handles errors from CreateCheck.
func (h *Handler) handleCreateError(writer http.ResponseWriter, err error) error {
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
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrInvalidCheckType):
		return h.WriteErrorErr(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid check type", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{
				Name:    fieldSlug,
				Message: "A check with this slug already exists in this organization",
			},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{
				Name: fieldSlug,
				Message: "Slug must start with a lowercase letter, be 3-20 characters, " +
					"and contain only lowercase letters, digits, or hyphens. UUIDs are not allowed.",
			},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleGetError handles errors from GetCheck.
func (h *Handler) handleGetError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleUpdateError handles errors from UpdateCheck.
func (h *Handler) handleUpdateError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{
				Name:    fieldSlug,
				Message: "A check with this slug already exists in this organization",
			},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{
				Name: fieldSlug,
				Message: "Slug must start with a lowercase letter, be 3-20 characters, " +
					"and contain only lowercase letters, digits, or hyphens. UUIDs are not allowed.",
			},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleUpsertError handles errors from UpsertCheck.
func (h *Handler) handleUpsertError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrInvalidCheckType):
		return h.WriteValidationError(writer, "Invalid check type", []base.ValidationErrorField{
			{
				Name:    fieldType,
				Message: "Unsupported check type",
			},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}

// handleDeleteError handles errors from DeleteCheck.
func (h *Handler) handleDeleteError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}
