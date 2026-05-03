package mcp

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
)

const (
	propPageIdentifier    = "pageIdentifier"
	propSectionIdentifier = "sectionIdentifier"
	propResourceUID       = "resourceUid"
	propPosition          = "position"
	propPublicName        = "publicName"
	propExplanation       = "explanation"
	propVisibility        = "visibility"
	propIsDefault         = "isDefault"
	propShowAvailability  = "showAvailability"
	propShowResponseTime  = "showResponseTime"
	propHistoryDays       = "historyDays"
	propLanguage          = "language"
	propCheckUID          = "checkUid"
)

// --- Pages ---

func listStatusPagesDef() ToolDefinition {
	return ToolDefinition{
		Name:        "list_status_pages",
		Description: "List all status pages for the organization.",
		InputSchema: objectSchema(map[string]any{}, nil),
	}
}

func (h *Handler) toolListStatusPages(ctx context.Context, orgSlug string, _ map[string]any) ToolCallResult {
	pages, err := h.statusPagesSvc.ListStatusPages(ctx, orgSlug)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(pages)
}

func getStatusPageDef() ToolDefinition {
	return ToolDefinition{
		Name:        "get_status_page",
		Description: "Get a single status page by UID or slug.",
		InputSchema: objectSchema(map[string]any{
			propIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propWith:       stringProp("\"sections\" to include nested sections and their resources"),
		}, []string{propIdentifier}),
	}
}

func (h *Handler) toolGetStatusPage(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	identifier := getStringArg(args, propIdentifier)
	if identifier == "" {
		return errorResult("identifier is required")
	}
	opts := statuspages.GetStatusPageOptions{}
	if v := getStringArg(args, propWith); v == "sections" {
		opts.IncludeSections = true
	}
	page, err := h.statusPagesSvc.GetStatusPage(ctx, orgSlug, identifier, opts)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(page)
}

func createStatusPageDef() ToolDefinition {
	return ToolDefinition{
		Name: "create_status_page",
		Description: "Create a new status page for the organization. A status page is the " +
			"public-facing dashboard that displays the current health of selected checks.",
		InputSchema: objectSchema(map[string]any{
			schemaKeyName:        stringProp("Status page display name (required), e.g. \"Public status\"."),
			schemaKeySlug:        stringProp("URL-friendly slug (required, unique per org), e.g. \"public\"."),
			schemaKeyDescription: stringProp("Optional free-text description shown in the UI."),
			propVisibility: stringProp(
				"Visibility setting. Allowed: \"public\", \"private\". Default depends on system config.",
			),
			propIsDefault:        boolProp("Whether this is the org's default status page (only one allowed)."),
			propShowAvailability: boolProp("Display availability percentage on the public page."),
			propShowResponseTime: boolProp("Display response-time charts on the public page."),
			propHistoryDays:      intProp("Days of history to show on the page (default 90)."),
			propLanguage:         stringProp("Language code, e.g. \"en\" or \"fr\"."),
		}, []string{schemaKeyName, schemaKeySlug}),
	}
}

func (h *Handler) toolCreateStatusPage(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	name := getStringArg(args, schemaKeyName)
	slug := getStringArg(args, schemaKeySlug)
	if name == "" || slug == "" {
		return errorResult("name and slug are required")
	}
	req := &statuspages.CreateStatusPageRequest{
		Name: name,
		Slug: slug,
	}
	if v := getStringArg(args, schemaKeyDescription); v != "" {
		req.Description = &v
	}
	if v := getStringArg(args, propVisibility); v != "" {
		req.Visibility = &v
	}
	req.IsDefault = getBoolArg(args, propIsDefault)
	req.ShowAvailability = getBoolArg(args, propShowAvailability)
	req.ShowResponseTime = getBoolArg(args, propShowResponseTime)
	if _, ok := args[propHistoryDays]; ok {
		v := getIntArg(args, propHistoryDays, 0)
		req.HistoryDays = &v
	}
	if v := getStringArg(args, propLanguage); v != "" {
		req.Language = &v
	}
	page, err := h.statusPagesSvc.CreateStatusPage(ctx, orgSlug, req)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(page)
}

