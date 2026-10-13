package mcp

// registeredTool is one entry of the registry: a definition, its
// implementation and the toolset that lists it.
type registeredTool struct {
	def     ToolDefinition
	fn      toolFunc
	toolset string
}

// listedTools are the 31 tools of the core, statuspages and admin toolsets.
func (h *Handler) listedTools() []registeredTool {
	return []registeredTool{
		// core: checks, their results and incidents (15 tools).
		{listChecksDef(), h.toolListChecks, toolsetCore},
		{getCheckDef(), h.toolGetCheck, toolsetCore},
		{createCheckDef(), h.toolCreateCheck, toolsetCore},
		{updateCheckDef(), h.toolUpdateCheck, toolsetCore},
		{deleteCheckDef(), h.toolDeleteCheck, toolsetCore},
		{validateCheckDef(), h.toolValidateCheck, toolsetCore},
		{runCheckDef(), h.toolRunCheck, toolsetCore},
		{diagnoseCheckDef(), h.toolDiagnoseCheck, toolsetCore},
		{listResultsDef(), h.toolListResults, toolsetCore},
		{listIncidentsDef(), h.toolListIncidents, toolsetCore},
		{getIncidentDef(), h.toolGetIncident, toolsetCore},
		{listCheckTypesDef(), h.toolListCheckTypes, toolsetCore},
		{listRegionsDef(), h.toolListRegions, toolsetCore},
		{listCheckGroupsDef(), h.toolListCheckGroups, toolsetCore},
		// js check authoring probes (spec 2026-10-03-07): no LLM involved,
		// so they are listed whether or not an AI provider is configured.
		{probeDef(), h.toolProbe, toolsetCore},

		// statuspages (8 tools).
		{listStatusPagesDef(), h.toolListStatusPages, toolsetStatusPages},
		{getStatusPageDef(), h.toolGetStatusPage, toolsetStatusPages},
		{createStatusPageDef(), h.toolCreateStatusPage, toolsetStatusPages},
		{updateStatusPageDef(), h.toolUpdateStatusPage, toolsetStatusPages},
		{deleteStatusPageDef(), h.toolDeleteStatusPage, toolsetStatusPages},
		{manageStatusPageSectionDef(), h.toolManageStatusPageSection, toolsetStatusPages},
		{manageStatusPageResourceDef(), h.toolManageStatusPageResource, toolsetStatusPages},
		// Status page incidents (publication overlay — spec 2026-08-19-08)
		{manageStatusPageIncidentDef(), h.toolManageStatusPageIncident, toolsetStatusPages},

		// admin, opt-in (8 tools).
		{listIntegrationsDef(), h.toolListIntegrations, toolsetAdmin},
		{createIntegrationDef(), h.toolCreateIntegration, toolsetAdmin},
		{listMaintenanceWindowsDef(), h.toolListMaintenanceWindows, toolsetAdmin},
		{getMaintenanceWindowDef(), h.toolGetMaintenanceWindow, toolsetAdmin},
		{createMaintenanceWindowDef(), h.toolCreateMaintenanceWindow, toolsetAdmin},
		{updateMaintenanceWindowDef(), h.toolUpdateMaintenanceWindow, toolsetAdmin},
		{deleteMaintenanceWindowDef(), h.toolDeleteMaintenanceWindow, toolsetAdmin},
		{setMaintenanceWindowChecksDef(), h.toolSetMaintenanceWindowChecks, toolsetAdmin},
	}
}

