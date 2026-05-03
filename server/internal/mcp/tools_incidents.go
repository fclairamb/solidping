package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

const incidentEventsCap = 50

func listIncidentsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_incidents",
		Description: "List incidents (past or active) for the org, optionally filtered by check, " +
			"state, or time range. For triaging a specific check's incidents in one call, " +
			"prefer diagnose_check.",
		InputSchema: objectSchema(map[string]any{
			"checkUid": stringProp(
				"Comma-separated check UIDs or slugs to filter by, e.g. \"api-prod,db-prod\".",
			),
			"state": stringProp(
				"Comma-separated incident states. Allowed: active, resolved. " +
					"Example: \"active\" or \"active,resolved\".",
			),
			"since": stringProp(
				"Lower bound on incident start time (RFC3339), e.g. \"2026-05-03T00:00:00Z\".",
			),
			"until": stringProp(
				"Upper bound on incident start time (RFC3339), e.g. \"2026-05-04T00:00:00Z\".",
			),
			propWith: stringProp(
				"Comma-separated extra fields:\n" +
					"  check — include the underlying check (slug, type, config)\n" +
					"Example: \"check\".",
			),
			"size":     intProp(descLimit),
			propCursor: stringProp(descCursor),
		}, nil),
	}
}

func (h *Handler) toolListIncidents(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	opts := &incidents.ListIncidentsOptions{
		Cursor: getStringArg(args, "cursor"),
		Size:   getIntArg(args, "size", 20),
	}

	if opts.Size < 1 {
		opts.Size = 1
	}
	if opts.Size > 100 {
		opts.Size = 100
	}

	if v := getStringArg(args, "checkUid"); v != "" {
		opts.CheckUIDs = strings.Split(v, ",")
	}
	if v := getStringArg(args, "state"); v != "" {
		opts.States = strings.Split(v, ",")
	}
	if v := getStringArg(args, "since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Since = &t
		}
	}
	if v := getStringArg(args, "until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Until = &t
		}
	}
	if v := getStringArg(args, "with"); v != "" {
		for _, part := range strings.Split(v, ",") {
			if strings.TrimSpace(part) == "check" {
				opts.WithCheck = true
			}
		}
	}

	result, err := h.incidentsSvc.ListIncidents(ctx, orgSlug, opts)
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(result)
}

func getIncidentDef() ToolDefinition {
	return ToolDefinition{
		Name: "get_incident",
		Description: "Get a single incident by UID. Pass with=\"events\" to also include the " +
			"timeline of state transitions and notifications.",
		InputSchema: objectSchema(map[string]any{
			propUID: stringProp("Incident UID returned by list_incidents or diagnose_check."),
			propWith: stringProp(
				"Comma-separated extra fields. \"check\" includes the underlying " +
					"check; \"events\" includes up to 50 most-recent timeline events " +
					"(status transitions, notifications, manual notes). " +
					"Example: \"check,events\".",
			),
		}, []string{propUID}),
	}
}

// IncidentWithEvents wraps an incident response with its event timeline. The
// events field is only populated when the caller passes with="events".
type IncidentWithEvents struct {
	*incidents.IncidentResponse
	Events []events.EventResponse `json:"events"`
}

func (h *Handler) toolGetIncident(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	uid := getStringArg(args, "uid")
	if uid == "" {
		return errorResult("uid is required")
	}

	opts := &incidents.GetIncidentOptions{}
	withEvents := false
	if v := getStringArg(args, "with"); v != "" {
		for _, part := range strings.Split(v, ",") {
			switch strings.TrimSpace(part) {
			case "check":
				opts.WithCheck = true
			case "events":
				withEvents = true
			}
		}
	}

	incident, err := h.incidentsSvc.GetIncident(ctx, orgSlug, uid, opts)
	if err != nil {
		return errorResult(err.Error())
	}

	if !withEvents {
		return marshalResult(incident)
	}

	eventsResp, err := h.eventsSvc.ListEvents(ctx, orgSlug, &events.ListEventsOptions{
		IncidentUID: &uid,
		Size:        incidentEventsCap,
	})
	if err != nil {
		return errorResult(err.Error())
	}

	return marshalResult(IncidentWithEvents{
		IncidentResponse: incident,
		Events:           eventsResp.Data,
	})
}