func updateStatusPageDef() ToolDefinition {
	return ToolDefinition{
		Name:        "update_status_page",
		Description: "Update an existing status page (PATCH semantics — only provided fields change).",
		InputSchema: objectSchema(map[string]any{
			propIdentifier:       stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			schemaKeyName:        stringProp("New display name, e.g. \"Public status page\"."),
			schemaKeySlug:        stringProp("New URL-friendly slug, e.g. \"public\"."),
			schemaKeyDescription: stringProp("New free-text description shown in the UI."),
			propVisibility:       stringProp("Visibility setting. Allowed: \"public\", \"private\"."),
			propIsDefault:        boolProp("Mark as the org's default page (only one allowed)."),
			schemaKeyEnabled:     boolProp("Enable or disable the public-facing page."),
			propShowAvailability: boolProp("Toggle availability percentage on the public page."),
			propShowResponseTime: boolProp("Toggle response-time charts on the public page."),
			propHistoryDays:      intProp("Days of history to show on the page (default 90)."),
			propLanguage:         stringProp("Language code, e.g. \"en\" or \"fr\"."),
		}, []string{propIdentifier}),
	}
}

func (h *Handler) toolUpdateStatusPage(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	identifier := getStringArg(args, propIdentifier)
	if identifier == "" {
		return errorResult("identifier is required")
	}
	req := buildUpdateStatusPageRequest(args)
	page, err := h.statusPagesSvc.UpdateStatusPage(ctx, orgSlug, identifier, req)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(page)
}

func buildUpdateStatusPageRequest(args map[string]any) *statuspages.UpdateStatusPageRequest {
	req := &statuspages.UpdateStatusPageRequest{}
	if v := getStringArg(args, schemaKeyName); v != "" {
		req.Name = &v
	}
	if v := getStringArg(args, schemaKeySlug); v != "" {
		req.Slug = &v
	}
	if v := getStringArg(args, schemaKeyDescription); v != "" {
		req.Description = &v
	}
	if v := getStringArg(args, propVisibility); v != "" {
		req.Visibility = &v
	}
	req.IsDefault = getBoolArg(args, propIsDefault)
	req.Enabled = getBoolArg(args, schemaKeyEnabled)
	req.ShowAvailability = getBoolArg(args, propShowAvailability)
	req.ShowResponseTime = getBoolArg(args, propShowResponseTime)
	if _, ok := args[propHistoryDays]; ok {
		v := getIntArg(args, propHistoryDays, 0)
		req.HistoryDays = &v
	}
	if v := getStringArg(args, propLanguage); v != "" {
		req.Language = &v
	}
	return req
}

func deleteStatusPageDef() ToolDefinition {
	return ToolDefinition{
		Name:        "delete_status_page",
		Description: "Soft-delete a status page by UID or slug.",
		InputSchema: objectSchema(map[string]any{
			propIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
		}, []string{propIdentifier}),
	}
}

func (h *Handler) toolDeleteStatusPage(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	identifier := getStringArg(args, propIdentifier)
	if identifier == "" {
		return errorResult("identifier is required")
	}
	if err := h.statusPagesSvc.DeleteStatusPage(ctx, orgSlug, identifier); err != nil {
		return errorResult(err.Error())
	}
	return textResult("Status page deleted successfully.")
}

// --- Sections ---

func listStatusPageSectionsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_status_page_sections",
		Description: "List sections within a status page. Sections group resources " +
			"(pinned checks) on the public-facing page.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
		}, []string{propPageIdentifier}),
	}
}

func (h *Handler) toolListStatusPageSections(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	pageID := getStringArg(args, propPageIdentifier)
	if pageID == "" {
		return errorResult("pageIdentifier is required")
	}
	sections, err := h.statusPagesSvc.ListSections(ctx, orgSlug, pageID)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(sections)
}

