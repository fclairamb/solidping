package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Toolsets (spec 2026-10-12-06) decide which tools tools/list shows. They
// never decide what may be CALLED: every registered name, listed or not,
// stays in toolMap and goes through the same scope, demo and role gates.
//
// A client picks them with ?toolsets=core,statuspages,admin (or all) on the
// MCP endpoint URL, or SP_MCP_TOOLSETS / --toolsets for `solidping mcp
// --stdio`. Absent means core + statuspages.
const (
	toolsetCore        = "core"
	toolsetStatusPages = "statuspages"
	toolsetAdmin       = "admin"
	// toolsetLegacy lists the deprecated names the merged tools replaced.
	// They stay callable whatever is selected.
	toolsetLegacy = "legacy"
	// toolsetAll is shorthand for core, statuspages and admin (not legacy).
	toolsetAll = "all"

	// ToolsetsQueryParam is the MCP endpoint URL parameter selecting toolsets.
	ToolsetsQueryParam = "toolsets"
	// ToolsetsEnvVar selects toolsets for `solidping mcp --stdio`.
	ToolsetsEnvVar = "SP_MCP_TOOLSETS"
)

// validToolsetNames is the user-facing list, in the order the error names them.
const validToolsetNames = "core, statuspages, admin, legacy, all"

// errUnknownToolset is wrapped by parseToolsets for a name it does not know.
var errUnknownToolset = errors.New("unknown toolset")

// toolsetSelection is the set of toolsets whose tools are listed.
type toolsetSelection map[string]struct{}

func (s toolsetSelection) has(toolset string) bool {
	_, ok := s[toolset]

	return ok
}

// defaultToolsets is what a client that names no toolset gets.
func defaultToolsets() toolsetSelection {
	return toolsetSelection{toolsetCore: {}, toolsetStatusPages: {}}
}

// parseToolsets reads a comma-separated toolset list. Blank means the
// default; an unknown name is an error naming the valid ones.
func parseToolsets(raw string) (toolsetSelection, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultToolsets(), nil
	}

	selection := toolsetSelection{}

	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)

		switch name {
		case "":
			continue
		case toolsetAll:
			selection[toolsetCore] = struct{}{}
			selection[toolsetStatusPages] = struct{}{}
			selection[toolsetAdmin] = struct{}{}
		case toolsetCore, toolsetStatusPages, toolsetAdmin, toolsetLegacy:
			selection[name] = struct{}{}
		default:
			return nil, fmt.Errorf("%w %q: valid toolsets are %s", errUnknownToolset, name, validToolsetNames)
		}
	}

	if len(selection) == 0 {
		return defaultToolsets(), nil
	}

	return selection, nil
}

// ValidateToolsets reports whether raw is a valid toolset list, for callers
// outside the package (the stdio command) that want to fail at startup.
func ValidateToolsets(raw string) error {
	_, err := parseToolsets(raw)

	return err
}

type toolsetsContextKey struct{}

// withToolsets attaches the request's toolset selection to ctx.
func withToolsets(ctx context.Context, selection toolsetSelection) context.Context {
	return context.WithValue(ctx, toolsetsContextKey{}, selection)
}

// toolsetsFromContext returns the request's selection, or the default.
func toolsetsFromContext(ctx context.Context) toolsetSelection {
	if selection, ok := ctx.Value(toolsetsContextKey{}).(toolsetSelection); ok && selection != nil {
		return selection
	}

	return defaultToolsets()
}

// listTools returns the definitions of the tools in the selected toolsets, in
// registration order. It allocates per call: h.tools is never mutated after
// registerTools, so concurrent sessions with different selections never race.
func (h *Handler) listTools(selection toolsetSelection) []ToolDefinition {
	listed := make([]ToolDefinition, 0, len(h.tools))

	for i := range h.tools {
		if selection.has(h.toolsets[h.tools[i].Name]) {
			listed = append(listed, h.tools[i])
		}
	}

	return listed
}
