package mcp

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

func listCheckTypesDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_check_types",
		Description: "List all monitoring check types supported by this server " +
			"(e.g. http, tcp, dns, icmp, ssl). Use this first when you don't know what " +
			"type to use. Then call get_check_type_samples for the chosen type to get a " +
			"starting config.",
		InputSchema: objectSchema(map[string]any{}, nil),
	}
}

func (h *Handler) toolListCheckTypes(_ context.Context, _ string, _ map[string]any) ToolCallResult {
	return marshalResult(h.checkTypesSvc.ListServerCheckTypes())
}

func getCheckTypeSamplesDef() ToolDefinition {
	return ToolDefinition{
		Name: "get_check_type_samples",
		Description: "Return ready-made sample configs for the given check type. " +
			"Each sample is a complete, valid config you can clone and modify. Use this " +
			"to learn the config shape for a type — much more reliable than guessing field names.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyType: stringProp(
				"Check type to get samples for (e.g. \"http\", \"dns\", \"tcp\", \"icmp\", \"ssl\").",
			),
		}, []string{schemaKeyType}),
	}
}

func (h *Handler) toolGetCheckTypeSamples(_ context.Context, _ string, args map[string]any) ToolCallResult {
	typeStr := getStringArg(args, schemaKeyType)
	if typeStr == "" {
		return errorResult("type is required")
	}
	return marshalResult(h.checkTypesSvc.ListSampleConfigs(typeStr))
}

func validateCheckDef() ToolDefinition {
	return ToolDefinition{
		Name: "validate_check",
		Description: "Dry-run validate a check config without creating the check. " +
			"Returns {valid: true} on success or {valid: false, fields: [...]} listing the " +
			"specific fields with errors. Use this before create_check when you've assembled " +
			"a config from scratch or modified a sample, to catch problems early.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyType: stringProp(
				"Check type (e.g. \"http\", \"dns\", \"tcp\", \"icmp\", \"ssl\").",
			),
			schemaKeyConfig: objectProp(
				"Check-specific config to validate (e.g., {\"url\": \"https://example.com\"}).",
			),
		}, []string{schemaKeyType, schemaKeyConfig}),
	}
}

func (h *Handler) toolValidateCheck(ctx context.Context, _ string, args map[string]any) ToolCallResult {
	typeStr := getStringArg(args, schemaKeyType)
	if typeStr == "" {
		return errorResult("type is required")
	}
	config := getMapArg(args, schemaKeyConfig)
	if config == nil {
		return errorResult("config is required")
	}
	result, err := h.checksSvc.ValidateCheck(ctx, checks.ValidateCheckRequest{
		Type:   typeStr,
		Config: config,
	})
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(result)
}
