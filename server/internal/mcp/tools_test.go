package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObjectSchema(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	t.Run("with properties only", func(t *testing.T) {
		t.Parallel()
		schema := objectSchema(map[string]any{
			"name": stringProp("A name"),
		}, nil)

		r.Equal("object", schema["type"])
		props, ok := schema["properties"].(map[string]any)
		r.True(ok)
		r.Contains(props, "name")
		_, hasRequired := schema["required"]
		r.False(hasRequired, "required should be omitted when nil")
	})

	t.Run("with required fields", func(t *testing.T) {
		t.Parallel()
		schema := objectSchema(map[string]any{
			"id":   stringProp("ID"),
			"name": stringProp("Name"),
		}, []string{"id"})

		required, ok := schema["required"].([]string)
		r.True(ok)
		r.Equal([]string{"id"}, required)
	})

	t.Run("empty properties", func(t *testing.T) {
		t.Parallel()
		schema := objectSchema(map[string]any{}, nil)
		r.Equal("object", schema["type"])
		props, ok := schema["properties"].(map[string]any)
		r.True(ok)
		r.Empty(props)
	})
}

func TestPropertyHelpers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	t.Run("stringProp", func(t *testing.T) {
		t.Parallel()
		prop := stringProp("a description")
		r.Equal("string", prop["type"])
		r.Equal("a description", prop["description"])
	})

	t.Run("intProp", func(t *testing.T) {
		t.Parallel()
		prop := intProp("count of items")
		r.Equal("integer", prop["type"])
		r.Equal("count of items", prop["description"])
	})

	t.Run("boolProp", func(t *testing.T) {
		t.Parallel()
		prop := boolProp("is enabled")
		r.Equal("boolean", prop["type"])
		r.Equal("is enabled", prop["description"])
	})

	t.Run("arrayOfStringsProp", func(t *testing.T) {
		t.Parallel()
		prop := arrayOfStringsProp("list of regions")
		r.Equal("array", prop["type"])
		r.Equal("list of regions", prop["description"])
		items, ok := prop["items"].(map[string]any)
		r.True(ok)
		r.Equal("string", items["type"])
	})

	t.Run("objectProp", func(t *testing.T) {
		t.Parallel()
		prop := objectProp("config object")
		r.Equal("object", prop["type"])
		r.Equal("config object", prop["description"])
	})
}

func TestToolDefinitions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Verify each tool definition has the required fields
	defs := []ToolDefinition{
		listChecksDef(),
		getCheckDef(),
		createCheckDef(),
		updateCheckDef(),
		deleteCheckDef(),
		listResultsDef(),
		listIncidentsDef(),
		getIncidentDef(),
		listConnectionsDef(),
		createConnectionDef(),
		listCheckGroupsDef(),
		listRegionsDef(),
		diagnoseCheckDef(),
		listStatusPagesDef(),
		getStatusPageDef(),
		createStatusPageDef(),
		updateStatusPageDef(),
		deleteStatusPageDef(),
		listStatusPageSectionsDef(),
		createStatusPageSectionDef(),
		updateStatusPageSectionDef(),
		deleteStatusPageSectionDef(),
		listStatusPageResourcesDef(),
		createStatusPageResourceDef(),
		updateStatusPageResourceDef(),
		deleteStatusPageResourceDef(),
		listMaintenanceWindowsDef(),
		getMaintenanceWindowDef(),
		createMaintenanceWindowDef(),
		updateMaintenanceWindowDef(),
		deleteMaintenanceWindowDef(),
		setMaintenanceWindowChecksDef(),
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

			schema, ok := def.InputSchema.(map[string]any)
			r.True(ok)
			r.Equal("object", schema["type"])
			_, hasProps := schema["properties"]
			r.True(hasProps)
		})
	}
}

func TestRegisterTools(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()

	r.Len(handler.tools, 35)
	r.Len(handler.toolMap, 35)

	// Every tool definition should have a corresponding function in the map
	for _, tool := range handler.tools {
		_, exists := handler.toolMap[tool.Name]
		r.True(exists, "tool %q registered in tools but not in toolMap", tool.Name)
	}
}

// TestAllToolDescriptionsMeetMinimum enforces a soft bar so future tool
// additions don't ship with one-word descriptions. Adjust the minimums only
// if you have a legitimately short description (no current tools do).
func TestAllToolDescriptionsMeetMinimum(t *testing.T) {
	t.Parallel()

	const minToolDescChars = 40
	const minParamDescChars = 20

	handler := newTestHandler()

	for i := range handler.tools {
		tool := handler.tools[i]
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			r.GreaterOrEqual(
				len(tool.Description), minToolDescChars,
				"tool %q description too short: %q", tool.Name, tool.Description,
			)
			schema, ok := tool.InputSchema.(map[string]any)
			r.True(ok)
			props, ok := schema["properties"].(map[string]any)
			if !ok {
				return
			}
			for name, p := range props {
				propMap, ok := p.(map[string]any)
				r.True(ok, "tool %q param %q schema malformed", tool.Name, name)
				desc, _ := propMap[schemaKeyDescription].(string)
				r.GreaterOrEqual(
					len(desc), minParamDescChars,
					"tool %q param %q description too short: %q", tool.Name, name, desc,
				)
			}
		})
	}
}
