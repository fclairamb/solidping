package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentnotifications"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

const incidentEventsCap = 50

func listIncidentsDef() ToolDefinition {
	return ToolDefinition{
		Name: "list_incidents",
		Description: "List internal incidents (active or resolved) for the organization, filtered by " +
			"check, state, or time range. Returns {data, pagination} — total, size, and a cursor " +
			"naming the last row of a full page; the cursor is not read back on the next call yet, " +
			"so move forward by narrowing since/until. Use diagnose_check for one check's " +
			"incidents plus current health in one call, and list_status_page_incidents for the " +
			"customer-facing entries on a status page. Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propCheckUID: stringProp(
				"Comma-separated check UIDs or slugs to filter by, e.g. \"api-prod,db-prod\".",
			),
			propState: stringProp(
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
			propSize:   intProp(descLimit),
			propCursor: stringProp(descCursor),
		}, nil),
		OutputSchema: objectSchema(map[string]any{
			schemaKeyData: arrayOfObjectsProp(
				"Incidents on this page.",
				incidentOutputProps(),
			),
			schemaKeyPagination: map[string]any{
				schemaKeyType:        schemaTypeObject,
				schemaKeyDescription: "Page state for the filter.",
				schemaKeyProperties: map[string]any{
					"total":    intProp("Total incidents matching the filter."),
					propCursor: stringProp("UID of the last incident on a full page; not applied when passed back."),
					"size":     intProp("Page size used for this response."),
				},
			},
		}, nil),
		Annotations: readOnlyAnnotations("List incidents"),
	}
}