func createStatusPageSectionDef() ToolDefinition {
	return ToolDefinition{
		Name:        "create_status_page_section",
		Description: "Create a new section within a status page.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			schemaKeyName:      stringProp("Section display name (required), e.g. \"API services\"."),
			schemaKeySlug:      stringProp("URL-friendly slug (required, unique within the page), e.g. \"api\"."),
			propPosition:       intProp("Display position within the page (smaller renders earlier)."),
		}, []string{propPageIdentifier, schemaKeyName, schemaKeySlug}),
	}
}

func (h *Handler) toolCreateStatusPageSection(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	pageID := getStringArg(args, propPageIdentifier)
	name := getStringArg(args, schemaKeyName)
	slug := getStringArg(args, schemaKeySlug)
	if pageID == "" || name == "" || slug == "" {
		return errorResult("pageIdentifier, name, and slug are required")
	}
	req := statuspages.CreateSectionRequest{Name: name, Slug: slug}
	if _, ok := args[propPosition]; ok {
		pos := getIntArg(args, propPosition, 0)
		req.Position = &pos
	}
	section, err := h.statusPagesSvc.CreateSection(ctx, orgSlug, pageID, req)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(section)
}

func updateStatusPageSectionDef() ToolDefinition {
	return ToolDefinition{
		Name: "update_status_page_section",
		Description: "Update a section within a status page (PATCH semantics — only " +
			"provided fields change).",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
			schemaKeyName:         stringProp("New section display name, e.g. \"API services\"."),
			schemaKeySlug:         stringProp("New URL-friendly slug, e.g. \"api\"."),
			propPosition:          intProp("New display position within the page (smaller renders earlier)."),
		}, []string{propPageIdentifier, propSectionIdentifier}),
	}
}

func (h *Handler) toolUpdateStatusPageSection(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	pageID := getStringArg(args, propPageIdentifier)
	sectionID := getStringArg(args, propSectionIdentifier)
	if pageID == "" || sectionID == "" {
		return errorResult("pageIdentifier and sectionIdentifier are required")
	}
	req := statuspages.UpdateSectionRequest{}
	if v := getStringArg(args, schemaKeyName); v != "" {
		req.Name = &v
	}
	if v := getStringArg(args, schemaKeySlug); v != "" {
		req.Slug = &v
	}
	if _, ok := args[propPosition]; ok {
		pos := getIntArg(args, propPosition, 0)
		req.Position = &pos
	}
	section, err := h.statusPagesSvc.UpdateSection(ctx, orgSlug, pageID, sectionID, req)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(section)
}

func deleteStatusPageSectionDef() ToolDefinition {
	return ToolDefinition{
		Name: "delete_status_page_section",
		Description: "Delete a section from a status page. Resources in the section are " +
			"removed too.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
		}, []string{propPageIdentifier, propSectionIdentifier}),
	}
}

func (h *Handler) toolDeleteStatusPageSection(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	pageID := getStringArg(args, propPageIdentifier)
	sectionID := getStringArg(args, propSectionIdentifier)
	if pageID == "" || sectionID == "" {
		return errorResult("pageIdentifier and sectionIdentifier are required")
	}
	if err := h.statusPagesSvc.DeleteSection(ctx, orgSlug, pageID, sectionID); err != nil {
		return errorResult(err.Error())
	}
	return textResult("Status page section deleted successfully.")
}

// --- Resources ---

func listStatusPageResourcesDef() ToolDefinition {
	return ToolDefinition{
		Name:        "list_status_page_resources",
		Description: "List resources (checks pinned to a section) within a status page section.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
		}, []string{propPageIdentifier, propSectionIdentifier}),
	}
}

func (h *Handler) toolListStatusPageResources(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	pageID := getStringArg(args, propPageIdentifier)
	sectionID := getStringArg(args, propSectionIdentifier)
	if pageID == "" || sectionID == "" {
		return errorResult("pageIdentifier and sectionIdentifier are required")
	}
	resources, err := h.statusPagesSvc.ListResources(ctx, orgSlug, pageID, sectionID)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(resources)
}

