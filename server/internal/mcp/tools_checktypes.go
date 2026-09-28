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
			"starting config. Returns {data: [...]} — one entry per type with its " +
			"description, enabled state, period bounds and secretFields. " +
			"Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{}, nil),
		OutputSchema: dataOutputSchema(
			"Check types this server supports, with their activation status.",
			checkTypeOutputProps(),
		),
		Annotations: readOnlyAnnotations("List check types"),
	}
}

// checkTypeOutputProps documents the check-type fields agents rely on
// (CheckTypeResponse). Extra properties stay allowed by default.
func checkTypeOutputProps() map[string]any {
	return map[string]any{
		schemaKeyType:          stringProp("Check type identifier, e.g. \"http\"."),
		schemaKeyDescription:   stringProp("Human-readable description of what the type monitors."),
		"labels":               arrayOfStringsProp("Category labels for the type."),
		schemaKeyEnabled:       boolProp("Whether the type is available on this server."),
		"disabledReason":       stringProp("Why the type is disabled; absent when there is nothing to say."),
		"advisory":             stringProp("Extra availability caveat for an enabled type; absent when there is none."),
		"minPeriodSeconds":     intProp("Minimum check interval in seconds; absent when unbounded."),
		"maxPeriodSeconds":     intProp("Maximum check interval in seconds; absent when unbounded."),
		"defaultPeriodSeconds": intProp("Default check interval in seconds; absent when there is no default."),
		"supportsTunnel":       boolProp("The type can run through an SSH check's tunnel."),
		"supportsIpVersion":    boolProp("The type honors the shared ipVersion config key."),
		"secretFields":         arrayOfStringsProp("Top-level config keys the type stores encrypted."),
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
			"to learn the config shape for a type — much more reliable than guessing " +
			"field names. Returns {data: [...]} with one entry per matching check " +
			"type, each carrying its samples. Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyType: stringProp(
				"Check type to get samples for (e.g. \"http\", \"dns\", \"tcp\", \"icmp\", \"ssl\").",
			),
		}, []string{schemaKeyType}),
		OutputSchema: dataOutputSchema(
			"One entry per matching check type, carrying its sample configs.",
			map[string]any{
				"checkType": stringProp("Check type the samples belong to, e.g. \"http\"."),
				"samples": arrayOfObjectsProp(
					"Ready-made configs to clone and modify.",
					sampleConfigOutputProps(),
				),
			},
		),
		Annotations: readOnlyAnnotations("Get check type samples"),
	}
}

// sampleConfigOutputProps documents one sample config (SampleConfigResponse).
func sampleConfigOutputProps() map[string]any {
	return map[string]any{
		schemaKeyName:   stringProp("Human-readable sample name."),
		schemaKeySlug:   stringProp("URL-friendly sample slug."),
		"periodSeconds": intProp("Suggested check interval in seconds."),
		"config":        objectProp("Complete, valid config for the check type."),
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
		Description: "Dry-run validate a check config without creating or changing " +
			"anything — a pure read, no side effects. Returns {valid, fields, " +
			"warnings}: valid is false exactly when fields lists the blocking " +
			"findings (every one of them, not just the first), while warnings are " +
			"advisory notes that never block. Use this before create_check when " +
			"you've assembled a config from scratch or modified a sample, to catch " +
			"problems early. Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyType: stringProp(
				"Check type (e.g. \"http\", \"dns\", \"tcp\", \"icmp\", \"ssl\").",
			),
			schemaKeyConfig: objectProp(
				"Check-specific config to validate (e.g., {\"url\": \"https://example.com\"}).",
			),
		}, []string{schemaKeyType, schemaKeyConfig}),
		OutputSchema: objectSchema(map[string]any{
			"valid": boolProp(
				"True when the config would be accepted; false when at least one " +
					"blocking finding exists.",
			),
			"fields": arrayOfObjectsProp(
				"Blocking findings; absent when there are none (valid stays true).",
				validationFindingOutputProps(),
			),
			"warnings": arrayOfObjectsProp(
				"Advisory findings that never block; absent when there are none.",
				validationFindingOutputProps(),
			),
		}, []string{"valid"}),
		Annotations: readOnlyAnnotations("Validate check"),
	}
}

// validationFindingOutputProps documents one finding of validate_check's
// fields / warnings lists (base.ValidationErrorField).
func validationFindingOutputProps() map[string]any {
	return map[string]any{
		schemaKeyName: stringProp("Field the finding is about, e.g. \"config\" or \"slug\"."),
		"message":     stringProp("Human-readable description of the finding."),
		"severity":    stringProp("error, warning or info; absent means error."),
		"code":        stringProp("Stable machine-readable rule code, e.g. \"SLUG_TAKEN\"; absent when there is none."),
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
