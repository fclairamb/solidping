package mcp

import (
	"context"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
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
		Name: toolValidateCheck,
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

func (h *Handler) toolValidateCheck(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	typeStr := getStringArg(args, schemaKeyType)
	if typeStr == "" {
		return errorResult("type is required")
	}
	config := getMapArg(args, schemaKeyConfig)
	if config == nil {
		return errorResult("config is required")
	}
	result, err := h.checksSvc.ValidateCheck(ctx, orgSlug, &checks.ValidateCheckRequest{
		Type:   typeStr,
		Config: config,
	})
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(result)
}

// allowedCheckTypes is the "Allowed:" list the create_check and list_results
// tool descriptions quote. Derived from the check-type registry — the same
// table list_check_types serves — so it can never go stale again: it used to
// be a hand-written list of seven types (spec 2026-09-25-05). The synthetic
// `sleep` type is not a customer check type and is left out.
func allowedCheckTypes() string {
	types := checkerdef.ListCheckTypes(nil)
	names := make([]string, 0, len(types))

	for _, checkType := range types {
		if checkType == checkerdef.CheckTypeSleep {
			continue
		}

		names = append(names, string(checkType))
	}

	return strings.Join(names, ", ")
}
