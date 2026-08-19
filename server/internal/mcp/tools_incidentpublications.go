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
		Description: "List the incident publications on a status page — the customer-facing incidents, " +
			"distinct from the internal `incidents` the monitoring system opens.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or slug, e.g. \"public\"."),
			propState: stringProp(
				"Filter by public state. Allowed: \"investigating\", \"identified\", \"monitoring\", \"resolved\"."),
			propActive: boolProp("When true, return only publications that are not resolved."),
		}, []string{propPageIdentifier}),
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

	return marshalResult(pubs)
}

func createStatusPageIncidentDef() ToolDefinition {
	return ToolDefinition{
		Name: "create_status_page_incident",
		Description: "Publish a hand-written incident on a status page. The title and body are shown to " +
			"CUSTOMERS: never paste probe output, error strings, internal hostnames or IPs into them.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or slug."),
			propTitle:          stringProp("Customer-facing title (required), e.g. \"Payments API is degraded\"."),
			propState: stringProp(
				"Initial public state (default \"investigating\"). Allowed: \"investigating\", " +
					"\"identified\", \"monitoring\", \"resolved\"."),
			propSeverity:     stringProp("Public badge severity. Allowed: \"minor\", \"major\", \"critical\"."),
			propIncidentUID:  stringProp("Optional UID of the internal incident this publication tracks."),
			propBodyMarkdown: stringProp("Optional first narrative entry, in Markdown."),
		}, []string{propPageIdentifier, propTitle}),
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
		Description: "Update a published incident's title, severity or state (PATCH semantics). Any edit " +
			"marks the publication as human-authored, which stops the auto-resolve pipeline from closing it.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or slug."),
			propPublicationUID: stringProp("Publication UID."),
			propTitle:          stringProp("New customer-facing title."),
			propState: stringProp(
				"New public state. Allowed: \"investigating\", \"identified\", \"monitoring\", \"resolved\"."),
			propSeverity: stringProp(
				"New severity. Allowed: \"minor\", \"major\", \"critical\". Pass an empty string to clear it."),
		}, []string{propPageIdentifier, propPublicationUID}),
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
		Description: "Append a narrative update to a published incident. Updates are APPEND-ONLY — there " +
			"is no edit or delete. The body is shown to customers: never include probe output or internal names.",
		InputSchema: objectSchema(map[string]any{
			propPageIdentifier: stringProp("Status page UID or slug."),
			propPublicationUID: stringProp("Publication UID."),
			propKind: stringProp(
				"Update kind (required). Allowed: \"investigating\", \"identified\", \"monitoring\", " +
					"\"resolved\", \"maintenance\", \"info\". The first four also advance the publication's state."),
			propBodyMarkdown: stringProp("Update body in Markdown (required)."),
			propTitle:        stringProp("Optional headline; defaults to the publication's title."),
		}, []string{propPageIdentifier, propPublicationUID, propKind, propBodyMarkdown}),
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
		Description: "Publish an EXISTING internal incident onto a status page. The public title is " +
			"templated from the page's own public resource name — the incident's internal title, which is " +
			"built from the check slug, is never exposed.",
		InputSchema: objectSchema(map[string]any{
			propIncidentUID:    stringProp("Internal incident UID."),
			propPageIdentifier: stringProp("Status page UID or slug to publish on."),
			propTitle:          stringProp("Optional customer-facing title; templated from the page when omitted."),
			propSeverity:       stringProp("Public badge severity. Allowed: \"minor\", \"major\", \"critical\"."),
		}, []string{propIncidentUID, propPageIdentifier}),
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
			"disappears from the public page, and the same incident can be published again later.",
		InputSchema: objectSchema(map[string]any{
			propIncidentUID:    stringProp("Internal incident UID."),
			propPublicationUID: stringProp("Publication UID to remove."),
		}, []string{propIncidentUID, propPublicationUID}),
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

	return marshalResult(map[string]any{"unpublished": true, "publicationUid": uid})
}
