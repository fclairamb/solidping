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
	propCustomCSS         = "customCss"
	propCheckUID          = "checkUid"
	propAutoPublish       = "autoPublish"
	propAutoPublishDelay  = "autoPublishDelaySeconds"
	propAutoResolve       = "autoResolve"
)

// autoPublishProps are the incident auto-publication settings (spec
// 2026-08-19-08), shared verbatim by create_status_page and update_status_page
// so the two descriptions can never drift.
func autoPublishProps(schema map[string]any) map[string]any {
	schema[propAutoPublish] = boolProp(
		"Automatically publish incidents affecting this page's resources as public incidents. " +
			"New pages default to true; pages that existed before this feature shipped default to false.")
	schema[propAutoPublishDelay] = intProp(
		"Debounce in seconds before an incident becomes public (default 60). 0 publishes immediately. " +
			"An incident that resolves inside the delay is never published at all.")
	schema[propAutoResolve] = stringProp(
		"What an auto-created publication does when its incident resolves. Allowed: \"always\", " +
			"\"if_untouched\" (default — a publication a human has edited is left for them to close), \"never\".")

	return schema
}

// --- Pages ---

func listStatusPagesDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_status_pages",
		Description: "List every status page in the organization, newest first, and " +
			"return {data: [...]}. Use get_status_page for a single page by UID or " +
			"slug (with=sections for its contents) and list_status_page_sections " +
			"for one page's sections. Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{}, nil),
		OutputSchema: dataOutputSchema(
			"Status pages in the organization.",
			statusPageResponseOutputProps(),
		),
		Annotations: readOnlyAnnotations("List status pages"),
	}
}

func (h *Handler) toolListStatusPages(ctx context.Context, orgSlug string, _ map[string]any) ToolCallResult {
	pages, err := h.statusPagesSvc.ListStatusPages(ctx, orgSlug)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(map[string]any{schemaKeyData: pages})
}

func getStatusPageDef() ToolDefinition {
	return ToolDefinition{
		Name: "get_status_page",
		Description: "Get one status page by UID or slug and return it; pass " +
			"with=sections to embed its sections and their resources. Use " +
			"list_status_pages to browse every page instead. Read-only: works " +
			"with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propWith:       stringProp("\"sections\" to include nested sections and their resources"),
		}, []string{propIdentifier}),
		OutputSchema: statusPageWithSectionsOutputSchema(),
		Annotations:  readOnlyAnnotations("Get status page"),
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
		Description: "Create a new status page for the organization and return the " +
			"created page. A duplicate slug is rejected with a conflict, never " +
			"merged. Only one page can be the default: the first page always " +
			"becomes it, and isDefault:true demotes the previous default. Every " +
			"new page is seeded with a default \"Services\" section and starts " +
			"enabled and public — use create_status_page_section to add more " +
			"sections and update_status_page to edit the page later. Requires the " +
			"mcp scope (mcp:read tokens are refused) and at least the user role in " +
			"the organization.",
		InputSchema: objectSchema(autoPublishProps(map[string]any{
			schemaKeyName:        stringProp("Status page display name (required), e.g. \"Public status\"."),
			schemaKeySlug:        stringProp("URL-friendly slug (required, unique per org), e.g. \"public\"."),
			schemaKeyDescription: stringProp("Optional free-text description shown in the UI."),
			propVisibility: stringProp(
				"Visibility setting. Allowed: \"public\", \"private\". Default \"public\".",
			),
			propIsDefault:        boolProp("Whether this is the org's default status page (only one allowed)."),
			propShowAvailability: boolProp("Display availability percentage on the public page."),
			propShowResponseTime: boolProp("Display response-time charts on the public page."),
			propHistoryDays:      intProp("Days of history to show on the page (default 90)."),
			propLanguage:         stringProp("Language code, e.g. \"en\" or \"fr\"."),
			propCustomCSS: stringProp(
				"Custom CSS injected into the public page as a <style> element. Overrides the theme's CSS " +
					"custom properties (--brand, --background, --foreground, --card, --border, the status " +
					"colors, and the .dark variant). Max 64 KB; @import is rejected.",
			),
		}), []string{schemaKeyName, schemaKeySlug}),
		OutputSchema: statusPageOutputSchema(),
		Annotations:  createAnnotations("Create status page"),
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
	// Presence-based, not emptiness-based: an explicit "" is a legitimate
	// "no stylesheet" on create and a clear on update.
	if _, ok := args[propCustomCSS]; ok {
		v := getStringArg(args, propCustomCSS)
		req.CustomCSS = &v
	}
	req.AutoPublish = getBoolArg(args, propAutoPublish)
	if _, ok := args[propAutoPublishDelay]; ok {
		v := getIntArg(args, propAutoPublishDelay, 0)
		req.AutoPublishDelaySeconds = &v
	}
	if v := getStringArg(args, propAutoResolve); v != "" {
		req.AutoResolve = &v
	}
	page, err := h.statusPagesSvc.CreateStatusPage(ctx, orgSlug, req)
	if err != nil {
		return errorResult(err.Error())
	}
	return marshalResult(page)
}

