package mcp

import (
	"context"
	"encoding/json"
	"net/http"
)

func (h *Handler) getResourceDefinitions() []ResourceDefinition {
	return []ResourceDefinition{
		{
			URI:         "solidping://organization",
			Name:        "Organization",
			Description: "Current organization metadata (slug, name).",
			MimeType:    "application/json",
		},
		{
			URI:         "solidping://regions",
			Name:        "Regions",
			Description: "Available monitoring regions.",
			MimeType:    "application/json",
		},
	}
}

func (h *Handler) handleResourcesList(req *Request) (*Response, int) {
	resp := successResponse(req.ID, ResourcesListResult{Resources: h.getResourceDefinitions()})
	return &resp, http.StatusOK
}

func (h *Handler) handleResourcesRead(
	ctx context.Context, req *Request, orgSlug string,
) (*Response, int) {
	var params ResourceReadParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		resp := errorResponse(req.ID, CodeInvalidParams, "Invalid params")
		return &resp, http.StatusOK
	}

	switch params.URI {
	case "solidping://organization":
		return h.readOrganizationResource(ctx, req, orgSlug)
	case "solidping://regions":
		return h.readRegionsResource(ctx, req, orgSlug)
	default:
		resp := errorResponse(req.ID, CodeNotFound, "Resource not found: "+params.URI)
		return &resp, http.StatusOK
	}
}

func (h *Handler) readOrganizationResource(
	ctx context.Context, req *Request, orgSlug string,
) (*Response, int) {
	org, err := h.dbService.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		resp := errorResponse(req.ID, CodeNotFound, "Organization not found")
		return &resp, http.StatusOK
	}

	data, errMarshal := json.Marshal(map[string]string{
		"slug": org.Slug,
		"name": org.Name,
	})
	if errMarshal != nil {
		resp := errorResponse(req.ID, CodeInternalError, "Failed to marshal organization")
		return &resp, http.StatusOK
	}

	resp := successResponse(req.ID, ResourceReadResult{
		Contents: []ResourceContent{
			{URI: "solidping://organization", MimeType: "application/json", Text: string(data)},
		},
	})
	return &resp, http.StatusOK
}

func (h *Handler) readRegionsResource(
	ctx context.Context, req *Request, orgSlug string,
) (*Response, int) {
	result, err := h.regionsSvc.ListOrgRegions(ctx, orgSlug)
	if err != nil {
		resp := errorResponse(req.ID, CodeInternalError, "Failed to list regions")
		return &resp, http.StatusOK
	}

	data, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		resp := errorResponse(req.ID, CodeInternalError, "Failed to marshal regions")
		return &resp, http.StatusOK
	}

	resp := successResponse(req.ID, ResourceReadResult{
		Contents: []ResourceContent{
			{URI: "solidping://regions", MimeType: "application/json", Text: string(data)},
		},
	})
	return &resp, http.StatusOK
}
