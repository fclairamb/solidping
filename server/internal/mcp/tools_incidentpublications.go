package mcp

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
)

// MCP argument names for the incident publication overlay (spec
// 2026-08-19-08).
const (
	propPublicationUID = "publicationUid"
	propIncidentUID    = "incidentUid"
	propState          = "state"
	propSeverity       = "severity"
	propKind           = "kind"
	propBodyMarkdown   = "bodyMarkdown"
	propActive         = "active"
)

// mcpActorUID is the author recorded for updates posted through MCP. It is
// deliberately empty: the MCP session authenticates a TOKEN, not a person, and
// stamping a random org member's UID on a public post would be a lie. An empty
// author reads as "posted through automation", which is exactly what happened.
const mcpActorUID = ""

func listStatusPageIncidentsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_status_page_incidents",
		Description: "List the incident publications on one status page — the customer-facing " +
			"incidents the page shows, distinct from the internal incidents the monitoring " +
			"system opens; use list_incidents for those. Returns {data} with one publication " +
			"per row, including its public state and stale flag. Read-only: works with " +
			"mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or slug, e.g. \"public\"."),
			propState: stringProp(
				"Filter by public state. Allowed: \"investigating\", \"identified\", \"monitoring\", \"resolved\"."),
			propActive: boolProp("When true, return only publications that are not resolved."),
		}, []string{propPageIdentifier}),
		OutputSchema: dataOutputSchema(
			"Publications on this page.",
			incidentPublicationOutputProps(),
		),
		Annotations: readOnlyAnnotations("List status page incidents"),
	}
}

func (h *Handler) toolListStatusPageIncidents(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	page := getStringArg(args, propPageIdentifier)
	if page == "" {
		return errorResult("pageIdentifier is required")
	}

	opts := incidentpublications.ListOptions{State: getStringArg(args, propState)}
	if active := getBoolArg(args, propActive); active != nil {
		opts.ActiveOnly = *active
	}

	pubs, err := h.publicationsSvc.ListPublications(ctx, orgSlug, page, opts)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(map[string]any{schemaKeyData: pubs})
}

func createStatusPageIncidentDef() ToolDefinition {
	return ToolDefinition{
		Name: "create_status_page_incident",
		Description: "Publish a hand-written incident on a status page; the title and body are shown " +
			"to CUSTOMERS, so never paste probe output, error strings, internal hostnames or IPs " +
			"into them. The page must already exist, and incidentUid, when given, must be an " +
			"internal incident not yet published on this page. bodyMarkdown posts the first " +
			"narrative entry, fanned out to status-page subscribers under a per-publication hourly " +
			"cap, and the publish event always reaches webhook connections. Returns the created " +
			"publication; close it later with update_status_page_incident. Use " +
			"create_incident_publication instead to relay an EXISTING internal incident, whose " +
			"public title is templated from the page's resources. Requires the mcp scope (mcp:read " +
			"tokens are refused) and at least the user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propTitle:          stringProp("Customer-facing title (required), e.g. \"Payments API is degraded\"."),
			propState: stringProp(
				"Initial public state (default \"investigating\"). Allowed: \"investigating\", " +
					"\"identified\", \"monitoring\", \"resolved\"."),
			propSeverity:     stringProp("Public badge severity. Allowed: \"minor\", \"major\", \"critical\"."),
			propIncidentUID:  stringProp("Optional UID of the internal incident this publication tracks."),
			propBodyMarkdown: stringProp("Optional first narrative entry, in Markdown."),
		}, []string{propPageIdentifier, propTitle}),
		OutputSchema: incidentPublicationOutputSchema(),
		Annotations:  createAnnotations("Create status page incident"),
	}
}

func (h *Handler) toolCreateStatusPageIncident(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	page := getStringArg(args, propPageIdentifier)
	title := getStringArg(args, propTitle)

	if page == "" || title == "" {
		return errorResult("pageIdentifier and title are required")
	}

	req := &incidentpublications.CreatePublicationRequest{Title: title}
	if v := getStringArg(args, propState); v != "" {
		req.State = &v
	}

	if v := getStringArg(args, propSeverity); v != "" {
		req.Severity = &v
	}

	if v := getStringArg(args, propIncidentUID); v != "" {
		req.IncidentUID = &v
	}

	if v := getStringArg(args, propBodyMarkdown); v != "" {
		req.BodyMarkdown = &v
	}

	pub, err := h.publicationsSvc.CreatePublication(ctx, orgSlug, page, mcpActorUID, req)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(pub)
}

