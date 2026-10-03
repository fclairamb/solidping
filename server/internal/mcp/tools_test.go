package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObjectSchema(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	t.Run("with properties only", func(t *testing.T) {
		t.Parallel()
		schema := objectSchema(map[string]any{
			schemaKeyName: stringProp("A name"),
		}, nil)

		r.Equal(schemaTypeObject, schema[schemaKeyType])
		props, ok := schema[schemaKeyProperties].(map[string]any)
		r.True(ok)
		r.Contains(props, schemaKeyName)
		_, hasRequired := schema["required"]
		r.False(hasRequired, "required should be omitted when nil")
	})

	t.Run("with required fields", func(t *testing.T) {
		t.Parallel()
		schema := objectSchema(map[string]any{
			"id":          stringProp("ID"),
			schemaKeyName: stringProp("Name"),
		}, []string{"id"})

		required, ok := schema["required"].([]string)
		r.True(ok)
		r.Equal([]string{"id"}, required)
	})

	t.Run("empty properties", func(t *testing.T) {
		t.Parallel()
		schema := objectSchema(map[string]any{}, nil)
		r.Equal(schemaTypeObject, schema[schemaKeyType])
		props, ok := schema[schemaKeyProperties].(map[string]any)
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
		r.Equal("string", prop[schemaKeyType])
		r.Equal("a description", prop[schemaKeyDescription])
	})

	t.Run("intProp", func(t *testing.T) {
		t.Parallel()
		prop := intProp("count of items")
		r.Equal("integer", prop[schemaKeyType])
		r.Equal("count of items", prop[schemaKeyDescription])
	})

	t.Run("boolProp", func(t *testing.T) {
		t.Parallel()
		prop := boolProp("is enabled")
		r.Equal("boolean", prop[schemaKeyType])
		r.Equal("is enabled", prop[schemaKeyDescription])
	})

	t.Run("arrayOfStringsProp", func(t *testing.T) {
		t.Parallel()
		prop := arrayOfStringsProp("list of regions")
		r.Equal(schemaTypeArray, prop[schemaKeyType])
		r.Equal("list of regions", prop[schemaKeyDescription])
		items, ok := prop["items"].(map[string]any)
		r.True(ok)
		r.Equal("string", items[schemaKeyType])
	})

	t.Run("objectProp", func(t *testing.T) {
		t.Parallel()
		prop := objectProp("config object")
		r.Equal(schemaTypeObject, prop[schemaKeyType])
		r.Equal("config object", prop[schemaKeyDescription])
	})
}

func TestToolDefinitions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Verify each registered tool definition has the required fields.
	handler := newTestHandler()
	r.NotEmpty(handler.tools)

	for _, def := range handler.tools {
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			r.NotEmpty(def.Name)
			r.NotEmpty(def.Description)
			r.NotNil(def.InputSchema)

			schema, ok := def.InputSchema.(map[string]any)
			r.True(ok)
			r.Equal(schemaTypeObject, schema[schemaKeyType])
			_, hasProps := schema[schemaKeyProperties]
			r.True(hasProps)
		})
	}
}

// TestEveryToolDeclaresAnnotationsAndOutputSchema gates the MCP 2025-06-18
// surface: every tool must carry behavioral annotations, a display title and
// an object-rooted output schema, and the read-only hint must stay in sync
// with the scope gate (isMutationTool) so a new write tool can never ship
// advertised as read-only. The per-hint assertions also pin the semantics of
// each annotation class (see annotations.go).
func TestEveryToolDeclaresAnnotationsAndOutputSchema(t *testing.T) {
	t.Parallel()

	handler := newTestHandler()

	for _, tool := range handler.tools {
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.NotNil(tool.Annotations, "tool %q has no annotations", tool.Name)
			r.Equal(tool.Annotations.Title, tool.Title,
				"tool %q title not mirrored from annotations", tool.Name)

			r.NotNil(tool.OutputSchema, "tool %q has no output schema", tool.Name)
			schema, ok := tool.OutputSchema.(map[string]any)
			r.True(ok, "tool %q output schema is not an object schema", tool.Name)
			r.Equal(schemaTypeObject, schema[schemaKeyType],
				"tool %q output schema must have an object root", tool.Name)

			ann := tool.Annotations
			// The js authoring probes reach the target the caller names
			// (spec 2026-10-03-07): they are the only open-world tools.
			probe := tool.Name == toolRunJSScript || tool.Name == toolFetchPage ||
				tool.Name == toolBrowserSnapshot
			r.Equal(probe, ann.OpenWorldHint,
				"tool %q open-world hint: only the js authoring probes reach outside SolidPing data",
				tool.Name)
			r.Equal(!isMutationTool(tool.Name), ann.ReadOnlyHint,
				"tool %q readOnlyHint out of sync with the scope gate", tool.Name)

			// The hints must all be present in the emitted JSON: omitempty
			// on a false would drop the key and let the client fall back to
			// the spec default (destructiveHint/openWorldHint default true).
			raw, err := json.Marshal(ann)
			r.NoError(err)
			var decoded map[string]any
			r.NoError(json.Unmarshal(raw, &decoded))
			for _, hint := range []string{
				"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint",
			} {
				_, present := decoded[hint]
				r.True(present, "tool %q annotation %q not emitted", tool.Name, hint)
			}

			switch {
			case !isMutationTool(tool.Name):
				r.False(ann.DestructiveHint, "read tool %q flagged destructive", tool.Name)
				r.True(ann.IdempotentHint, "read tool %q not flagged idempotent", tool.Name)
			case strings.HasPrefix(tool.Name, "delete_"),
				strings.HasPrefix(tool.Name, "set_"):
				r.True(ann.DestructiveHint,
					"deleting/replacing tool %q must be flagged destructive", tool.Name)
			case strings.HasPrefix(tool.Name, "create_"):
				r.False(ann.IdempotentHint,
					"create tool %q must not claim idempotency (it makes a new row)",
					tool.Name)
				r.False(ann.DestructiveHint,
					"create tool %q must not claim destructive", tool.Name)
			case strings.HasPrefix(tool.Name, "update_"):
				r.True(ann.IdempotentHint,
					"update tool %q should be idempotent (PATCH keeps omitted fields)",
					tool.Name)
				r.False(ann.DestructiveHint,
					"update tool %q must not claim destructive", tool.Name)
			}
		})
	}
}