func (h *Handler) toolListIncidents(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	opts := &incidents.ListIncidentsOptions{
		Cursor: getStringArg(args, propCursor),
		Size:   getIntArg(args, "size", 20),
	}

	if opts.Size < 1 {
		opts.Size = 1
	}
	if opts.Size > 100 {
		opts.Size = 100
	}

	if v := getStringArg(args, propCheckUID); v != "" {
		opts.CheckUIDs = strings.Split(v, ",")
	}
	if v := getStringArg(args, propState); v != "" {
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
			if strings.TrimSpace(part) == schemaKeyCheck {
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
		Description: "Get a single incident by UID; an unknown UID fails with an \"incident not " +
			"found\" error. Returns the incident object — number, state, timestamps, failure " +
			"counts — plus the check with with=check and up to 50 timeline events with " +
			"with=events. Use list_incidents to search and filter across incidents, and " +
			"diagnose_check for a check's full triage briefing. Read-only: works with mcp:read " +
			"tokens.",
		InputSchema: objectSchema(map[string]any{
			propUID: stringProp("Incident UID returned by list_incidents or diagnose_check."),
			propWith: stringProp(
				"Comma-separated extra fields. \"check\" includes the underlying " +
					"check; \"events\" includes up to 50 most-recent timeline events " +
					"(status transitions, notifications, manual notes). " +
					"Example: \"check,events\".",
			),
		}, []string{propUID}),
		OutputSchema: incidentOutputSchema(),
		Annotations:  readOnlyAnnotations("Get incident"),
	}
}

// IncidentWithEvents wraps an incident response with its event timeline. The
// events field is only populated when the caller passes with="events".
type IncidentWithEvents struct {
	*incidents.IncidentResponse
	Events []events.EventResponse `json:"events"`
}

func incidentNotificationsListDef() ToolDefinition {
	return ToolDefinition{
		Name: "incident_notifications_list",
		Description: "List the notification attempts for one incident — one row per dispatch target " +
			"with its delivery status " +
			"(pending, sent, failed, cancelled, skipped), the channel " + //nolint:misspell // the API value is "cancelled"
			"used, and the recipient name or connection. Take the incident UID from list_incidents " +
			"or get_incident. Returns {data}, newest first — the limit is clamped server-side to " +
			"500. Use get_incident with=events for the incident's own timeline instead of delivery " +
			"records. Read-only: works with mcp:read tokens.",
		InputSchema: objectSchema(map[string]any{
			propUID: stringProp("Incident UID returned by list_incidents or get_incident."),
			propStatus: stringProp(
				"Optional: filter by delivery status. " +
					"Allowed: pending, sent, failed, cancelled, skipped.", //nolint:misspell // the API value is "cancelled"
			),
			propLimit: intProp("Max results (1-500, default 20)."),
		}, []string{propUID}),
		OutputSchema: dataOutputSchema(
			"Notification attempts for the incident, newest first.",
			incidentNotificationRowOutputProps(),
		),
		Annotations: readOnlyAnnotations("List incident notifications"),
	}
}

func (h *Handler) toolIncidentNotificationsList(
	ctx context.Context, orgSlug string, args map[string]any,
) ToolCallResult {
	uid := getStringArg(args, propUID)
	if uid == "" {
		return errorResult("uid is required")
	}

	limit := getIntArg(args, "limit", 20)
	if limit < 1 {
		limit = 1
	}

	if limit > 500 {
		limit = 500
	}

	orgUID, err := func() (string, error) {
		org, orgErr := h.dbService.GetOrganizationBySlug(ctx, orgSlug)
		if orgErr != nil || org == nil {
			return "", incidentnotifications.ErrOrgNotFound
		}

		return org.UID, nil
	}()
	if err != nil {
		return errorResult(err.Error())
	}

	rows, err := h.dbService.ListIncidentNotifications(ctx, orgUID, db.ListIncidentNotificationsFilter{
		IncidentUID: uid,
		Status:      getStringArg(args, propStatus),
		Limit:       limit,
	})
	if err != nil {
		return errorResult(err.Error())
	}

	dtos := make([]*incidentnotifications.NotificationRow, len(rows))
	for i := range rows {
		dtos[i] = incidentnotifications.NotificationRowFromModel(rows[i])
	}

	return marshalResult(map[string]any{schemaKeyData: dtos})
}

func (h *Handler) toolGetIncident(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult {
	uid := getStringArg(args, propUID)
	if uid == "" {
		return errorResult("uid is required")
	}

	opts := &incidents.GetIncidentOptions{}
	withEvents := false
	if v := getStringArg(args, "with"); v != "" {
		for _, part := range strings.Split(v, ",") {
			switch strings.TrimSpace(part) {
			case schemaKeyCheck:
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

// incidentOutputProps documents the incident fields agents rely on
// (IncidentResponse). The DTO carries more (ack/snooze telemetry, group
// members, attachments…); extra properties stay allowed by default, so this
// documents without freezing the whole DTO into the contract.
func incidentOutputProps() map[string]any {
	return map[string]any{
		propUID:              stringProp("Incident UID."),
		schemaTypeNumber:     intProp("Short per-org reference, e.g. 42."),
		propCheckUID:         stringProp("UID of the check the incident is about."),
		"checkSlug":          stringProp("Slug of that check."),
		"checkName":          stringProp("Name of that check."),
		propKind:             stringProp("What the incident is about: check, slo_burn or degraded."),
		propState:            stringProp("active or resolved."),
		schemaKeyStartedAt:   stringProp("RFC3339 timestamp when the incident opened."),
		schemaKeyResolvedAt:  stringProp("RFC3339 timestamp when it resolved; absent while active."),
		"failureCount":       intProp("Number of failures recorded for the incident."),
		"relapseCount":       intProp("Number of times the incident reopened."),
		propTitle:            stringProp("Custom title, when one was set."),
		schemaKeyDescription: stringProp("Custom description, when one was set."),
		schemaKeyCheck:       objectProp("Underlying check, when requested with with=check."),
	}
}

// incidentOutputSchema is the output shape of get_incident: the incident
// object plus the timeline events that tool alone returns.
func incidentOutputSchema() map[string]any {
	props := incidentOutputProps()
	props["events"] = arrayOfObjectsProp(
		"Timeline events (status transitions, notifications, manual notes), newest first; "+
			"present when with=events was passed.",
		incidentTimelineEventOutputProps(),
	)

	return objectSchema(props, []string{propUID})
}

// incidentTimelineEventOutputProps documents one entry of the incident
// timeline (events.EventResponse).
func incidentTimelineEventOutputProps() map[string]any {
	return map[string]any{
		propUID:            stringProp("Event UID."),
		"eventType":        stringProp("Event type, e.g. \"incident.created\"."),
		"actorType":        stringProp("Who acted: system or user."),
		"actorName":        stringProp("Display name of the acting user, when known."),
		schemaKeyCreatedAt: stringProp("RFC3339 timestamp of the event."),
	}
}

// incidentNotificationRowOutputProps documents one delivery attempt as the
// REST DTO returns it (incidentnotifications.NotificationRow): camelCase
// keys, optional fields absent rather than null, and user/connection always
// present (null when the target is the other kind).
func incidentNotificationRowOutputProps() map[string]any {
	return map[string]any{
		propUID:       stringProp("Notification row UID."),
		"incidentUid": stringProp("Incident the attempt belongs to."),
		"eventType":   stringProp("Incident event that triggered the attempt."),
		"source": stringProp(
			"What triggered it, e.g. check_connection, escalation_user or escalation_schedule.",
		),
		"channelType": stringProp("Delivery channel, e.g. email or slack."),
		propStatus: stringProp(
			"Delivery status: pending, sent, failed, cancelled or skipped.", //nolint:misspell // the API value is "cancelled"
		),
		schemaKeyCreatedAt: stringProp("RFC3339 timestamp when the attempt was recorded."),
		"sentAt":           stringProp("RFC3339 timestamp when it was delivered; absent until sent."),
		"stepUid":          stringProp("Escalation step that produced the attempt; absent for direct sends."),
		"repeatIndex":      intProp("Escalation repeat round; absent for direct sends."),
		"skipReason":       stringProp("Why the attempt was skipped; absent when it was not skipped."),
		"error":            stringProp("Delivery error; absent when there was none."),
		"messageId":        stringProp("Provider message ID; absent when the channel did not return one."),
		"user": map[string]any{
			schemaKeyType:        []string{schemaTypeObject, schemaTypeNull},
			schemaKeyDescription: "Targeted user {uid, name}, or null when the target was a connection.",
			schemaKeyProperties: map[string]any{
				propUID:       stringProp("User UID."),
				schemaKeyName: stringProp("User display name."),
			},
		},
		"connection": map[string]any{
			schemaKeyType:        []string{schemaTypeObject, schemaTypeNull},
			schemaKeyDescription: "Targeted integration {uid, name, type}, or null when the target was a user.",
			schemaKeyProperties: map[string]any{
				propUID:       stringProp("Integration UID."),
				schemaKeyName: stringProp("Integration name."),
				schemaKeyType: stringProp("Integration type, e.g. email or webhook."),
			},
		},
		"incident": map[string]any{
			schemaKeyType:        schemaTypeObject,
			schemaKeyDescription: "Incident context {uid, title, state, startedAt}; absent unless the query filled it.",
			schemaKeyProperties: map[string]any{
				propUID:            stringProp("Incident UID."),
				propTitle:          stringProp("Incident title."),
				propState:          stringProp("Incident state: active or resolved."),
				schemaKeyStartedAt: stringProp("RFC3339 incident start."),
			},
		},
	}
}
