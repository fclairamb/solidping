package mcp

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

// integrationOutputProps documents the integration fields agents rely on,
// verified against integrations.IntegrationResponse's JSON tags. includeSettings
// is false for list responses: the list path (toResponse(conn, false)) never
// carries settings, only their private-key names.
func integrationOutputProps(includeSettings bool) map[string]any {
	props := map[string]any{
		propUID:          stringProp("Integration UID."),
		schemaKeyType:    stringProp("Integration type, e.g. \"webhook\"."),
		schemaKeyName:    stringProp("Display name."),
		schemaKeyEnabled: boolProp("Whether the integration is active."),
		propIsDefault: boolProp(
			"Whether the integration is auto-attached to newly-created checks.",
		),
		"settingsPrivateKeys": arrayOfStringsProp(
			"Names of settings stored encrypted; their values never appear in responses.",
		),
		schemaKeyCreatedAt: stringProp("RFC3339 creation timestamp."),
		schemaKeyUpdatedAt: stringProp("RFC3339 last-update timestamp."),
	}
	if includeSettings {
		props["settings"] = objectProp(
			"Settings for the type; secret keys are stripped and listed in " +
				"settingsPrivateKeys — except a webhook's signing secret, returned " +
				"here so it can be displayed.",
		)
	}

	return props
}

// integrationOutputSchema is the output shape of create_integration: the
// integration object itself, settings included.
func integrationOutputSchema() map[string]any {
	return objectSchema(integrationOutputProps(true), []string{propUID, schemaKeyType, schemaKeyName})
}

func listIntegrationsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_integrations",
		Description: "List integrations (Slack, webhook, email, …) configured for the " +
			"organization, optionally filtered by type. Returns {data: [...]}. Use this " +
			"to discover what notification channels are available before attaching them " +
			"to a check; use create_integration to add a missing one. Read-only: works " +
			"with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyType: stringProp(
				"Filter by integration type. Allowed: slack, webhook, email, msteams, " +
					"msteams-bot. Example: \"slack\". (\"msteams\" is the one-way Teams " +
					"Workflow webhook; \"msteams-bot\" is the two-way Teams bot.)",
			),
		}, nil),
		OutputSchema: dataOutputSchema(
			"Integrations on this page.",
			integrationOutputProps(false),
		),
		Annotations: readOnlyAnnotations("List integrations"),
	}
}

func (h *Handler) toolListIntegrations(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	var connType *string
	if v := getStringArg(args, schemaKeyType); v != "" {
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
		Description: "Create a new integration (webhook, email, msteams, …) and return it " +
			"(uid, type, name, settings). Type and settings are validated before anything " +
			"is stored. Slack cannot be created here — install it via the dashboard OAuth " +
			"flow instead. Use list_integrations first to check whether a matching channel " +
			"already exists. Requires the mcp scope (mcp:read tokens are refused) and at " +
			"least the user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyType: stringProp(
				"Integration type. Allowed: webhook, email, msteams. Example: \"webhook\". " +
					"(\"slack\" and \"msteams-bot\" are both rejected here — they carry a " +
					"provider-side identity that must be proven, not asserted, so they are " +
					"created by their own install flows in the dashboard.)",
			),
			schemaKeyName:    stringProp("Display name shown in the UI, e.g. \"Engineering Slack\"."),
			schemaKeyEnabled: boolProp("Whether the integration is active. Default true."),
			propIsDefault: boolProp(
				"If true, the integration is auto-attached to newly-created checks.",
			),
			"settings": objectProp(
				"Type-specific settings. For webhook: {\"url\":\"https://...\"}. " +
					"Slack cannot be created here — Slack integrations are installed via the " +
					"dashboard OAuth flow only, and creating type \"slack\" through this tool is rejected. " +
					"For email: {\"to\":\"oncall@example.com\"}.",
			),
		}, []string{schemaKeyType, schemaKeyName}),
		OutputSchema: integrationOutputSchema(),
		Annotations:  createAnnotations("Create integration"),
	}
}

func (h *Handler) toolCreateIntegration(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	connType := getStringArg(args, schemaKeyType)
	name := getStringArg(args, schemaKeyName)
	if connType == "" || name == "" {
		return errorResult("type and name are required")
	}

	req := integrations.CreateIntegrationRequest{
		Type:      connType,
		Name:      name,
		Enabled:   getBoolArg(args, schemaKeyEnabled),
		IsDefault: getBoolArg(args, propIsDefault),
		Settings:  getMapArg(args, "settings"),
	}

	result, err := h.integrationsSvc.CreateIntegration(ctx, orgSlug, req)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}
