package mcp

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/handlers/connections"
)

func listConnectionsDef() ToolDefinition {
	return ToolDefinition{
		Name:        "list_connections",
		Description: "List notification connections (Slack, webhook, email).",
		InputSchema: objectSchema(map[string]any{
			"type": stringProp("Filter by type: slack, webhook, email"),
		}, nil),
	}
}

func (h *Handler) toolListConnections(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	var connType *string
	if v := getStringArg(args, "type"); v != "" {
		connType = &v
	}

	result, err := h.connectionsSvc.ListConnections(ctx, orgSlug, connType)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func createConnectionDef() ToolDefinition {
	return ToolDefinition{
		Name:        "create_connection",
		Description: "Create a new notification connection.",
		InputSchema: objectSchema(map[string]any{
			"type":      stringProp("Connection type: slack, webhook, email"),
			"name":      stringProp("Display name"),
			"enabled":   boolProp("Default true"),
			"isDefault": boolProp("Auto-attach to new checks"),
			"settings":  objectProp("Type-specific settings (e.g., {\"webhookUrl\": \"...\"} for webhook)"),
		}, []string{"type", "name"}),
	}
}

func (h *Handler) toolCreateConnection(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	connType := getStringArg(args, "type")
	name := getStringArg(args, "name")
	if connType == "" || name == "" {
		return errorResult("type and name are required")
	}

	req := connections.CreateConnectionRequest{
		Type:      connType,
		Name:      name,
		Enabled:   getBoolArg(args, "enabled"),
		IsDefault: getBoolArg(args, "isDefault"),
		Settings:  getMapArg(args, "settings"),
	}

	result, err := h.connectionsSvc.CreateConnection(ctx, orgSlug, req)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}