func updateStatusPageDef() ToolDefinition {
	return ToolDefinition{
		Name: "update_status_page",
		Description: "Update an existing status page by UID or slug and return the " +
			"updated page. PATCH semantics — only fields you pass change, omitted " +
			"fields keep their current values; the page must already exist and a " +
			"slug that collides with another page is rejected. Use " +
			"create_status_page to make a new page and delete_status_page to remove " +
			"one instead. Requires the mcp scope (mcp:read tokens are refused) and " +
			"at least the user role in the organization.",
		InputSchema: objectSchema(autoPublishProps(map[string]any{
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
			propCustomCSS: stringProp(
				"Custom CSS injected into the public page as a <style> element (see create_status_page). " +
					"Max 64 KB; @import is rejected. Pass an empty string to clear it.",
			),
		}), []string{propIdentifier}),
		OutputSchema: statusPageOutputSchema(),
		Annotations:  updateAnnotations("Update status page"),
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
	if _, ok := args[propCustomCSS]; ok {
		v := getStringArg(args, propCustomCSS)
		req.CustomCSS = &v
	}
	req.AutoPublish = getBoolArg(args, propAutoPublish)
	if _, ok := args[propAutoPublishDelay]; ok {
		v := getIntArg(args, propAutoPublishDelay, 0)
		req.AutoPublishDelaySeconds = &v
	}
	if v := getStringArg(args, propAutoResolve); v != "" {
		req.AutoResolve = &v
	}
	return req
}

func deleteStatusPageDef() ToolDefinition {
	return ToolDefinition{
		Name: "delete_status_page",
		Description: "Soft-delete a status page by UID or slug: the page disappears " +
			"from the API and the public site, taking its sections and resources " +
			"with it, and its uploaded brand assets are deleted. There is no " +
			"undelete — use update_status_page with enabled:false to take a page " +
			"down without destroying it. Returns {deleted: true, identifier}. Use " +
			"delete_status_page_section or delete_status_page_resource to remove " +
			"part of a page instead. Requires the mcp scope (mcp:read tokens are " +
			"refused) and at least the user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
		}, []string{propIdentifier}),
		OutputSchema: objectSchema(map[string]any{
			schemaKeyDeleted: boolProp("Always true on success."),
			propIdentifier:   stringProp("The identifier that was deleted."),
		}, []string{schemaKeyDeleted, propIdentifier}),
		Annotations: deleteAnnotations("Delete status page"),
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
	return marshalResult(map[string]any{schemaKeyDeleted: true, propIdentifier: identifier})
}

// --- Sections ---

func listStatusPageSectionsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_status_page_sections",
		Description: "List the sections of one status page (UID or slug), in display " +
			"order, and return {data: [...]} — section metadata only, without their " +
			"resources. Use get_status_page with with=sections for the page with " +
			"sections and resources together, and list_status_page_resources for " +
			"one section's resources. Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
		}, []string{propPageIdentifier}),
		OutputSchema: dataOutputSchema(
			"Sections of the status page, in display order.",
			sectionResponseOutputProps(),
		),
		Annotations: readOnlyAnnotations("List status page sections"),
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
	return marshalResult(map[string]any{schemaKeyData: sections})
}

func createStatusPageSectionDef() ToolDefinition {
	return ToolDefinition{
		Name: "create_status_page_section",
		Description: "Create a section on a status page and return the created " +
			"section; the page must already exist. A duplicate slug within the page " +
			"is rejected with a conflict, and without position the section is " +
			"appended last. Every new page ships with a default \"Services\" " +
			"section. Use update_status_page_section to rename or reposition a " +
			"section and create_status_page_resource to pin a check into one. " +
			"Requires the mcp scope (mcp:read tokens are refused) and at least the " +
			"user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			schemaKeyName:      stringProp("Section display name (required), e.g. \"API services\"."),
			schemaKeySlug:      stringProp("URL-friendly slug (required, unique within the page), e.g. \"api\"."),
			propPosition:       intProp("Display position within the page (smaller renders earlier)."),
		}, []string{propPageIdentifier, schemaKeyName, schemaKeySlug}),
		OutputSchema: sectionOutputSchema(),
		Annotations:  createAnnotations("Create status page section"),
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
		Description: "Update a section of a status page and return the updated " +
			"section; the section must already exist. PATCH semantics — only " +
			"fields you pass change, omitted fields keep their current values, and " +
			"a slug that collides with another section on the page is rejected. " +
			"Use create_status_page_section to add a section and " +
			"delete_status_page_section to remove one instead. Requires the mcp " +
			"scope (mcp:read tokens are refused) and at least the user role in the " +
			"organization.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
			schemaKeyName:         stringProp("New section display name, e.g. \"API services\"."),
			schemaKeySlug:         stringProp("New URL-friendly slug, e.g. \"api\"."),
			propPosition:          intProp("New display position within the page (smaller renders earlier)."),
		}, []string{propPageIdentifier, propSectionIdentifier}),
		OutputSchema: sectionOutputSchema(),
		Annotations:  updateAnnotations("Update status page section"),
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
		Description: "Delete a section from a status page: the section and the " +
			"resources it displays disappear from the page. There is no undelete — " +
			"use delete_status_page_resource to remove a single pinned check while " +
			"keeping the section. Returns {deleted: true, identifier}. Requires the " +
			"mcp scope (mcp:read tokens are refused) and at least the user role in " +
			"the organization.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
		}, []string{propPageIdentifier, propSectionIdentifier}),
		OutputSchema: objectSchema(map[string]any{
			schemaKeyDeleted: boolProp("Always true on success."),
			propIdentifier:   stringProp("The identifier that was deleted."),
		}, []string{schemaKeyDeleted, propIdentifier}),
		Annotations: deleteAnnotations("Delete status page section"),
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
	return marshalResult(map[string]any{schemaKeyDeleted: true, propIdentifier: sectionID})
}

// --- Resources ---

func listStatusPageResourcesDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_status_page_resources",
		Description: "List the resources (pinned checks and check groups) of one " +
			"status page section, in display order, and return {data: [...]}. Use " +
			"list_status_page_sections to find the section first, or get_status_page " +
			"with with=sections for the whole page at once. Read-only: works with " +
			"mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
		}, []string{propPageIdentifier, propSectionIdentifier}),
		OutputSchema: dataOutputSchema(
			"Resources pinned to the section, in display order.",
			resourceResponseOutputProps(),
		),
		Annotations: readOnlyAnnotations("List status page resources"),
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
	return marshalResult(map[string]any{schemaKeyData: resources})
}

