package mcp

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/handlers/connections"
)

func listConnectionsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_connections",
		Description: "List notification connections (Slack, webhook, email) configured for the " +
			"organization. Use this to discover what notification channels are available " +
			"before attaching them to a check.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyType: stringProp(
				"Filter by connection type. Allowed: slack, webhook, email. " +
					"Example: \"slack\".",
			),
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
		Name: "create_connection",
		Description: "Create a new notification connection (Slack, webhook, or email) that can " +
			"be attached to checks for incident notifications.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyType: stringProp(
				"Connection type. Allowed: slack, webhook, email. Example: \"webhook\".",
			),
			schemaKeyName:    stringProp("Display name shown in the UI, e.g. \"Engineering Slack\"."),
			schemaKeyEnabled: boolProp("Whether the connection is active. Default true."),
			"isDefault": boolProp(
				"If true, the connection is auto-attached to newly-created checks.",
			),
			"settings": objectProp(
				"Type-specific settings. For webhook: {\"webhookUrl\":\"https://...\"}. " +
					"For slack: {\"channel\":\"#alerts\",\"webhookUrl\":\"https://hooks.slack.com/...\"}. " +
					"For email: {\"to\":\"oncall@example.com\"}.",
			),
		}, []string{schemaKeyType, schemaKeyName}),
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
