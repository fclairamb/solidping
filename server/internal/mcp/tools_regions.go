package mcp

import (
	"context"
)

func listRegionsDef() ToolDefinition {
	return ToolDefinition{
		Name:        "list_regions",
		Description: "List available monitoring regions for the organization.",
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
