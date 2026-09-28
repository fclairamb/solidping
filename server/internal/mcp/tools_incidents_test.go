package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentnotifications"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

func TestGetIncidentDef_DocumentsEventsValue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	def := getIncidentDef()
	schema, ok := def.InputSchema.(map[string]any)
	r.True(ok)
	props, ok := schema[schemaKeyProperties].(map[string]any)
	r.True(ok)
	withProp, ok := props[propWith].(map[string]any)
	r.True(ok)
	desc, ok := withProp[schemaKeyDescription].(string)
	r.True(ok)
	r.Contains(desc, "events")
	r.Contains(desc, "50")
}

func TestToolGetIncident_MissingUID(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	result := handler.toolGetIncident(context.Background(), "test-org", map[string]any{})
	r.True(result.IsError)
	r.Contains(result.Content[0].Text, "uid is required")
}

func TestIncidentEventsCap(t *testing.T) {
	t.Parallel()
	require.New(t).Equal(50, incidentEventsCap)
}

func TestIncidentWithEvents_JSONMarshal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Date(2026, 5, 3, 10, 14, 22, 0, time.UTC)
	wrapped := IncidentWithEvents{
		IncidentResponse: &incidents.IncidentResponse{
			UID:          "inc-1",
			CheckUID:     "check-1",
			State:        "active",
			StartedAt:    now,
			FailureCount: 3,
		},
		Events: []events.EventResponse{
			{UID: "evt-1", EventType: "incident.created", ActorType: "system", CreatedAt: now},
			{UID: "evt-2", EventType: "incident.notification.sent", ActorType: "system", CreatedAt: now.Add(30 * time.Second)},
		},
	}

	raw, err := json.Marshal(wrapped)
	r.NoError(err)

	var decoded map[string]any
	r.NoError(json.Unmarshal(raw, &decoded))
	r.Equal("inc-1", decoded[propUID])
	r.Equal("active", decoded[propState])
	r.Contains(decoded, "events")
	evts, ok := decoded["events"].([]any)
	r.True(ok)
	r.Len(evts, 2)
	first, ok := evts[0].(map[string]any)
	r.True(ok)
	r.Equal("incident.created", first["eventType"])
}

func TestIncidentWithEvents_EmptyEventsStillSerialized(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	wrapped := IncidentWithEvents{
		IncidentResponse: &incidents.IncidentResponse{UID: "inc-1", State: "resolved"},
		Events:           []events.EventResponse{},
	}
	raw, err := json.Marshal(wrapped)
	r.NoError(err)

	var decoded map[string]any
	r.NoError(json.Unmarshal(raw, &decoded))
	r.Contains(decoded, "events")
	evts, ok := decoded["events"].([]any)
	r.True(ok)
	r.Empty(evts)
}

// incidentToolCase is one incident/publication tool plus the annotation class
// it must carry.
type incidentToolCase struct {
	def         ToolDefinition
	title       string
	readOnly    bool
	destructive bool
	idempotent  bool
}

func incidentToolCases() []incidentToolCase {
	return []incidentToolCase{
		{listStatusPageIncidentsDef(), "List status page incidents", true, false, true},
		{createStatusPageIncidentDef(), "Create status page incident", false, false, false},
		{updateStatusPageIncidentDef(), "Update status page incident", false, false, true},
		{createStatusPageIncidentUpdateDef(), "Create status page incident update", false, false, false},
		{createIncidentPublicationDef(), "Create incident publication", false, false, false},
		{deleteIncidentPublicationDef(), "Delete incident publication", false, true, true},
		{listIncidentsDef(), "List incidents", true, false, true},
		{getIncidentDef(), "Get incident", true, false, true},
		{incidentNotificationsListDef(), "List incident notifications", true, false, true},
	}
}

// TestIncidentToolAnnotationsAndOutputSchemas pins the two pieces every tool
// definition owes MCP clients: the behavioral-hint class with its
// sentence-case title, and an object-rooted outputSchema.
func TestIncidentToolAnnotationsAndOutputSchemas(t *testing.T) {
	t.Parallel()

	for _, tc := range incidentToolCases() {
		t.Run(tc.def.Name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.NotNil(tc.def.Annotations)
			r.Equal(tc.title, tc.def.Annotations.Title)
			r.Equal(tc.readOnly, tc.def.Annotations.ReadOnlyHint, "readOnly hint")
			r.Equal(tc.destructive, tc.def.Annotations.DestructiveHint, "destructive hint")
			r.Equal(tc.idempotent, tc.def.Annotations.IdempotentHint, "idempotent hint")
			r.False(tc.def.Annotations.OpenWorldHint, "openWorld hint")

			schema, ok := tc.def.OutputSchema.(map[string]any)
			r.True(ok, "output schema must be present")
			r.Equal(schemaTypeObject, schema[schemaKeyType], "output schema root must be an object")
			props, ok := schema[schemaKeyProperties].(map[string]any)
			r.True(ok, "output schema must declare properties")
			r.NotEmpty(props)
		})
	}
}

// TestIncidentToolDescriptionsDiscloseAuth pins the exact auth sentences the
// tool descriptions are required to carry.
func TestIncidentToolDescriptionsDiscloseAuth(t *testing.T) {
	t.Parallel()

	const readSentence = "Read-only: works with mcp:read tokens."
	const writeSentence = "Requires the mcp scope (mcp:read tokens are refused) and at least the " +
		"user role in the organization."

	for _, tc := range incidentToolCases() {
		t.Run(tc.def.Name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			if tc.readOnly {
				r.Contains(tc.def.Description, readSentence)
				return
			}

			r.Contains(tc.def.Description, writeSentence)
		})
	}
}