// legacyTools are the 19 names the merged tools replaced (spec 2026-10-12-06).
// Always callable, listed only with ?toolsets=legacy. A write alias forces the
// merged tool's action and runs its code; a read alias is the very function
// the merged tool's include calls.
func (h *Handler) legacyTools() []registeredTool {
	return []registeredTool{
		{
			legacyDef(runJSScriptDef, "probe kind=script"),
			withFixedArg(h.toolProbe, propKind, probeKindScript), toolsetLegacy,
		},
		{
			legacyDef(fetchPageDef, "probe kind=fetch"),
			withFixedArg(h.toolProbe, propKind, probeKindFetch), toolsetLegacy,
		},
		{
			legacyDef(browserSnapshotDef, "probe kind=browser"),
			withFixedArg(h.toolProbe, propKind, probeKindBrowser), toolsetLegacy,
		},
		{
			legacyDef(getCheckTypeSamplesDef, "list_check_types with type"),
			h.toolGetCheckTypeSamples, toolsetLegacy,
		},
		{
			legacyDef(incidentNotificationsListDef, "get_incident include=notifications"),
			h.toolIncidentNotificationsList, toolsetLegacy,
		},
		{
			legacyDef(listStatusPageSectionsDef, "get_status_page include=sections"),
			h.toolListStatusPageSections, toolsetLegacy,
		},
		{
			legacyDef(createStatusPageSectionDef, "manage_status_page_section action=create"),
			withFixedArg(h.toolManageStatusPageSection, propAction, actionCreate), toolsetLegacy,
		},
		{
			legacyDef(updateStatusPageSectionDef, "manage_status_page_section action=update"),
			withFixedArg(h.toolManageStatusPageSection, propAction, actionUpdate), toolsetLegacy,
		},
		{
			legacyDef(deleteStatusPageSectionDef, "manage_status_page_section action=delete"),
			withFixedArg(h.toolManageStatusPageSection, propAction, actionDelete), toolsetLegacy,
		},
		{
			legacyDef(listStatusPageResourcesDef, "get_status_page include=resources"),
			h.toolListStatusPageResources, toolsetLegacy,
		},
		{
			legacyDef(createStatusPageResourceDef, "manage_status_page_resource action=create"),
			withFixedArg(h.toolManageStatusPageResource, propAction, actionCreate), toolsetLegacy,
		},
		{
			legacyDef(updateStatusPageResourceDef, "manage_status_page_resource action=update"),
			withFixedArg(h.toolManageStatusPageResource, propAction, actionUpdate), toolsetLegacy,
		},
		{
			legacyDef(deleteStatusPageResourceDef, "manage_status_page_resource action=delete"),
			withFixedArg(h.toolManageStatusPageResource, propAction, actionDelete), toolsetLegacy,
		},
		{
			legacyDef(listStatusPageIncidentsDef, "get_status_page include=incidents"),
			h.toolListStatusPageIncidents, toolsetLegacy,
		},
		{
			legacyDef(createStatusPageIncidentDef, "manage_status_page_incident action=create"),
			withFixedArg(h.toolManageStatusPageIncident, propAction, actionCreate), toolsetLegacy,
		},
		{
			legacyDef(updateStatusPageIncidentDef, "manage_status_page_incident action=update"),
			withFixedArg(h.toolManageStatusPageIncident, propAction, actionUpdate), toolsetLegacy,
		},
		{
			legacyDef(createStatusPageIncidentUpdateDef, "manage_status_page_incident action=add_update"),
			withFixedArg(h.toolManageStatusPageIncident, propAction, actionAddUpdate), toolsetLegacy,
		},
		{
			legacyDef(createIncidentPublicationDef, "manage_status_page_incident action=publish"),
			withFixedArg(h.toolManageStatusPageIncident, propAction, actionPublish), toolsetLegacy,
		},
		{
			legacyDef(deleteIncidentPublicationDef, "manage_status_page_incident action=unpublish"),
			withFixedArg(h.toolManageStatusPageIncident, propAction, actionUnpublish), toolsetLegacy,
		},
	}
}

func (h *Handler) registerTools() {
	all := append(h.listedTools(), h.legacyTools()...)

	// h.tools holds EVERY definition (listed or alias) and is never mutated
	// afterwards; tools/list filters it per request by toolset.
	h.tools = make([]ToolDefinition, len(all))
	h.toolMap = make(map[string]toolFunc, len(all))
	h.toolsets = make(map[string]string, len(all))
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
		h.toolsets[def.Name] = all[i].toolset
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
	return map[string]any{schemaKeyType: schemaTypeString, schemaKeyDescription: desc}
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
		schemaKeyItems:       map[string]any{schemaKeyType: schemaTypeString},
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