func createStatusPageResourceDef() ToolDefinition {
	return ToolDefinition{
		Name: "create_status_page_resource",
		Description: "Pin a check or a whole check group to a status-page section as " +
			"a publicly displayed resource, and return the created resource. The " +
			"page and section must already exist, exactly one of checkUid/" +
			"checkGroupUid must resolve to this organization, and pinning the same " +
			"target twice in one section is rejected (an existing selector-managed " +
			"row for it is replaced instead). Without position the resource " +
			"is appended last. Use update_status_page_resource to change its display " +
			"fields and delete_status_page_resource to unpin it. Requires the mcp " +
			"scope (mcp:read tokens are refused) and at least the user role in the " +
			"organization.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
			propCheckUID: stringProp(
				"Check UID or slug to pin (for example api-health). " +
					"Mutually exclusive with checkGroupUid; exactly one is required."),
			propCheckGroupUID: stringProp(
				"Check group UID or slug to pin as one aggregated component. " +
					"Mutually exclusive with checkUid; exactly one is required."),
			propPublicName:  stringProp("Display name for the public page (defaults to the check or group name)"),
			propExplanation: stringProp("Short explanation rendered under the resource"),
			propPosition:    intProp("Display position within the section"),
		}, []string{propPageIdentifier, propSectionIdentifier}),
		OutputSchema: resourceOutputSchema(),
		Annotations:  createAnnotations("Create status page resource"),
	}
}

func (h *Handler) toolCreateStatusPageResource(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	pageID := getStringArg(args, propPageIdentifier)
	sectionID := getStringArg(args, propSectionIdentifier)
	checkUID := getStringArg(args, propCheckUID)
	checkGroupUID := getStringArg(args, propCheckGroupUID)

	if pageID == "" || sectionID == "" {
		return errorResult("pageIdentifier and sectionIdentifier are required")
	}

	if (checkUID == "") == (checkGroupUID == "") {
		return errorResult("exactly one of checkUid or checkGroupUid is required")
	}

	req := statuspages.CreateResourceRequest{CheckUID: checkUID, CheckGroupUID: checkGroupUID}
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
		Name: "update_status_page_resource",
		Description: "Update a pinned resource's display name, explanation or " +
			"position and return the updated resource; the page, section and " +
			"resource must already exist. PATCH semantics — only fields you pass " +
			"change, omitted fields keep their current values, and a resource " +
			"managed by a section selector refuses position changes. Use " +
			"create_status_page_resource to pin a new check, list_status_page_resources " +
			"to find resource UIDs, and delete_status_page_resource to unpin. " +
			"Requires the mcp scope (mcp:read tokens are refused) and at least the " +
			"user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
			propResourceUID:       stringProp("Status page resource UID (returned by list/create_status_page_resource)."),
			propPublicName:        stringProp("New display name shown on the public page."),
			propExplanation:       stringProp("New short explanation rendered under the resource."),
			propPosition:          intProp("New display position within the section (smaller renders earlier)."),
		}, []string{propPageIdentifier, propSectionIdentifier, propResourceUID}),
		OutputSchema: resourceOutputSchema(),
		Annotations:  updateAnnotations("Update status page resource"),
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
		Name: "delete_status_page_resource",
		Description: "Remove a pinned resource from its section, returning " +
			"{deleted: true, identifier}; the underlying check or group itself is " +
			"untouched. The delete is permanent, a resource managed by a section " +
			"selector refuses it, and a matching selector re-adopts the freed check " +
			"right away. Use delete_status_page_section to remove a whole section " +
			"with all its resources instead. Requires the mcp scope (mcp:read " +
			"tokens are refused) and at least the user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier:    stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propSectionIdentifier: stringProp("Status page section UID or URL-friendly slug, e.g. \"api\"."),
			propResourceUID:       stringProp("Status page resource UID (returned by list/create_status_page_resource)."),
		}, []string{propPageIdentifier, propSectionIdentifier, propResourceUID}),
		OutputSchema: objectSchema(map[string]any{
			schemaKeyDeleted: boolProp("Always true on success."),
			propIdentifier:   stringProp("The identifier that was deleted."),
		}, []string{schemaKeyDeleted, propIdentifier}),
		Annotations: deleteAnnotations("Delete status page resource"),
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
	return marshalResult(map[string]any{schemaKeyDeleted: true, propIdentifier: resourceUID})
}

