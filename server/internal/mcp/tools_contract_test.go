package mcp

import (
	"strings"
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

// stringInputGaps lists the string properties of an input schema that miss
// maxLength, plus the required or identifier ones that miss minLength >= 1.
func stringInputGaps(def *ToolDefinition) []string {
	schema, _ := def.InputSchema.(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]string)

	var gaps []string
	for name, raw := range props {
		prop, _ := raw.(map[string]any)
		if prop["type"] != "string" {
			continue
		}
		if _, ok := prop["maxLength"].(int); !ok {
			gaps = append(gaps, name+": no maxLength")
		}
		if n, _ := prop["minLength"].(int); n < 1 {
			for _, req := range required {
				if req == name {
					gaps = append(gaps, name+": required without minLength")
				}
			}
		}
	}

	return gaps
}

func TestToolContract_StringInputsAreBounded(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	for i := range handler.tools {
		def := &handler.tools[i]
		r.Empty(stringInputGaps(def), "tool %q", def.Name)
	}
}

func TestToolContract_OptionalFreeTextCanBeCleared(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	for i := range handler.tools {
		def := &handler.tools[i]
		if def.Name != toolUpdateCheck && def.Name != toolUpdateStatusPage {
			continue
		}
		props := inputProps(def)
		r.NotContains(props[schemaKeyDescription], "minLength", "tool %q", def.Name)
		r.Equal(1, props[propIdentifier]["minLength"], "tool %q", def.Name)
	}
}

func TestToolContract_LongInputsKeepTheirOwnBound(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	for i := range handler.tools {
		def := &handler.tools[i]
		props := inputProps(def)
		for _, name := range []string{propScript, propCustomCSS} {
			if prop, ok := props[name]; ok {
				r.Equal(maxLongStringLength, prop["maxLength"], "tool %q %s", def.Name, name)
			}
		}
	}
}

func TestToolContract_MutatingToolsDiscloseReversibility(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	mutating := map[string]bool{
		toolCreateCheck: true, toolUpdateCheck: true, toolDeleteCheck: true,
		toolCreateStatusPage: true, toolUpdateStatusPage: true, toolDeleteStatusPage: true,
	}

	handler := newTestHandler()
	seen := 0
	for i := range handler.tools {
		def := &handler.tools[i]
		if !mutating[def.Name] {
			continue
		}
		seen++
		desc := strings.ToLower(def.Description)
		r.True(strings.Contains(desc, "reversible") || strings.Contains(desc, "permanent"), "tool %q", def.Name)
	}
	r.Equal(len(mutating), seen)
}

func TestToolCall_OversizedStringDoesNotCrash(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	long := strings.Repeat("x", 5000)
	// The schema bounds are advertised only (the handler does not validate
	// against inputSchema): an oversized value must not crash the call.
	r.NotPanics(func() {
		_ = handler.toolProbe(t.Context(), "acme", map[string]any{propKind: probeKindFetch, "url": long})
		res := handler.toolProbe(t.Context(), "acme", map[string]any{propKind: long})
		r.True(res.IsError)
	})
}

// inputProps returns the input schema properties of a tool, one map each.
func inputProps(def *ToolDefinition) map[string]map[string]any {
	schema, _ := def.InputSchema.(map[string]any)
	raw, _ := schema["properties"].(map[string]any)
	out := make(map[string]map[string]any, len(raw))
	for name, v := range raw {
		out[name], _ = v.(map[string]any)
	}

	return out
}
