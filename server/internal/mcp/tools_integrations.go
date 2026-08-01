package mcp

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

func listIntegrationsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_integrations",
		Description: "List integrations (Slack, webhook, email, …) configured for the " +
			"organization. Use this to discover what notification channels are available " +
			"before attaching them to a check.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyType: stringProp(
				"Filter by integration type. Allowed: slack, webhook, email, msteams. " +
					"Example: \"slack\".",
			),
		}, nil),
	}
}

func (h *Handler) toolListIntegrations(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	var connType *string
	if v := getStringArg(args, "type"); v != "" {
		connType = &v
	}

	result, err := h.integrationsSvc.ListIntegrations(ctx, orgSlug, connType)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func createIntegrationDef() ToolDefinition {
	return ToolDefinition{
		Name: "create_integration",
		Description: "Create a new integration (webhook, email, msteams, …) that can be " +
			"attached to checks for incident notifications. Slack cannot be created here — " +
			"install it via the dashboard OAuth flow instead.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyType: stringProp(
				"Integration type. Allowed: webhook, email, msteams. Example: \"webhook\". " +
					"(\"slack\" is rejected here — see settings below.)",
			),
			schemaKeyName:    stringProp("Display name shown in the UI, e.g. \"Engineering Slack\"."),
			schemaKeyEnabled: boolProp("Whether the integration is active. Default true."),
			"isDefault": boolProp(
				"If true, the integration is auto-attached to newly-created checks.",
			),
			"settings": objectProp(
				"Type-specific settings. For webhook: {\"url\":\"https://...\"}. " +
					"Slack cannot be created here — Slack integrations are installed via the " +
					"dashboard OAuth flow only, and creating type \"slack\" through this tool is rejected. " +
					"For email: {\"to\":\"oncall@example.com\"}.",
			),
		}, []string{schemaKeyType, schemaKeyName}),
	}
}

func (h *Handler) toolCreateIntegration(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	connType := getStringArg(args, "type")
	name := getStringArg(args, "name")
	if connType == "" || name == "" {
		return errorResult("type and name are required")
	}

	req := integrations.CreateIntegrationRequest{
		Type:      connType,
		Name:      name,
		Enabled:   getBoolArg(args, "enabled"),
		IsDefault: getBoolArg(args, "isDefault"),
		Settings:  getMapArg(args, "settings"),
	}

	result, err := h.integrationsSvc.CreateIntegration(ctx, orgSlug, req)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}
