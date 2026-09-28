package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestListCheckGroupsDefinition locks annotations and the {data: [...]}
// outputSchema on list_check_groups (Glama usage scoring requires both).
func TestListCheckGroupsDefinition(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	def := listCheckGroupsDef()
	r.NotEmpty(def.Description)

	r.NotNil(def.Annotations, "list_check_groups must declare annotations")
	r.Equal("List check groups", def.Annotations.Title, "sentence-case title")
	r.True(def.Annotations.ReadOnlyHint)
	r.False(def.Annotations.DestructiveHint)
	r.True(def.Annotations.IdempotentHint)
	r.False(def.Annotations.OpenWorldHint)
	r.Contains(def.Description, "Read-only: works with mcp:read tokens.")
	r.Contains(def.Description, "{data: [...]}")

	r.NotNil(def.OutputSchema, "list_check_groups must declare an outputSchema")
	schema, ok := def.OutputSchema.(map[string]any)
	r.True(ok, "outputSchema must be an object schema")
	r.Equal(schemaTypeObject, schema[schemaKeyType])
	props, hasProps := schema[schemaKeyProperties].(map[string]any)
	r.True(hasProps)
	data, hasData := props[schemaKeyData].(map[string]any)
	r.True(hasData, "list_check_groups must return {data: [...]}")
	r.Equal(schemaTypeArray, data[schemaKeyType])
}
