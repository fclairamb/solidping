package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

func TestGetIncidentDef_DocumentsEventsValue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	def := getIncidentDef()
	schema, ok := def.InputSchema.(map[string]any)
	r.True(ok)
	props, ok := schema["properties"].(map[string]any)
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
	r.Equal("inc-1", decoded["uid"])
	r.Equal("active", decoded["state"])
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
