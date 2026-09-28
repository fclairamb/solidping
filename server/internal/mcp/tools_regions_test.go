package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestListRegionsDefinition locks annotations and the object outputSchema on
// list_regions (Glama usage scoring requires both).
func TestListRegionsDefinition(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	def := listRegionsDef()
	r.NotEmpty(def.Description)

	r.NotNil(def.Annotations, "list_regions must declare annotations")
	r.Equal("List regions", def.Annotations.Title, "sentence-case title")
	r.True(def.Annotations.ReadOnlyHint)
	r.False(def.Annotations.DestructiveHint)
	r.True(def.Annotations.IdempotentHint)
	r.False(def.Annotations.OpenWorldHint)
	r.Contains(def.Description, "Read-only: works with mcp:read tokens.")

	r.NotNil(def.OutputSchema, "list_regions must declare an outputSchema")
	schema, ok := def.OutputSchema.(map[string]any)
	r.True(ok, "outputSchema must be an object schema")
	r.Equal(schemaTypeObject, schema[schemaKeyType])
	props, hasProps := schema[schemaKeyProperties].(map[string]any)
	r.True(hasProps)
	data, hasData := props[schemaKeyData].(map[string]any)
	r.True(hasData, "list_regions must return {data: [...]}")
	r.Equal(schemaTypeArray, data[schemaKeyType])
	r.Contains(props, "defaultRegions")
}
