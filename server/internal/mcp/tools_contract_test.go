package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// toolContractGaps reports every way a definition misses the tool contract
// (issue 587): outputSchema, annotations with a title, closed inputSchema.
func toolContractGaps(def *ToolDefinition) []string {
	var gaps []string

	if def.OutputSchema == nil {
		gaps = append(gaps, "no outputSchema")
	}

	if def.Annotations == nil || def.Annotations.Title == "" {
		gaps = append(gaps, "no annotations.title")
	}

	schema, ok := def.InputSchema.(map[string]any)
	if !ok || schema["additionalProperties"] != false {
		gaps = append(gaps, "inputSchema.additionalProperties is not false")
	}

	return gaps
}

func TestToolContract_EveryRegisteredTool(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	r.NotEmpty(handler.tools)

	// tools/list differs only by toolset; h.tools holds every one of them.
	for i := range handler.tools {
		def := &handler.tools[i]
		r.Empty(toolContractGaps(def), "tool %q", def.Name)
	}
}

func TestToolContract_ReportsGaps(t *testing.T) {
	t.Parallel()

	bare := &ToolDefinition{Name: "fixture", InputSchema: map[string]any{"type": "object"}}
	require.Len(t, toolContractGaps(bare), 3)

	ok := &ToolDefinition{
		Name:         "fixture",
		InputSchema:  objectSchema(map[string]any{}, nil),
		OutputSchema: map[string]any{"type": "object"},
		Annotations:  readOnlyAnnotations("Fixture"),
	}
	require.Empty(t, toolContractGaps(ok))
}