func createStatusPageResourceDef() ToolDefinition {
	return ToolDefinition{
		Name:        "create_status_page_resource",
		Description: "Pin a check to a status-page section as a publicly-displayed resource.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
			propCheckUID:          stringProp("Check UID or slug to pin (required)"),
			propPublicName:        stringProp("Display name for the public page (defaults to the check name)"),
			propExplanation:       stringProp("Short explanation rendered under the resource"),
			propPosition:          intProp("Display position within the section"),
		}, []string{propPageIdentifier, propSectionIdentifier, propCheckUID}),
	}
}

func (h *Handler) toolCreateStatusPageResource(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	pageID := getStringArg(args, propPageIdentifier)
	sectionID := getStringArg(args, propSectionIdentifier)
	checkUID := getStringArg(args, propCheckUID)
	if pageID == "" || sectionID == "" || checkUID == "" {
		return errorResult("pageIdentifier, sectionIdentifier, and checkUid are required")
	}
	req := statuspages.CreateResourceRequest{CheckUID: checkUID}
	if v := getStringArg(args, propPublicName); v != "" {
		req.PublicName = &v
	}
	if v := getStringArg(args, propExplanation); v != "" {
		req.Explanation = &v
	}
	if _, ok := args[propPosition]; ok {
		pos := getIntArg(args, propPosition, 0)
		req.Position = &pos
	}
	resource, err := h.statusPagesSvc.CreateResource(ctx, orgSlug, pageID, sectionID, req)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(resource)
}

func updateStatusPageResourceDef() ToolDefinition {
	return ToolDefinition{
		Name:        "update_status_page_resource",
		Description: "Update a status page resource (display name, explanation, position).",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
			propResourceUID:       stringProp("Status page resource UID (returned by list/create_status_page_resource)."),
			propPublicName:        stringProp("New display name shown on the public page."),
			propExplanation:       stringProp("New short explanation rendered under the resource."),
			propPosition:          intProp("New display position within the section (smaller renders earlier)."),
		}, []string{propPageIdentifier, propSectionIdentifier, propResourceUID}),
	}
}

func (h *Handler) toolUpdateStatusPageResource(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	pageID := getStringArg(args, propPageIdentifier)
	sectionID := getStringArg(args, propSectionIdentifier)
	resourceUID := getStringArg(args, propResourceUID)
	if pageID == "" || sectionID == "" || resourceUID == "" {
		return errorResult("pageIdentifier, sectionIdentifier, and resourceUid are required")
	}
	req := statuspages.UpdateResourceRequest{}
	if v := getStringArg(args, propPublicName); v != "" {
		req.PublicName = &v
	}
	if v := getStringArg(args, propExplanation); v != "" {
		req.Explanation = &v
	}
	if _, ok := args[propPosition]; ok {
		pos := getIntArg(args, propPosition, 0)
		req.Position = &pos
	}
	resource, err := h.statusPagesSvc.UpdateResource(ctx, orgSlug, pageID, sectionID, resourceUID, req)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(resource)
}

func deleteStatusPageResourceDef() ToolDefinition {
	return ToolDefinition{
		Name:        "delete_status_page_resource",
		Description: "Remove a resource (pinned check) from a status-page section.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
			propResourceUID:       stringProp("Status page resource UID (returned by list/create_status_page_resource)."),
		}, []string{propPageIdentifier, propSectionIdentifier, propResourceUID}),
	}
}

func (h *Handler) toolDeleteStatusPageResource(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	pageID := getStringArg(args, propPageIdentifier)
	sectionID := getStringArg(args, propSectionIdentifier)
	resourceUID := getStringArg(args, propResourceUID)
	if pageID == "" || sectionID == "" || resourceUID == "" {
		return errorResult("pageIdentifier, sectionIdentifier, and resourceUid are required")
	}
	if err := h.statusPagesSvc.DeleteResource(ctx, orgSlug, pageID, sectionID, resourceUID); err != nil {
		return errorResult(err.Error())
	}
	return textResult("Status page resource deleted successfully.")
}
