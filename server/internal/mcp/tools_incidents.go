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
		Name:        "list_incidents",
		Description: "List incidents with filtering.",
		InputSchema: objectSchema(map[string]any{
			"checkUid": stringProp("Comma-separated check UIDs"),
			"state":    stringProp("Comma-separated: active, resolved"),
			"since":    stringProp("RFC3339 timestamp (started after)"),
			"until":    stringProp("RFC3339 timestamp (started before)"),
			propWith:   stringProp("\"check\" to include check details"),
			"size":     intProp("Max results (1-100, default 20)"),
			propCursor: stringProp("Pagination cursor"),
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
		Name:        "get_incident",
		Description: "Get a single incident by UID.",
		InputSchema: objectSchema(map[string]any{
			"uid": stringProp("Incident UID"),
			propWith: stringProp(
				"Comma-separated extra fields. \"check\" includes the underlying " +
					"check; \"events\" includes up to 50 most-recent timeline events " +
					"(status transitions, notifications, manual notes). " +
					"Example: \"check,events\".",
			),
		}, []string{"uid"}),
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
