package mcp

import (
	"context"
)

func listCheckGroupsDef() ToolDefinition {
	return ToolDefinition{
		Name:        "list_check_groups",
		Description: "List all check groups for the organization.",
		InputSchema: objectSchema(map[string]any{}, nil),
	}
}

func (h *Handler) toolListCheckGroups(ctx context.Context, orgSlug string, _ map[string]any) ToolCallResult {
	result, err := h.checkGroupsSvc.ListCheckGroups(ctx, orgSlug)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(map[string]any{schemaKeyData: result})
}