// newIncidentToolsEnv wires the services the incident/publication tools call
// onto an in-memory database holding one org and one status page.
func newIncidentToolsEnv(t *testing.T) (*Handler, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("mcpinc", "MCP Incident Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.CreateStatusPage(ctx, models.NewStatusPage(org.UID, "Public", "public")))

	handler := &Handler{dbService: dbSvc}
	handler.publicationsSvc = incidentpublications.NewService(dbSvc, nil, nil)

	return handler, org
}

// TestIncidentBareSliceToolsWrapData pins the {data: [...]} wrap: MCP
// structuredContent must be an object, so a bare slice return would be
// invalid against the declared outputSchema.
func TestIncidentBareSliceToolsWrapData(t *testing.T) {
	t.Parallel()

	handler, org := newIncidentToolsEnv(t)
	ctx := t.Context()

	t.Run("list_status_page_incidents", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		result := handler.toolListStatusPageIncidents(ctx, org.Slug,
			map[string]any{propPageIdentifier: "public"})
		r.False(result.IsError)

		payload, ok := result.StructuredContent.(map[string]any)
		r.True(ok, "a bare []PublicationResponse must be wrapped")
		rows, ok := payload["data"].([]incidentpublications.PublicationResponse)
		r.True(ok)
		r.Empty(rows)

		raw, err := json.Marshal(result.StructuredContent)
		r.NoError(err)
		var decoded map[string]any
		r.NoError(json.Unmarshal(raw, &decoded))
		r.Contains(decoded, "data")
	})

	t.Run("incident_notifications_list", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		result := handler.toolIncidentNotificationsList(ctx, org.Slug,
			map[string]any{propUID: "inc-none"})
		r.False(result.IsError)

		payload, ok := result.StructuredContent.(map[string]any)
		r.True(ok, "the notification rows must be wrapped")
		rows, ok := payload["data"].([]*incidentnotifications.NotificationRow)
		r.True(ok)
		r.Empty(rows)
	})
}

// TestIncidentNotificationRowOutputSchemaMatchesJSON checks every declared
// property against the DTO's real marshaled shape: camelCase keys, optional
// fields absent rather than null, user/connection always present (null when
// unset).
func TestIncidentNotificationRowOutputSchemaMatchesJSON(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sentAt := time.Date(2026, 5, 3, 10, 14, 22, 0, time.UTC)
	startedAt := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	userName := "alice"
	connName := "oncall-mail"
	connType := "email"
	stepUID := "step-1"
	repeat := 2
	skipReason := "escalation policy"
	deliveryErr := "smtp timeout"
	messageID := "msg-1"
	incidentTitle := "api-prod is down"
	incidentState := int(models.IncidentStateActive)
	row := &models.IncidentNotificationRow{
		IncidentNotification: models.IncidentNotification{
			UID:         "notif-1",
			IncidentUID: "inc-1",
			EventType:   "incident.opened",
			StepUID:     &stepUID,
			RepeatIndex: &repeat,
			Source:      models.IncidentNotificationSourceEscalationUser,
			ChannelType: "email",
			Status:      models.IncidentNotificationStatusSent,
			SkipReason:  &skipReason,
			Error:       &deliveryErr,
			MessageID:   &messageID,
			CreatedAt:   sentAt,
			SentAt:      &sentAt,
		},
		UserName:          &userName,
		ConnectionName:    &connName,
		ConnectionType:    &connType,
		IncidentTitle:     &incidentTitle,
		IncidentState:     &incidentState,
		IncidentStartedAt: &startedAt,
	}

	raw, err := json.Marshal(incidentnotifications.NotificationRowFromModel(row))
	r.NoError(err)

	var decoded map[string]any
	r.NoError(json.Unmarshal(raw, &decoded))

	schema, ok := incidentNotificationsListDef().OutputSchema.(map[string]any)
	r.True(ok)
	dataProp, ok := schema[schemaKeyProperties].(map[string]any)[schemaKeyData].(map[string]any)
	r.True(ok)
	items, ok := dataProp[schemaKeyItems].(map[string]any)
	r.True(ok)
	itemProps, ok := items[schemaKeyProperties].(map[string]any)
	r.True(ok)
	r.NotEmpty(itemProps)

	for name, propSpec := range itemProps {
		value, present := decoded[name]
		r.True(present, "declared property %q is missing from the JSON", name)

		spec, ok := propSpec.(map[string]any)
		r.True(ok)

		types, ok := spec[schemaKeyType].([]string)
		if !ok {
			single, singleOK := spec[schemaKeyType].(string)
			r.True(singleOK, "declared property %q has no usable type", name)
			types = []string{single}
		}

		if value == nil {
			r.Contains(types, schemaTypeNull, "declared property %q is null but not nullable", name)
			continue
		}

		actual := incidentRowJSONType(value)
		matched := false
		for _, typ := range types {
			if typ == actual || (typ == schemaTypeNumber && actual == "integer") {
				matched = true
			}
		}

		r.True(matched, "declared property %q has type %q, JSON gives %q", name, types, actual)
	}
}

// incidentRowJSONType maps a decoded JSON value onto its JSON Schema type
// name.
func incidentRowJSONType(value any) string {
	switch typed := value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if typed == float64(int64(typed)) {
			return "integer"
		}

		return schemaTypeNumber
	case nil:
		return schemaTypeNull
	default:
		return schemaTypeObject
	}
}