func updateStatusPageIncidentDef() ToolDefinition {
	return ToolDefinition{
		Name: "update_status_page_incident",
		Description: "Update a published incident's title, severity or state (PATCH semantics — " +
			"omitted fields are kept as-is). Any edit marks the publication human-authored, which " +
			"stops the auto-resolve pipeline from closing it, and moving back to an open state " +
			"clears resolvedAt. It posts no narrative entry, so subscribers are not emailed; the " +
			"update event still reaches webhook connections. Returns the updated publication. Use " +
			"create_status_page_incident_update to append a customer-visible narrative entry " +
			"instead. Requires the mcp scope (mcp:read tokens are refused) and at least the user " +
			"role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propPublicationUID: stringProp("UID of the incident publication to act on."),
			propTitle:          stringProp("New customer-facing title."),
			propState: stringProp(
				"New public state. Allowed: \"investigating\", \"identified\", \"monitoring\", \"resolved\"."),
			propSeverity: stringProp(
				"New severity. Allowed: \"minor\", \"major\", \"critical\". Pass an empty string to clear it."),
		}, []string{propPageIdentifier, propPublicationUID}),
		OutputSchema: incidentPublicationOutputSchema(),
		Annotations:  updateAnnotations("Update status page incident"),
	}
}

func (h *Handler) toolUpdateStatusPageIncident(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	page := getStringArg(args, propPageIdentifier)
	uid := getStringArg(args, propPublicationUID)

	if page == "" || uid == "" {
		return errorResult("pageIdentifier and publicationUid are required")
	}

	req := &incidentpublications.UpdatePublicationRequest{}
	if v := getStringArg(args, propTitle); v != "" {
		req.Title = &v
	}

	if v := getStringArg(args, propState); v != "" {
		req.State = &v
	}

	if _, ok := args[propSeverity]; ok {
		v := getStringArg(args, propSeverity)
		req.Severity = &v
	}

	pub, err := h.publicationsSvc.UpdatePublication(ctx, orgSlug, page, uid, mcpActorUID, req)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(pub)
}

func createStatusPageIncidentUpdateDef() ToolDefinition {
	return ToolDefinition{
		Name: "create_status_page_incident_update",
		Description: "Append a narrative update to a published incident; updates are APPEND-ONLY — " +
			"there is no edit or delete, and repeating this call posts ANOTHER entry. The body is " +
			"shown to customers: never include probe output or internal names. The new entry fans " +
			"out to status-page subscribers under a per-publication hourly cap, and its event " +
			"reaches webhook connections. Returns the created update. Requires the mcp scope " +
			"(mcp:read tokens are refused) and at least the user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or URL-friendly slug, e.g. \"public\"."),
			propPublicationUID: stringProp("UID of the incident publication to act on."),
			propKind: stringProp(
				"Update kind (required). Allowed: \"investigating\", \"identified\", \"monitoring\", " +
					"\"resolved\", \"maintenance\", \"info\". The first four also advance the publication's state."),
			propBodyMarkdown: stringProp("Update body in Markdown (required)."),
			propTitle:        stringProp("Optional headline; defaults to the publication's title."),
		}, []string{propPageIdentifier, propPublicationUID, propKind, propBodyMarkdown}),
		OutputSchema: publicationUpdateOutputSchema(),
		Annotations:  createAnnotations("Create status page incident update"),
	}
}

func (h *Handler) toolCreateStatusPageIncidentUpdate(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	page := getStringArg(args, propPageIdentifier)
	uid := getStringArg(args, propPublicationUID)
	kind := getStringArg(args, propKind)
	body := getStringArg(args, propBodyMarkdown)

	if page == "" || uid == "" || kind == "" || body == "" {
		return errorResult("pageIdentifier, publicationUid, kind and bodyMarkdown are required")
	}

	req := &incidentpublications.AppendUpdateRequest{Kind: kind, BodyMarkdown: body}
	if v := getStringArg(args, propTitle); v != "" {
		req.Title = &v
	}

	update, err := h.publicationsSvc.AppendUpdate(ctx, orgSlug, page, uid, mcpActorUID, req)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(update)
}

func createIncidentPublicationDef() ToolDefinition {
	return ToolDefinition{
		Name: "create_incident_publication",
		Description: "Publish an EXISTING internal incident onto a status page; both must already " +
			"exist, and one already published on that page is refused with a conflict. The public " +
			"title is templated from the page's own public resource names, so the internal title " +
			"built from the check slug is never exposed. It posts a templated investigating entry " +
			"— fanned out to status-page subscribers under a per-publication hourly cap — fires an " +
			"event to webhook connections, and never modifies the internal incident. Reversible: " +
			"delete_incident_publication unpublishes it for later republishing, or " +
			"update_status_page_incident resolves it. Use create_status_page_incident to write a " +
			"free-form incident with your own customer-facing text instead. Returns the created " +
			"publication. Requires the mcp scope (mcp:read tokens are refused) and at least the " +
			"user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propIncidentUID:    stringProp("UID of the internal monitoring incident."),
			propPageIdentifier: stringProp("UID or URL-friendly slug of the status page to publish on."),
			propTitle:          stringProp("Optional customer-facing title; templated from the page when omitted."),
			propSeverity:       stringProp("Public badge severity. Allowed: \"minor\", \"major\", \"critical\"."),
		}, []string{propIncidentUID, propPageIdentifier}),
		OutputSchema: incidentPublicationOutputSchema(),
		Annotations:  createAnnotations("Create incident publication"),
	}
}

