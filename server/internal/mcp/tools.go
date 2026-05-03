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
		{diagnoseCheckDef(), h.toolDiagnoseCheck},
		// Status pages
		{listStatusPagesDef(), h.toolListStatusPages},
		{getStatusPageDef(), h.toolGetStatusPage},
		{createStatusPageDef(), h.toolCreateStatusPage},
		{updateStatusPageDef(), h.toolUpdateStatusPage},
		{deleteStatusPageDef(), h.toolDeleteStatusPage},
		// Status page sections
		{listStatusPageSectionsDef(), h.toolListStatusPageSections},
		{createStatusPageSectionDef(), h.toolCreateStatusPageSection},
		{updateStatusPageSectionDef(), h.toolUpdateStatusPageSection},
		{deleteStatusPageSectionDef(), h.toolDeleteStatusPageSection},
		// Status page resources
		{listStatusPageResourcesDef(), h.toolListStatusPageResources},
		{createStatusPageResourceDef(), h.toolCreateStatusPageResource},
		{updateStatusPageResourceDef(), h.toolUpdateStatusPageResource},
		{deleteStatusPageResourceDef(), h.toolDeleteStatusPageResource},
		// Maintenance windows
		{listMaintenanceWindowsDef(), h.toolListMaintenanceWindows},
		{getMaintenanceWindowDef(), h.toolGetMaintenanceWindow},
		{createMaintenanceWindowDef(), h.toolCreateMaintenanceWindow},
		{updateMaintenanceWindowDef(), h.toolUpdateMaintenanceWindow},
		{deleteMaintenanceWindowDef(), h.toolDeleteMaintenanceWindow},
		{setMaintenanceWindowChecksDef(), h.toolSetMaintenanceWindowChecks},
		// Check type discovery & validation
		{listCheckTypesDef(), h.toolListCheckTypes},
		{getCheckTypeSamplesDef(), h.toolGetCheckTypeSamples},
		{validateCheckDef(), h.toolValidateCheck},
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
		schemaKeyType: "object",
		"properties":  props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProp(desc string) map[string]any {
	return map[string]any{schemaKeyType: "string", schemaKeyDescription: desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{schemaKeyType: "integer", schemaKeyDescription: desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{schemaKeyType: "boolean", schemaKeyDescription: desc}
}

func arrayOfStringsProp(desc string) map[string]any {
	return map[string]any{
		schemaKeyType:        "array",
		schemaKeyItems:       map[string]any{schemaKeyType: "string"},
		schemaKeyDescription: desc,
	}
}

func objectProp(desc string) map[string]any {
	return map[string]any{schemaKeyType: "object", schemaKeyDescription: desc}
}
