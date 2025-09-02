package mcp

func (h *Handler) registerTools() {
	type tool struct {
		def ToolDefinition
		fn  toolFunc
	}

	all := []tool{
		{listChecksDef(), h.toolListChecks},
		{getCheckDef(), h.toolGetCheck},
		{createCheckDef(), h.toolCreateCheck},
		{updateCheckDef(), h.toolUpdateCheck},
		{deleteCheckDef(), h.toolDeleteCheck},
		{listResultsDef(), h.toolListResults},
		{listIncidentsDef(), h.toolListIncidents},
		{getIncidentDef(), h.toolGetIncident},
		{listConnectionsDef(), h.toolListConnections},
		{createConnectionDef(), h.toolCreateConnection},
		{listCheckGroupsDef(), h.toolListCheckGroups},
		{listRegionsDef(), h.toolListRegions},
	}

	h.tools = make([]ToolDefinition, len(all))
	h.toolMap = make(map[string]toolFunc, len(all))
	for i := range all {
		h.tools[i] = all[i].def
		h.toolMap[all[i].def.Name] = all[i].fn
	}
}

// schema helpers for tool input schemas.

func objectSchema(props map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func arrayOfStringsProp(desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": desc,
	}
}

func objectProp(desc string) map[string]any {
	return map[string]any{"type": "object", "description": desc}
}
