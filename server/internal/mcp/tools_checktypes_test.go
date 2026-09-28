package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckTypeToolDefinitions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defs := []struct {
		def   ToolDefinition
		title string
	}{
		{listCheckTypesDef(), "List check types"},
		{getCheckTypeSamplesDef(), "Get check type samples"},
		{validateCheckDef(), "Validate check"},
	}

	for _, tc := range defs {
		def := tc.def
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			r.NotEmpty(def.Name)
			r.NotEmpty(def.Description)
			r.NotNil(def.InputSchema)
			r.Contains(def.Description, "Read-only: works with mcp:read tokens.")

			r.NotNil(def.Annotations)
			r.True(def.Annotations.ReadOnlyHint)
			r.False(def.Annotations.DestructiveHint)
			r.True(def.Annotations.IdempotentHint)
			r.False(def.Annotations.OpenWorldHint)
			r.Equal(tc.title, def.Annotations.Title)

			schema, ok := def.OutputSchema.(map[string]any)
			r.True(ok, "%s must declare an outputSchema", def.Name)
			r.Equal(schemaTypeObject, schema[schemaKeyType])
			r.Contains(schema, schemaKeyProperties)
		})
	}
}

// TestListCheckTypesOutputSchema_MatchesResponse: the payload is the
// checktypes.ListCheckTypesResponse envelope ({data: [...]}) whose item is a
// CheckTypeResponse.
func TestListCheckTypesOutputSchema_MatchesResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	schema, ok := listCheckTypesDef().OutputSchema.(map[string]any)
	r.True(ok)
	props, ok := schema[schemaKeyProperties].(map[string]any)
	r.True(ok)

	data, ok := props[schemaKeyData].(map[string]any)
	r.True(ok)
	r.Equal(schemaTypeArray, data[schemaKeyType])

	items, ok := data[schemaKeyItems].(map[string]any)
	r.True(ok)
	itemProps, ok := items[schemaKeyProperties].(map[string]any)
	r.True(ok)

	for _, key := range []string{
		schemaKeyType, schemaKeyDescription, "labels", schemaKeyEnabled, "disabledReason", "advisory",
		"minPeriodSeconds", "maxPeriodSeconds", "defaultPeriodSeconds",
		"supportsTunnel", "supportsIpVersion", "secretFields",
	} {
		r.Contains(itemProps, key)
	}
}

// TestGetCheckTypeSamplesOutputSchema_MatchesResponse: the payload is
// checktypes.ListSamplesResponse ({data: [{checkType, samples}]}) and each
// sample is a SampleConfigResponse.
func TestGetCheckTypeSamplesOutputSchema_MatchesResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	schema, ok := getCheckTypeSamplesDef().OutputSchema.(map[string]any)
	r.True(ok)
	props, ok := schema[schemaKeyProperties].(map[string]any)
	r.True(ok)

	data, ok := props[schemaKeyData].(map[string]any)
	r.True(ok)
	items, ok := data[schemaKeyItems].(map[string]any)
	r.True(ok)
	itemProps, ok := items[schemaKeyProperties].(map[string]any)
	r.True(ok)
	r.Contains(itemProps, "checkType")
	r.Contains(itemProps, "samples")

	samples, ok := itemProps["samples"].(map[string]any)
	r.True(ok)
	r.Equal(schemaTypeArray, samples[schemaKeyType])
	sampleItems, ok := samples[schemaKeyItems].(map[string]any)
	r.True(ok)
	sampleProps, ok := sampleItems[schemaKeyProperties].(map[string]any)
	r.True(ok)
	for _, key := range []string{schemaKeyName, schemaKeySlug, "periodSeconds", "config"} {
		r.Contains(sampleProps, key)
	}
}

// TestValidateCheckOutputSchema_MatchesResponse: the payload is
// checks.ValidateCheckResponse — valid is always present, fields/warnings
// are the severity split and are omitted when empty.
func TestValidateCheckOutputSchema_MatchesResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	schema, ok := validateCheckDef().OutputSchema.(map[string]any)
	r.True(ok)
	r.Equal(schemaTypeObject, schema[schemaKeyType])

	required, ok := schema["required"].([]string)
	r.True(ok)
	r.Equal([]string{"valid"}, required)

	props, ok := schema[schemaKeyProperties].(map[string]any)
	r.True(ok)
	valid, ok := props["valid"].(map[string]any)
	r.True(ok)
	r.Equal("boolean", valid[schemaKeyType])

	for _, key := range []string{"fields", "warnings"} {
		list, ok := props[key].(map[string]any)
		r.True(ok, "%s must be declared", key)
		r.Equal(schemaTypeArray, list[schemaKeyType])
		items, ok := list[schemaKeyItems].(map[string]any)
		r.True(ok)
		itemProps, ok := items[schemaKeyProperties].(map[string]any)
		r.True(ok)
		for _, fieldKey := range []string{schemaKeyName, "message", "severity", "code"} {
			r.Contains(itemProps, fieldKey)
		}
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
			args:        map[string]any{schemaKeyType: "http"},
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

	props, ok := schema[schemaKeyProperties].(map[string]any)
	r.True(ok)

	typeProp, ok := props[schemaKeyType].(map[string]any)
	r.True(ok)
	r.Contains(typeProp[schemaKeyDescription], "private-location")
}
