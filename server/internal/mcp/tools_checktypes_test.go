package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckTypeToolDefinitions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defs := []ToolDefinition{
		listCheckTypesDef(),
		getCheckTypeSamplesDef(),
		validateCheckDef(),
	}

	for _, def := range defs {
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			r.NotEmpty(def.Name)
			r.NotEmpty(def.Description)
			r.NotNil(def.InputSchema)
		})
	}
}

func TestCheckTypeWorkflowDescriptionsChain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// list_check_types description must point at get_check_type_samples
	listDesc, ok := listCheckTypesDef().Description, true
	r.True(ok)
	r.Contains(listDesc, "get_check_type_samples")

	// validate_check description must mention create_check
	validateDesc := validateCheckDef().Description
	r.Contains(validateDesc, "create_check")
}

func TestCheckTypeRequiredArgs(t *testing.T) {
	t.Parallel()

	handler := newTestHandler()

	tests := []struct {
		name        string
		tool        toolFunc
		args        map[string]any
		errContains string
	}{
		{
			name:        "get_check_type_samples rejects empty type",
			tool:        handler.toolGetCheckTypeSamples,
			args:        map[string]any{},
			errContains: "type is required",
		},
		{
			name:        "validate_check rejects empty type",
			tool:        handler.toolValidateCheck,
			args:        map[string]any{"config": map[string]any{"url": "https://x"}},
			errContains: "type is required",
		},
		{
			name:        "validate_check rejects missing config",
			tool:        handler.toolValidateCheck,
			args:        map[string]any{"type": "http"},
			errContains: "config is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			result := tc.tool(context.Background(), "test-org", tc.args)
			r.True(result.IsError, "expected error for %s", tc.name)
			r.Contains(result.Content[0].Text, tc.errContains)
		})
	}
}

// TestAllowedCheckTypesFollowTheRegistry: the create_check / list_results
// "Allowed:" lists come from the registry, so a new type (private-location,
// spec 2026-09-25-05) is listed without anyone editing a string.
func TestAllowedCheckTypesFollowTheRegistry(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	allowed := allowedCheckTypes()

	for _, name := range []string{"http", "heartbeat", "email", "prometheus", "private-location"} {
		r.Contains(allowed, name)
	}

	r.NotContains(allowed, "sleep")
	schema, ok := createCheckDef().InputSchema.(map[string]any)
	r.True(ok)

	props, ok := schema["properties"].(map[string]any)
	r.True(ok)

	typeProp, ok := props[schemaKeyType].(map[string]any)
	r.True(ok)
	r.Contains(typeProp[schemaKeyDescription], "private-location")
}
