// Package statuspages provides HTTP handlers for status page management endpoints.
package statuspages

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

const slugValidationMsg = "Slug must start with a lowercase letter, be 3-40 characters, " +
	"and contain only lowercase letters, digits, or hyphens. UUIDs are not allowed."

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
func (h *Handler) ListStatusPages(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	pages, err := h.svc.ListStatusPages(req.Context(), orgSlug)
	if err != nil {
		return h.handleOrgError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"data": pages,
	})
}

// CreateStatusPage handles creating a new status page.
func (h *Handler) CreateStatusPage(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	var createReq CreateStatusPageRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	page, err := h.svc.CreateStatusPage(req.Context(), orgSlug, &createReq)
	if err != nil {
		return h.handleCreatePageError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, page)
}

// GetStatusPage handles retrieving a single status page by UID or slug.
func (h *Handler) GetStatusPage(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("statusPageUid")

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
func (h *Handler) UpdateStatusPage(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("statusPageUid")

	var updateReq UpdateStatusPageRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	page, err := h.svc.UpdateStatusPage(req.Context(), orgSlug, identifier, &updateReq)
	if err != nil {
		return h.handleUpdatePageError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, page)
}

// DeleteStatusPage handles deleting a status page.
func (h *Handler) DeleteStatusPage(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("statusPageUid")

	if err := h.svc.DeleteStatusPage(req.Context(), orgSlug, identifier); err != nil {
		return h.handlePageError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}

// --- Section handlers ---

// ListSections handles listing all sections for a status page.
func (h *Handler) ListSections(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	pageIdentifier := req.Param("statusPageUid")

	sections, err := h.svc.ListSections(req.Context(), orgSlug, pageIdentifier)
	if err != nil {
		return h.handlePageError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"data": sections,
	})
}

// CreateSection handles creating a new section within a status page.
func (h *Handler) CreateSection(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	pageIdentifier := req.Param("statusPageUid")

	var createReq CreateSectionRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	section, err := h.svc.CreateSection(req.Context(), orgSlug, pageIdentifier, createReq)
	if err != nil {
		return h.handleCreateSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, section)
}

// GetSection handles retrieving a single section.
func (h *Handler) GetSection(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	pageIdentifier := req.Param("statusPageUid")
	sectionIdentifier := req.Param("sectionUid")

	section, err := h.svc.GetSection(req.Context(), orgSlug, pageIdentifier, sectionIdentifier)
	if err != nil {
		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, section)
}

// UpdateSection handles updating an existing section.
func (h *Handler) UpdateSection(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	pageIdentifier := req.Param("statusPageUid")
	sectionIdentifier := req.Param("sectionUid")

	var updateReq UpdateSectionRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	section, err := h.svc.UpdateSection(req.Context(), orgSlug, pageIdentifier, sectionIdentifier, updateReq)
	if err != nil {
		return h.handleUpdateSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, section)
}

// DeleteSection handles deleting a section.
func (h *Handler) DeleteSection(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	pageIdentifier := req.Param("statusPageUid")
	sectionIdentifier := req.Param("sectionUid")

	if err := h.svc.DeleteSection(req.Context(), orgSlug, pageIdentifier, sectionIdentifier); err != nil {
		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}

// --- Resource handlers ---

// ListResources handles listing all resources for a section.
func (h *Handler) ListResources(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	pageIdentifier := req.Param("statusPageUid")
	sectionIdentifier := req.Param("sectionUid")

	resources, err := h.svc.ListResources(req.Context(), orgSlug, pageIdentifier, sectionIdentifier)
	if err != nil {
		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"data": resources,
	})
}

// CreateResource handles adding a check to a section.
func (h *Handler) CreateResource(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	pageIdentifier := req.Param("statusPageUid")
	sectionIdentifier := req.Param("sectionUid")

	var createReq CreateResourceRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	resource, err := h.svc.CreateResource(req.Context(), orgSlug, pageIdentifier, sectionIdentifier, createReq)
	if err != nil {
		return h.handleCreateResourceError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, resource)
}

// UpdateResource handles updating a resource.
func (h *Handler) UpdateResource(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	pageIdentifier := req.Param("statusPageUid")
	sectionIdentifier := req.Param("sectionUid")
	resourceUID := req.Param("resourceUid")

	var updateReq UpdateResourceRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	resource, err := h.svc.UpdateResource(
		req.Context(), orgSlug, pageIdentifier, sectionIdentifier, resourceUID, updateReq,
	)
	if err != nil {
		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resource)
}

// DeleteResource handles removing a check from a section.
func (h *Handler) DeleteResource(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	pageIdentifier := req.Param("statusPageUid")
	sectionIdentifier := req.Param("sectionUid")
	resourceUID := req.Param("resourceUid")

	if err := h.svc.DeleteResource(
		req.Context(), orgSlug, pageIdentifier, sectionIdentifier, resourceUID,
	); err != nil {
		return h.handleSectionError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}

// --- Public handlers ---

// ViewStatusPage handles the public view of a status page.
func (h *Handler) ViewStatusPage(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	slug := req.Param("slug")

	page, err := h.svc.ViewStatusPage(req.Context(), orgSlug, slug)
	if err != nil {
		return h.handlePublicError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, page)
}

// ViewDefaultStatusPage handles the public view of an organization's default status page.
func (h *Handler) ViewDefaultStatusPage(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	page, err := h.svc.ViewDefaultStatusPage(req.Context(), orgSlug)
	if err != nil {
		return h.handlePublicError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, page)
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

func (h *Handler) handleCreatePageError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{Name: "slug", Message: "A status page with this slug already exists in this organization"},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{Name: "slug", Message: slugValidationMsg},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) handleUpdatePageError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{Name: "slug", Message: "A status page with this slug already exists in this organization"},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{Name: "slug", Message: slugValidationMsg},
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
			{Name: "slug", Message: "A section with this slug already exists in this status page"},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{Name: "slug", Message: slugValidationMsg},
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
			{Name: "slug", Message: "A section with this slug already exists in this status page"},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{Name: "slug", Message: slugValidationMsg},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) handleCreateResourceError(writer http.ResponseWriter, err error) error {
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
	default:
		return h.WriteInternalError(writer, err)
	}
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