// Output schemas (MCP 2025-06-18). Each one documents the fields agents rely
// on; the DTOs carry more (custom-domain state, availability thresholds,
// selector diagnostics…), and extra properties stay allowed by default, so
// this documents without freezing the whole DTO into the contract.

// statusPageResponseOutputProps is the shared page shape of the four
// single-page tools and the list's items.
func statusPageResponseOutputProps() map[string]any {
	return map[string]any{
		propUID:            stringProp("Status page UID."),
		schemaKeyName:      stringProp("Display name."),
		schemaKeySlug:      stringProp("URL-friendly slug."),
		"visibility":       stringProp("\"public\", \"private\" or \"password\"."),
		propIsDefault:      boolProp("Whether this is the organization's default page."),
		schemaKeyEnabled:   boolProp("Whether the public page is served."),
		"historyPeriod":    stringProp("History window: \"24h\", \"7d\", \"30d\" or \"90d\"."),
		schemaKeyCreatedAt: stringProp("RFC3339 creation timestamp."),
	}
}

// statusPageOutputSchema is the output shape of create_status_page,
// update_status_page and the list items' element (via
// statusPageResponseOutputProps).
func statusPageOutputSchema() map[string]any {
	return objectSchema(statusPageResponseOutputProps(), []string{propUID})
}

// statusPageWithSectionsOutputSchema is get_status_page's output: the page
// plus its sections (and each section's resources) when with=sections was
// passed, absent otherwise.
func statusPageWithSectionsOutputSchema() map[string]any {
	sectionProps := sectionResponseOutputProps()
	sectionProps["resources"] = arrayOfObjectsProp(
		"Pinned checks and groups of the section.",
		resourceResponseOutputProps(),
	)

	props := statusPageResponseOutputProps()
	props["sections"] = arrayOfObjectsProp(
		"Nested sections; absent unless with=sections was passed.",
		sectionProps,
	)

	return objectSchema(props, []string{propUID})
}

// sectionResponseOutputProps is the section shape shared by the section list
// and the section write tools (and by the nested sections of get_status_page).
func sectionResponseOutputProps() map[string]any {
	return map[string]any{
		propUID:       stringProp("Section UID."),
		schemaKeyName: stringProp("Section display name."),
		schemaKeySlug: stringProp("URL-friendly slug, unique within the page."),
		"position":    intProp("Display position (smaller renders earlier)."),
		"selector": objectProp(
			"Dynamic membership rule; absent on hand-curated sections.",
		),
	}
}

func sectionOutputSchema() map[string]any {
	return objectSchema(sectionResponseOutputProps(), []string{propUID})
}

// resourceResponseOutputProps is the resource shape shared by the resource
// list and the resource write tools. Exactly one of checkUid/checkGroupUid
// is set (spec 2026-08-01-03).
func resourceResponseOutputProps() map[string]any {
	return map[string]any{
		propUID:         stringProp("Resource UID."),
		propCheckUID:    stringProp("Pinned check UID, when the resource targets a check."),
		"checkGroupUid": stringProp("Pinned check group UID, when the resource targets a group."),
		"publicName":    stringProp("Display name shown on the public page."),
		"explanation":   stringProp("Short explanation rendered under the resource."),
		"position":      intProp("Display position within the section (smaller renders earlier)."),
	}
}

func resourceOutputSchema() map[string]any {
	return objectSchema(resourceResponseOutputProps(), []string{propUID})
}
