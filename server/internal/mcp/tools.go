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
		{incidentNotificationsListDef(), h.toolIncidentNotificationsList},
		{listIntegrationsDef(), h.toolListIntegrations},
		{createIntegrationDef(), h.toolCreateIntegration},
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
		// Status page incidents (publication overlay — spec 2026-08-19-08)
		{listStatusPageIncidentsDef(), h.toolListStatusPageIncidents},
		{createStatusPageIncidentDef(), h.toolCreateStatusPageIncident},
		{updateStatusPageIncidentDef(), h.toolUpdateStatusPageIncident},
		{createStatusPageIncidentUpdateDef(), h.toolCreateStatusPageIncidentUpdate},
		{createIncidentPublicationDef(), h.toolCreateIncidentPublication},
		{deleteIncidentPublicationDef(), h.toolDeleteIncidentPublication},
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
		// js check authoring probes (spec 2026-10-03-07): no LLM involved,
		// so they are listed whether or not an AI provider is configured.
		{runJSScriptDef(), h.toolRunJSScript},
		{fetchPageDef(), h.toolFetchPage},
		{browserSnapshotDef(), h.toolBrowserSnapshot},
	}

	h.tools = make([]ToolDefinition, len(all))
	h.toolMap = make(map[string]toolFunc, len(all))
	for i := range all {
		def := all[i].def
		// Title lives with the annotations (one place per tool) and is
		// mirrored onto the definition, since MCP's display precedence is
		// Tool.title → annotations.title → name.
		if def.Title == "" && def.Annotations != nil {
			def.Title = def.Annotations.Title
		}
		h.tools[i] = def
		h.toolMap[def.Name] = all[i].fn
	}
}

// schema helpers for tool input schemas.

func objectSchema(props map[string]any, required []string) map[string]any {
	schema := map[string]any{
		schemaKeyType:       schemaTypeObject,
		schemaKeyProperties: props,
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
		schemaKeyType:        schemaTypeArray,
		schemaKeyItems:       map[string]any{schemaKeyType: "string"},
		schemaKeyDescription: desc,
	}
}

func objectProp(desc string) map[string]any {
	return map[string]any{schemaKeyType: schemaTypeObject, schemaKeyDescription: desc}
}

// Output-schema helpers (MCP 2025-06-18). An outputSchema MUST have an
// object root, and whenever one is declared the server MUST return
// structuredContent conforming to it — so these are always paired with
// marshalResult, never textResult. Item/property maps deliberately leave
// additionalProperties at its default (true): they document the fields an
// agent relies on without freezing every DTO field into the contract.

// arrayOfObjectsProp builds an array-of-objects property whose items expose
// the given (verified) fields.
func arrayOfObjectsProp(desc string, itemProps map[string]any) map[string]any {
	return map[string]any{
		schemaKeyType:        schemaTypeArray,
		schemaKeyItems:       map[string]any{schemaKeyType: schemaTypeObject, schemaKeyProperties: itemProps},
		schemaKeyDescription: desc,
	}
}

// dataOutputSchema is the output shape of a list tool: the repo-standard
// {data: [...]} envelope (REST list responses are wrapped the same way).
func dataOutputSchema(desc string, itemProps map[string]any) map[string]any {
	return objectSchema(map[string]any{
		schemaKeyData: arrayOfObjectsProp(desc, itemProps),
	}, nil)
}