func (h *Handler) toolCreateIncidentPublication(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	incidentUID := getStringArg(args, propIncidentUID)
	page := getStringArg(args, propPageIdentifier)

	if incidentUID == "" || page == "" {
		return errorResult("incidentUid and pageIdentifier are required")
	}

	req := &incidentpublications.PublishIncidentRequest{StatusPageUID: page}
	if v := getStringArg(args, propTitle); v != "" {
		req.Title = &v
	}

	if v := getStringArg(args, propSeverity); v != "" {
		req.Severity = &v
	}

	pub, err := h.publicationsSvc.PublishIncident(ctx, orgSlug, incidentUID, mcpActorUID, req)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(pub)
}

func deleteIncidentPublicationDef() ToolDefinition {
	return ToolDefinition{
		Name: "delete_incident_publication",
		Description: "Unpublish an incident from a status page. The publication row is kept for audit but " +
			"disappears from the public page, and the same incident can be published again later. " +
			"Returns {unpublished: true, publicationUid}. Only a publication linked to an internal " +
			"incident can be removed this way — resolve a free-form one with " +
			"update_status_page_incident instead. Requires the mcp scope (mcp:read tokens are " +
			"refused) and at least the user role in the organization.",
		InputSchema: objectSchema(map[string]any{
			propIncidentUID:    stringProp("UID of the internal monitoring incident."),
			propPublicationUID: stringProp("UID of the incident publication to remove from the page."),
		}, []string{propIncidentUID, propPublicationUID}),
		OutputSchema: objectSchema(map[string]any{
			schemaKeyUnpublished: boolProp("Always true on success."),
			propPublicationUID:   stringProp("UID of the publication removed from the page."),
		}, []string{schemaKeyUnpublished, propPublicationUID}),
		Annotations: deleteAnnotations("Delete incident publication"),
	}
}

func (h *Handler) toolDeleteIncidentPublication(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	incidentUID := getStringArg(args, propIncidentUID)
	uid := getStringArg(args, propPublicationUID)

	if incidentUID == "" || uid == "" {
		return errorResult("incidentUid and publicationUid are required")
	}

	if err := h.publicationsSvc.UnpublishIncident(ctx, orgSlug, incidentUID, uid, mcpActorUID); err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(map[string]any{schemaKeyUnpublished: true, propPublicationUID: uid})
}

// incidentPublicationOutputProps documents the publication fields the tools
// return (PublicationResponse). The DTO's single-publication extras
// (updates, affectedResources) are populated only by GetPublication, which no
// MCP tool calls, so they stay undeclared; extra properties remain allowed by
// default.
func incidentPublicationOutputProps() map[string]any {
	return map[string]any{
		propUID:             stringProp("Publication UID."),
		"statusPageUid":     stringProp("UID of the status page the publication lives on."),
		"incidentUid":       stringProp("Linked internal incident UID, when the publication tracks one."),
		propTitle:           stringProp("Customer-facing title."),
		propState:           stringProp("Public state: investigating, identified, monitoring or resolved."),
		"severity":          stringProp("Public severity: minor, major or critical; absent when unset."),
		"autoCreated":       boolProp("True when the publication was created by the auto-publish pipeline."),
		"humanTouched":      boolProp("True once an operator edited it, which stops auto-resolve."),
		"publishedAt":       stringProp("RFC3339 timestamp when the entry appeared on the page."),
		schemaKeyResolvedAt: stringProp("RFC3339 timestamp when it was resolved; absent while open."),
		schemaKeyCreatedAt:  stringProp("RFC3339 creation timestamp."),
		schemaKeyUpdatedAt:  stringProp("RFC3339 last-update timestamp."),
		"stale":             boolProp("True when the linked incident has resolved while the entry is still open."),
	}
}

// incidentPublicationOutputSchema is the output shape of the single-publication
// tools (create_status_page_incident, update_status_page_incident,
// create_incident_publication).
func incidentPublicationOutputSchema() map[string]any {
	return objectSchema(incidentPublicationOutputProps(), []string{propUID})
}

// publicationUpdateOutputSchema is the output shape of
// create_status_page_incident_update (PublicationUpdateResponse). authorUid is
// undeclared: MCP posts carry no author, so it is never present here.
func publicationUpdateOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		propUID:        stringProp("Update UID."),
		propKind:       stringProp("Update kind, e.g. \"investigating\" or \"resolved\"."),
		propTitle:      stringProp("Customer-facing headline."),
		"bodyMarkdown": stringProp("Update body in Markdown."),
		"publishedAt":  stringProp("RFC3339 timestamp when the update was posted."),
	}, []string{propUID})
}