func TestRegisterTools(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()

	r.Len(handler.tools, 45)
	r.Len(handler.toolMap, 45)

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
			props, ok := schema[schemaKeyProperties].(map[string]any)
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

// lintToolDefinition returns every registry-quality violation of one tool
// (spec 2026-09-30-03). Directories such as Smithery and Glama score exactly
// these: a real description that opens with a verb and every input property,
// nested ones and array items included, documented.
func lintToolDefinition(def ToolDefinition) []string {
	const minToolDescChars = 40

	var problems []string

	desc := strings.TrimSpace(def.Description)
	switch {
	case len(desc) < minToolDescChars:
		problems = append(problems, "description shorter than 40 chars")
	case strings.HasPrefix(desc, "This tool"), strings.HasPrefix(desc, "A tool"), strings.HasPrefix(desc, "Tool "):
		problems = append(problems, "description must open with a verb, not describe itself as a tool")
	case desc[0] < 'A' || desc[0] > 'Z':
		problems = append(problems, "description must start with a capitalised verb")
	}

	schema, ok := def.InputSchema.(map[string]any)
	if !ok {
		return append(problems, "inputSchema is not an object")
	}

	return append(problems, lintSchemaProperties(schema, "inputSchema")...)
}

// lintSchemaProperties walks a JSON-schema node: each property needs a
// non-empty description, and object properties and array items are descended.
func lintSchemaProperties(node map[string]any, path string) []string {
	var problems []string

	if props, ok := node[schemaKeyProperties].(map[string]any); ok {
		for name, raw := range props {
			prop, isMap := raw.(map[string]any)
			propPath := path + "." + name

			if !isMap {
				problems = append(problems, propPath+" is malformed")

				continue
			}

			if d, _ := prop[schemaKeyDescription].(string); strings.TrimSpace(d) == "" {
				problems = append(problems, propPath+" has no description")
			}

			problems = append(problems, lintSchemaProperties(prop, propPath)...)
		}
	}

	if items, ok := node[schemaKeyItems].(map[string]any); ok {
		problems = append(problems, lintSchemaProperties(items, path+"[]")...)
	}

	return problems
}

func TestEveryToolPassesTheRegistryLint(t *testing.T) {
	t.Parallel()

	handler := newTestHandler()
	require.NotEmpty(t, handler.tools)

	for i := range handler.tools {
		def := handler.tools[i]
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			require.Empty(t, lintToolDefinition(def), "tool %q", def.Name)
		})
	}
}

func TestRegistryLintRejectsABadTool(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bad := ToolDefinition{
		Name:        "bad_tool",
		Description: "This tool does things.",
		InputSchema: map[string]any{
			schemaKeyType: schemaTypeObject,
			schemaKeyProperties: map[string]any{
				"url": map[string]any{schemaKeyType: "string"},
				"nested": map[string]any{
					schemaKeyType:        schemaTypeObject,
					schemaKeyDescription: "A nested object with a documented shape.",
					schemaKeyProperties: map[string]any{
						"inner": map[string]any{schemaKeyType: "string"},
					},
				},
				"list": map[string]any{
					schemaKeyType:        schemaTypeArray,
					schemaKeyDescription: "A list whose items are undocumented objects.",
					schemaKeyItems: map[string]any{
						schemaKeyType: schemaTypeObject,
						schemaKeyProperties: map[string]any{
							"leaf": map[string]any{schemaKeyType: "string"},
						},
					},
				},
			},
		},
	}

	problems := strings.Join(lintToolDefinition(bad), "\n")
	r.Contains(problems, "description")
	r.Contains(problems, "inputSchema.url has no description")
	r.Contains(problems, "inputSchema.nested.inner has no description")
	r.Contains(problems, "inputSchema.list[].leaf has no description")

	good := ToolDefinition{
		Name:        "good_tool",
		Description: "List the widgets of the organization, newest first.",
		InputSchema: objectSchema(map[string]any{"q": stringProp("Free-text search, for example 'api'.")}, nil),
	}
	r.Empty(lintToolDefinition(good), "positive control")
}
