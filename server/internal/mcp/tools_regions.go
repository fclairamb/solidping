package mcp

import (
	"context"
)

func listRegionsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_regions",
		Description: "List monitoring regions available to the organization (e.g. eu-west-1, " +
			"us-east-1). Returns the slug, label, and any per-region metadata. Use these " +
			"slugs in the regions array of create_check or update_check.",
		InputSchema: objectSchema(map[string]any{}, nil),
	}
}

func (h *Handler) toolListRegions(ctx context.Context, orgSlug string, _ map[string]any) ToolCallResult {
	result, err := h.regionsSvc.ListOrgRegions(ctx, orgSlug)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}
